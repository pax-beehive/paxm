package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"text/template"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type Service struct {
	cfg    config.Config
	router *memory.Router
}

type RecallInput struct {
	Query   string            `json:"query"`
	Profile string            `json:"profile,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

type RecallResult struct {
	Query          string                 `json:"query"`
	Hits           []memory.MemoryHit     `json:"hits"`
	ProviderErrors []memory.ProviderError `json:"provider_errors,omitempty"`
}

type IngestInput struct {
	Text     string            `json:"text"`
	Profile  string            `json:"profile,omitempty"`
	Source   string            `json:"source,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type IngestResult struct {
	Refs           []memory.MemoryRef     `json:"refs"`
	ProviderErrors []memory.ProviderError `json:"provider_errors,omitempty"`
}

type IngestBatchInput struct {
	Items []IngestInput `json:"items"`
}

type HookEvent struct {
	Target    string            `json:"target,omitempty"`
	Event     string            `json:"event,omitempty"`
	Query     string            `json:"query,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Limit     int               `json:"limit,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Raw       json.RawMessage   `json:"-"`
}

type HookResult struct {
	Target  string        `json:"target"`
	Event   string        `json:"event"`
	Query   string        `json:"query,omitempty"`
	Skipped bool          `json:"skipped,omitempty"`
	Recall  *RecallResult `json:"recall,omitempty"`
}

func New(cfg config.Config, router *memory.Router) *Service {
	return &Service{cfg: config.Normalize(cfg), router: router}
}

func (s *Service) Recall(ctx context.Context, input RecallInput) (RecallResult, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return RecallResult{}, errors.New("recall query is required")
	}
	policy, err := s.searchPolicy(input.Profile, input.Limit)
	if err != nil {
		return RecallResult{}, err
	}
	searchResult, err := s.router.SearchWithPolicy(ctx, memory.SearchQuery{
		Text:     query,
		Metadata: input.Meta,
	}, policy)
	result := RecallResult{
		Query:          query,
		Hits:           searchResult.Hits,
		ProviderErrors: searchResult.ProviderErrors,
	}
	return result, err
}

func (s *Service) Ingest(ctx context.Context, input IngestInput) (IngestResult, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return IngestResult{}, errors.New("ingest text is required")
	}
	policy, err := s.putPolicy(input.Profile)
	if err != nil {
		return IngestResult{}, err
	}
	putResult, err := s.router.PutWithPolicy(ctx, memory.MemoryItem{
		Text:      text,
		Source:    input.Source,
		Metadata:  input.Metadata,
		CreatedAt: time.Now().UTC(),
	}, policy)
	result := IngestResult{
		Refs:           putResult.Refs,
		ProviderErrors: putResult.ProviderErrors,
	}
	return result, err
}

func (s *Service) IngestBatch(ctx context.Context, input IngestBatchInput) (IngestResult, error) {
	grouped := make(map[string][]memory.MemoryItem)
	for _, item := range input.Items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		profile := item.Profile
		if strings.TrimSpace(profile) == "" {
			profile = "default"
		}
		grouped[profile] = append(grouped[profile], memory.MemoryItem{
			Text:      text,
			Source:    item.Source,
			Metadata:  item.Metadata,
			CreatedAt: time.Now().UTC(),
		})
	}
	if len(grouped) == 0 {
		return IngestResult{}, nil
	}

	var result IngestResult
	var errs []error
	for profile, items := range grouped {
		policy, err := s.putPolicy(profile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		putResult, err := s.router.PutBatchWithPolicy(ctx, items, policy)
		result.Refs = append(result.Refs, putResult.Refs...)
		result.ProviderErrors = append(result.ProviderErrors, putResult.ProviderErrors...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
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

	query := strings.TrimSpace(event.Query)
	if query == "" {
		var err error
		query, err = renderHookQuery(eventCfg.Recall.QueryTemplate, event)
		if err != nil {
			return result, err
		}
	}
	if query == "" {
		query = event.Prompt
	}
	limit := event.Limit
	if limit == 0 {
		limit = eventCfg.Recall.MaxResults
	}
	recall, err := s.Recall(ctx, RecallInput{
		Query:   query,
		Profile: eventCfg.Recall.Profile,
		Limit:   limit,
		Meta:    event.Metadata,
	})
	result.Query = recall.Query
	result.Recall = &recall
	return result, err
}

func (s *Service) HookWriteItem(event HookEvent) (IngestInput, bool, error) {
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
	return IngestInput{
		Text:     text,
		Profile:  eventCfg.Write.Profile,
		Source:   "hook:" + event.Target + ":" + event.Event,
		Metadata: metadata,
	}, true, nil
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
	return memory.PutPolicy{Providers: toMemoryRoutes(profile.Providers)}, nil
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
		memoryRoutes = append(memoryRoutes, memory.ProviderRoute{
			Name:     route.Name,
			Required: route.Required,
			Weight:   route.Weight,
		})
	}
	return memoryRoutes
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
		"workspace": event.Workspace,
		"metadata":  event.Metadata,
		"raw_json":  strings.TrimSpace(string(event.Raw)),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func copyMetadata(metadata map[string]string) map[string]string {
	copied := make(map[string]string, len(metadata)+3)
	for key, value := range metadata {
		copied[key] = value
	}
	return copied
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
