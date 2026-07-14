package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type RecallInput struct {
	Query   string            `json:"query"`
	Profile string            `json:"profile,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}
type RecallResult struct {
	Query           string                  `json:"query"`
	Hits            []memory.MemoryHit      `json:"hits"`
	ProviderErrors  []memory.ProviderError  `json:"provider_errors,omitempty"`
	ProviderRecalls []memory.ProviderRecall `json:"provider_recalls,omitempty"`
	TimedOut        bool                    `json:"timed_out,omitempty"`
}
type RememberInput struct {
	ID            string              `json:"id,omitempty"`
	Text          string              `json:"text"`
	AdmissionText string              `json:"-"`
	Profile       string              `json:"profile,omitempty"`
	Source        string              `json:"source,omitempty"`
	Metadata      map[string]string   `json:"metadata,omitempty"`
	CreatedAt     time.Time           `json:"created_at,omitempty"`
	Tier          memory.MemoryTier   `json:"tier,omitempty"`
	ExpiresAt     *time.Time          `json:"expires_at,omitempty"`
	Turn          *memory.TurnContext `json:"-"`
}
type RememberResult struct {
	Refs           []memory.MemoryRef     `json:"refs"`
	ProviderErrors []memory.ProviderError `json:"provider_errors,omitempty"`
}
type RememberBatchInput struct {
	Items []RememberInput `json:"items"`
}

// Agent is the least-privilege memory interface exposed to CLI and MCP tools.
type Agent interface {
	Recall(context.Context, RecallInput) (RecallResult, error)
	Remember(context.Context, RememberInput) (RememberResult, error)
}

// Engine implements agent tools plus internal batch operations used by capture
// and operator workflows. Runtime exposes it to agents only through Agent.
type Engine struct {
	cfg    config.Config
	router *memory.Router
}

func New(cfg config.Config, router *memory.Router) *Engine {
	return &Engine{cfg: config.Normalize(cfg), router: router}
}

func (s *Engine) Recall(ctx context.Context, input RecallInput) (RecallResult, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return RecallResult{}, errors.New("recall query is required")
	}
	policy, err := s.searchPolicy(input.Profile, input.Limit)
	if err != nil {
		return RecallResult{}, err
	}
	value, err := s.router.SearchWithPolicy(ctx, memory.SearchQuery{Text: query, Metadata: input.Meta}, policy)
	result := RecallResult{Query: query, Hits: value.Hits, ProviderErrors: value.ProviderErrors, ProviderRecalls: value.ProviderRecalls}
	if errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	return result, err
}
func (s *Engine) Remember(ctx context.Context, input RememberInput) (RememberResult, error) {
	item, profile, ok := itemFromInput(input)
	if !ok {
		return RememberResult{}, errors.New("ingest text is required")
	}
	policy, err := s.putPolicy(profile)
	if err != nil {
		return RememberResult{}, err
	}
	value, err := s.router.PutWithPolicy(ctx, item, policy)
	return RememberResult{Refs: value.Refs, ProviderErrors: value.ProviderErrors}, err
}
func (s *Engine) RememberBatch(ctx context.Context, input RememberBatchInput) (RememberResult, error) {
	return s.rememberBatch(ctx, "", input)
}
func (s *Engine) RememberBatchToProvider(ctx context.Context, provider string, input RememberBatchInput) (RememberResult, error) {
	if strings.TrimSpace(provider) == "" {
		return RememberResult{}, errors.New("provider is required")
	}
	return s.rememberBatch(ctx, strings.TrimSpace(provider), input)
}
func (s *Engine) rememberBatch(ctx context.Context, provider string, input RememberBatchInput) (RememberResult, error) {
	grouped := map[string][]memory.MemoryItem{}
	for _, inputItem := range input.Items {
		item, profile, ok := itemFromInput(inputItem)
		if ok {
			grouped[profile] = append(grouped[profile], item)
		}
	}
	if len(grouped) == 0 {
		return RememberResult{}, nil
	}
	var result RememberResult
	var errs []error
	for profile, items := range grouped {
		policy, err := s.putPolicy(profile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if provider != "" {
			policy.Providers = []memory.ProviderRoute{directProviderRoute(policy.Providers, provider)}
		}
		value, err := s.router.PutBatchWithPolicy(ctx, items, policy)
		result.Refs = append(result.Refs, value.Refs...)
		result.ProviderErrors = append(result.ProviderErrors, value.ProviderErrors...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

func directProviderRoute(routes []memory.ProviderRoute, provider string) memory.ProviderRoute {
	for _, route := range routes {
		if route.Name == provider {
			route.Required = true
			return route
		}
	}
	return memory.ProviderRoute{Name: provider, Required: true, Timeout: 30 * time.Second}
}
func (s *Engine) CleanupExpired(ctx context.Context, limit int) (memory.CleanupExpiredResult, error) {
	return s.router.CleanupExpired(ctx, limit)
}
func (s *Engine) PreservesTurnBoundaries(provider string) bool {
	return s.router != nil && s.router.PreservesTurnBoundaries(provider)
}
func (s *Engine) PutPolicy(profile string) (memory.PutPolicy, error) { return s.putPolicy(profile) }

func itemFromInput(input RememberInput) (memory.MemoryItem, string, bool) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return memory.MemoryItem{}, "", false
	}
	profile := input.Profile
	if strings.TrimSpace(profile) == "" {
		profile = "default"
	}
	created := input.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	} else {
		created = created.UTC()
	}
	return memory.MemoryItem{ID: input.ID, Text: text, AdmissionText: input.AdmissionText, Source: input.Source, Metadata: input.Metadata, CreatedAt: created, Tier: input.Tier, ExpiresAt: input.ExpiresAt, Turn: input.Turn}, profile, true
}
func (s *Engine) searchPolicy(name string, limit int) (memory.SearchPolicy, error) {
	if strings.TrimSpace(name) == "" {
		name = s.defaultRecallProfile()
	}
	profile, ok := s.cfg.RecallProfiles[name]
	if !ok {
		return memory.SearchPolicy{}, errors.New("recall profile " + name + " is not configured")
	}
	if limit <= 0 {
		limit = profile.MaxResults
	}
	return memory.SearchPolicy{Providers: routes(profile.Providers), Limit: limit, MinRelevance: profile.Thresholds.MinRelevance, MinScore: profile.Thresholds.MinScore, RecencyBoost: profile.Ranking.RecencyBoost, Tiers: tiers(profile.Tiers)}, nil
}
func (s *Engine) putPolicy(name string) (memory.PutPolicy, error) {
	if strings.TrimSpace(name) == "" {
		name = "default"
	}
	profile, ok := s.cfg.WriteProfiles[name]
	if !ok {
		return memory.PutPolicy{}, errors.New("write profile " + name + " is not configured")
	}
	policy := memory.PutPolicy{Providers: routes(profile.Providers), Tier: memory.NormalizeTier(memory.MemoryTier(profile.Tier))}
	if strings.TrimSpace(profile.ExpiresAfter) != "" {
		duration, err := time.ParseDuration(profile.ExpiresAfter)
		if err != nil {
			return memory.PutPolicy{}, errors.New("write profile " + name + " expires_after is invalid: " + err.Error())
		}
		policy.ExpiresAfter = duration
	}
	return policy, nil
}
func (s *Engine) defaultRecallProfile() string {
	if agent, ok := s.cfg.Agents["codex"]; ok && agent.ActiveRecall.Enabled && strings.TrimSpace(agent.ActiveRecall.Profile) != "" {
		return agent.ActiveRecall.Profile
	}
	return "default"
}
func routes(values []config.ProviderRouteConfig) []memory.ProviderRoute {
	result := make([]memory.ProviderRoute, 0, len(values))
	for _, route := range values {
		item := memory.ProviderRoute{Name: route.Name, Required: route.Required, Weight: route.Weight}
		if timeout, err := time.ParseDuration(route.Timeout); err == nil && timeout > 0 {
			item.Timeout = timeout
		}
		if route.Thresholds != nil {
			item.MinRelevance = route.Thresholds.MinRelevance
			item.MinScore = route.Thresholds.MinScore
		}
		result = append(result, item)
	}
	return result
}
func tiers(values []string) []memory.MemoryTier {
	result := make([]memory.MemoryTier, 0, len(values))
	for _, value := range values {
		result = append(result, memory.NormalizeTier(memory.MemoryTier(value)))
	}
	return memory.NormalizeTiers(result)
}

func WrapRecallContext(mode, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "passive" && mode != "active" {
		mode = "unknown"
	}
	content = strings.ReplaceAll(content, "</paxm-recall>", "&lt;/paxm-recall&gt;")
	content = strings.ReplaceAll(content, "<paxm-recall", "&lt;paxm-recall")
	return `<paxm-recall version="1" mode="` + mode + `">` + "\n" + content + "\n</paxm-recall>"
}
