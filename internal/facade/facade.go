package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/tools"
)

const (
	HookRecallPhaseMetadataKey = "paxm_recall_phase"
	HookRecallPhaseInitial     = "initial"
)

type Service struct {
	cfg    config.Config
	router *memory.Router
	tools  *tools.Engine
}

type RecallInput = tools.RecallInput
type RecallResult = tools.RecallResult
type IngestInput = tools.RememberInput
type IngestResult = tools.RememberResult
type IngestBatchInput = tools.RememberBatchInput

type HookEvent struct {
	Target    string            `json:"target,omitempty"`
	Event     string            `json:"event,omitempty"`
	Query     string            `json:"query,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Assistant string            `json:"assistant,omitempty"`
	Messages  []HookMessage     `json:"messages,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Limit     int               `json:"limit,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Raw       json.RawMessage   `json:"-"`
}

type HookMessage struct {
	Role    string `json:"role,omitempty"`
	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`
	Source  string `json:"source,omitempty"`
}

type HookResult struct {
	Target  string        `json:"target"`
	Event   string        `json:"event"`
	Query   string        `json:"query,omitempty"`
	Skipped bool          `json:"skipped,omitempty"`
	Recall  *RecallResult `json:"recall,omitempty"`
}

func New(cfg config.Config, router *memory.Router) *Service {
	normalized := config.Normalize(cfg)
	return &Service{cfg: normalized, router: router, tools: tools.New(normalized, router)}
}

func (s *Service) Tools() *tools.Engine { return s.tools }

func (s *Service) Config() config.Config {
	return s.cfg
}

func (s *Service) Recall(ctx context.Context, input RecallInput) (RecallResult, error) {
	return s.tools.Recall(ctx, input)
}

func (s *Service) Ingest(ctx context.Context, input IngestInput) (IngestResult, error) {
	return s.tools.Remember(ctx, input)
}

func (s *Service) IngestBatch(ctx context.Context, input IngestBatchInput) (IngestResult, error) {
	return s.tools.RememberBatch(ctx, input)
}

func (s *Service) IngestBatchToProvider(ctx context.Context, provider string, input IngestBatchInput) (IngestResult, error) {
	return s.tools.RememberBatchToProvider(ctx, provider, input)
}

func (s *Service) CleanupExpired(ctx context.Context, limit int) (memory.CleanupExpiredResult, error) {
	return s.tools.CleanupExpired(ctx, limit)
}

func memoryItemFromIngestInput(input IngestInput) (memory.MemoryItem, string, bool) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return memory.MemoryItem{}, "", false
	}
	profile := input.Profile
	if strings.TrimSpace(profile) == "" {
		profile = "default"
	}
	return memory.MemoryItem{
		ID:            input.ID,
		Text:          text,
		AdmissionText: input.AdmissionText,
		Source:        input.Source,
		Metadata:      input.Metadata,
		CreatedAt:     effectiveCreatedAt(input.CreatedAt),
		Tier:          input.Tier,
		ExpiresAt:     input.ExpiresAt,
		Turn:          input.Turn,
	}, profile, true
}

func effectiveCreatedAt(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func (s *Service) RunHook(ctx context.Context, event HookEvent) (HookResult, error) {
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	result := HookResult{Target: event.Target, Event: event.Event}

	agentCfg, ok := s.cfg.Agents[event.Target]
	if !ok || !agentCfg.Enabled {
		result.Skipped = true
		return result, nil
	}
	eventCfg, ok := agentCfg.Hooks[event.Event]
	if !ok || !eventCfg.Recall.Enabled {
		result.Skipped = true
		return result, nil
	}

	recallCfg := effectiveHookRecallConfig(eventCfg.Recall, event)
	query := strings.TrimSpace(event.Query)
	if query == "" {
		var err error
		query, err = renderHookQuery(recallCfg.QueryTemplate, event)
		if err != nil {
			return result, err
		}
	}
	if query == "" {
		query = event.Prompt
	}
	limit := event.Limit
	if limit == 0 {
		limit = recallCfg.MaxResults
	}
	recallCtx := ctx
	cancel := func() {}
	if timeout := hookRecallOverallTimeout(recallCfg, s.cfg.RecallProfiles[recallCfg.Profile]); timeout > 0 {
		recallCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	recall, err := s.Recall(recallCtx, RecallInput{
		Query:   query,
		Profile: recallCfg.Profile,
		Limit:   limit,
		Meta:    event.Metadata,
	})
	recall.Hits = filterHookInsertionHits(recall.Hits, query, recallCfg.Insertion)
	result.Query = recall.Query
	result.Recall = &recall
	if errors.Is(err, context.DeadlineExceeded) && errors.Is(recallCtx.Err(), context.DeadlineExceeded) {
		result.Recall.TimedOut = true
		return result, nil
	}
	return result, err
}

func effectiveHookRecallConfig(recall config.HookRecallConfig, event HookEvent) config.HookRecallConfig {
	if event.Metadata == nil || event.Metadata[HookRecallPhaseMetadataKey] != HookRecallPhaseInitial || recall.Initial == nil || !recall.Initial.Enabled {
		return recall
	}
	initial := recall.Initial
	if initial.Profile != "" {
		recall.Profile = initial.Profile
	}
	if initial.QueryTemplate != "" {
		recall.QueryTemplate = initial.QueryTemplate
	}
	if initial.MaxResults != 0 {
		recall.MaxResults = initial.MaxResults
	}
	if initial.Timeout != "" {
		recall.Timeout = initial.Timeout
		if initial.TimeoutExtra == "" {
			recall.TimeoutExtra = ""
		}
	}
	if initial.TimeoutExtra != "" {
		recall.TimeoutExtra = initial.TimeoutExtra
	}
	if initial.Insertion != (config.HookInsertionConfig{}) {
		recall.Insertion = initial.Insertion
	}
	return recall
}

func hookRecallOverallTimeout(recall config.HookRecallConfig, profile config.RecallProfileConfig) time.Duration {
	extra, err := time.ParseDuration(recall.TimeoutExtra)
	if err == nil && extra > 0 {
		var longest time.Duration
		for _, route := range profile.Providers {
			timeout, parseErr := time.ParseDuration(route.Timeout)
			if parseErr == nil && timeout > longest {
				longest = timeout
			}
		}
		if longest > 0 {
			return longest + extra
		}
	}
	timeout, err := time.ParseDuration(recall.Timeout)
	if err == nil && timeout > 0 {
		return timeout
	}
	return 0
}

func (s *Service) HookWriteItem(event HookEvent) (IngestInput, bool, error) {
	event = stripRecallContextFromHookEvent(event)
	if event.Target == "" {
		event.Target = "codex"
	}
	agentCfg, ok := s.cfg.Agents[event.Target]
	if !ok || !agentCfg.Enabled {
		return IngestInput{}, false, nil
	}
	eventCfg, ok := agentCfg.Hooks[event.Event]
	if !ok || !eventCfg.Write.Enabled {
		return IngestInput{}, false, nil
	}
	text, err := renderHookTemplate(eventCfg.Write.Template, event)
	if err != nil {
		return IngestInput{}, false, err
	}
	if strings.TrimSpace(text) == "" {
		return IngestInput{}, false, nil
	}
	metadata := copyMetadata(event.Metadata)
	metadata["hook_target"] = event.Target
	metadata["hook_event"] = event.Event
	if event.Workspace != "" {
		metadata["workspace"] = event.Workspace
	}
	admissionText := ""
	if event.Event == "user_input" {
		admissionText = event.Prompt
	}
	input := IngestInput{
		Text:          text,
		AdmissionText: admissionText,
		Profile:       eventCfg.Write.Profile,
		Source:        "hook:" + event.Target + ":" + event.Event,
		Metadata:      metadata,
		AgentName:     event.Target,
	}
	policy, err := s.putPolicy(input.Profile)
	if err != nil {
		return IngestInput{}, false, err
	}
	input.Tier = policy.Tier
	input.CreatedAt = time.Now().UTC()
	if policy.ExpiresAfter > 0 {
		expiresAt := input.CreatedAt.Add(policy.ExpiresAfter)
		input.ExpiresAt = &expiresAt
	}
	return input, true, nil
}

func (s *Service) HookBufferConfig(event HookEvent) config.HookBufferConfig {
	if event.Target == "" {
		event.Target = "codex"
	}
	if agentCfg, ok := s.cfg.Agents[event.Target]; ok && agentCfg.Enabled {
		if eventCfg, ok := agentCfg.Hooks[event.Event]; ok {
			return eventCfg.Write.Buffer
		}
	}
	return config.HookBufferConfig{}
}

func (s *Service) searchPolicy(profileName string, limitOverride int) (memory.SearchPolicy, error) {
	if strings.TrimSpace(profileName) == "" {
		profileName = s.defaultActiveRecallProfile()
	}
	profile, ok := s.cfg.RecallProfiles[profileName]
	if !ok {
		return memory.SearchPolicy{}, fmtMissingProfile("recall", profileName)
	}
	limit := profile.MaxResults
	if limitOverride > 0 {
		limit = limitOverride
	}
	return memory.SearchPolicy{
		Providers:    toMemoryRoutes(profile.Providers),
		Limit:        limit,
		MinRelevance: profile.Thresholds.MinRelevance,
		MinScore:     profile.Thresholds.MinScore,
		RecencyBoost: profile.Ranking.RecencyBoost,
		Tiers:        toMemoryTiers(profile.Tiers),
	}, nil
}

func (s *Service) putPolicy(profileName string) (memory.PutPolicy, error) {
	if strings.TrimSpace(profileName) == "" {
		profileName = "default"
	}
	profile, ok := s.cfg.WriteProfiles[profileName]
	if !ok {
		return memory.PutPolicy{}, fmtMissingProfile("write", profileName)
	}
	policy := memory.PutPolicy{
		Providers: toMemoryRoutes(profile.Providers),
		Tier:      memory.NormalizeTier(memory.MemoryTier(profile.Tier)),
	}
	if strings.TrimSpace(profile.ExpiresAfter) != "" {
		expiresAfter, err := time.ParseDuration(profile.ExpiresAfter)
		if err != nil {
			return memory.PutPolicy{}, errors.New("write profile " + profileName + " expires_after is invalid: " + err.Error())
		}
		policy.ExpiresAfter = expiresAfter
	}
	return policy, nil
}

func (s *Service) defaultActiveRecallProfile() string {
	if agent, ok := s.cfg.Agents["codex"]; ok {
		if agent.ActiveRecall.Enabled && strings.TrimSpace(agent.ActiveRecall.Profile) != "" {
			return agent.ActiveRecall.Profile
		}
	}
	return "default"
}

func toMemoryRoutes(routes []config.ProviderRouteConfig) []memory.ProviderRoute {
	memoryRoutes := make([]memory.ProviderRoute, 0, len(routes))
	for _, route := range routes {
		memoryRoute := memory.ProviderRoute{
			Name:     route.Name,
			Required: route.Required,
			Weight:   route.Weight,
		}
		if timeout, err := time.ParseDuration(route.Timeout); err == nil && timeout > 0 {
			memoryRoute.Timeout = timeout
		}
		if route.Thresholds != nil {
			memoryRoute.MinRelevance = route.Thresholds.MinRelevance
			memoryRoute.MinScore = route.Thresholds.MinScore
		}
		memoryRoutes = append(memoryRoutes, memoryRoute)
	}
	return memoryRoutes
}

func toMemoryTiers(tiers []string) []memory.MemoryTier {
	memoryTiers := make([]memory.MemoryTier, 0, len(tiers))
	for _, tier := range tiers {
		memoryTiers = append(memoryTiers, memory.NormalizeTier(memory.MemoryTier(tier)))
	}
	return memory.NormalizeTiers(memoryTiers)
}

func fmtMissingProfile(kind, name string) error {
	return errors.New(kind + " profile " + name + " is not configured")
}

func renderHookQuery(queryTemplate string, event HookEvent) (string, error) {
	return renderHookTemplate(queryTemplate, event)
}

func renderHookTemplate(queryTemplate string, event HookEvent) (string, error) {
	if strings.TrimSpace(queryTemplate) == "" {
		return "", nil
	}
	tmpl, err := template.New("hook_template").Option("missingkey=zero").Parse(queryTemplate)
	if err != nil {
		return "", err
	}
	data := map[string]any{
		"target":    event.Target,
		"event":     event.Event,
		"query":     event.Query,
		"prompt":    event.Prompt,
		"assistant": event.Assistant,
		"messages":  event.Messages,
		"workspace": event.Workspace,
		"metadata":  event.Metadata,
		"safe_text": safeHookText(event),
		"raw_json":  strings.TrimSpace(string(event.Raw)),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func safeHookText(event HookEvent) string {
	label := hookTargetLabel(event.Target)
	switch strings.TrimSpace(event.Event) {
	case "session_start":
		if strings.TrimSpace(event.Workspace) == "" {
			return label + " session started."
		}
		return label + " session started.\nWorkspace: " + strings.TrimSpace(event.Workspace)
	case "user_input":
		if strings.TrimSpace(event.Prompt) == "" {
			return ""
		}
		return label + " user input:\n" + strings.TrimSpace(event.Prompt)
	case "turn_end":
		assistant := strings.TrimSpace(event.Assistant)
		messages := formatHookMessages(event.Messages, assistant)
		var sections []string
		if assistant != "" {
			sections = append(sections, label+" assistant response:\n"+assistant)
		}
		if messages != "" {
			sections = append(sections, label+" turn messages:\n"+messages)
		}
		return strings.Join(sections, "\n\n")
	case "tool_use", "tool_failure":
		if messages := formatHookMessages(event.Messages, ""); messages != "" {
			return label + " tool activity:\n" + messages
		}
		return ""
	default:
		if strings.TrimSpace(event.Prompt) == "" {
			return ""
		}
		eventName := strings.TrimSpace(event.Event)
		if eventName == "" {
			eventName = "hook event"
		}
		return label + " " + eventName + ":\n" + strings.TrimSpace(event.Prompt)
	}
}

func hookTargetLabel(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "claude":
		return "Claude Code"
	case "pi":
		return "Pi"
	case "codex", "":
		return "Codex"
	default:
		return strings.TrimSpace(target)
	}
}

func formatHookMessages(messages []HookMessage, duplicateAssistant string) string {
	var lines []string
	for _, message := range messages {
		role := normalizeHookMessageRole(message.Role)
		if role == "" {
			continue
		}
		text := strings.TrimSpace(firstNonEmpty(message.Text, message.Content))
		if text == "" {
			continue
		}
		if role == "assistant" && duplicateAssistant != "" && text == duplicateAssistant {
			continue
		}
		lines = append(lines, hookMessageRoleLabel(role)+": "+text)
	}
	return strings.Join(lines, "\n")
}

func normalizeHookMessageRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant":
		return strings.ToLower(strings.TrimSpace(role))
	case "toolcall", "tool_call", "tool_use", "function_call":
		return "tool_call"
	case "tool", "toolresult", "tool_result", "tool_response", "function_call_output", "function_result":
		return "tool_result"
	default:
		return ""
	}
}

func hookMessageRoleLabel(role string) string {
	switch role {
	case "assistant":
		return "Assistant"
	case "tool_call":
		return "Tool call"
	case "tool_result":
		return "Tool result"
	default:
		return "User"
	}
}

func copyMetadata(metadata map[string]string) map[string]string {
	copied := make(map[string]string, len(metadata)+3)
	for key, value := range metadata {
		copied[key] = value
	}
	return copied
}

func filterHookInsertionHits(hits []memory.MemoryHit, query string, policy config.HookInsertionConfig) []memory.MemoryHit {
	if policy == (config.HookInsertionConfig{}) {
		return hits
	}
	limit := policy.MaxItems
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	filtered := make([]memory.MemoryHit, 0, limit)
	for _, hit := range hits {
		if policy.MinScore > 0 && hit.Score < policy.MinScore {
			continue
		}
		if policy.RequireQueryTerms && !textMatchesQueryTerms(hit.Text, query) {
			continue
		}
		filtered = append(filtered, hit)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func textMatchesQueryTerms(text, query string) bool {
	text = strings.ToLower(text)
	for _, term := range significantQueryTerms(query) {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func significantQueryTerms(query string) []string {
	var terms []string
	for _, field := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		field = strings.TrimSpace(field)
		if len([]rune(field)) < 3 {
			continue
		}
		if isQueryStopWord(field) {
			continue
		}
		terms = append(terms, field)
	}
	return terms
}

func isQueryStopWord(value string) bool {
	switch value {
	case "about", "check", "please", "有没有", "是否", "什么", "这个", "那个":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
