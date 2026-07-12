package mem0

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const (
	defaultBaseURL = "http://localhost:8888"
	defaultTimeout = 30 * time.Second
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name    string
	baseURL string
	apiKey  string
	userID  string
	agentID string
	runID   string
	infer   *bool
	client  httpDoer
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type addRequest struct {
	Messages []message      `json:"messages"`
	UserID   string         `json:"user_id,omitempty"`
	AgentID  string         `json:"agent_id,omitempty"`
	RunID    string         `json:"run_id,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Infer    *bool          `json:"infer,omitempty"`
}

type searchRequest struct {
	Query   string         `json:"query"`
	Filters map[string]any `json:"filters,omitempty"`
	TopK    *int           `json:"top_k,omitempty"`
}

func New(name string, cfg config.ProviderConfig) (*Provider, error) {
	return newWithClient(name, cfg, &http.Client{Timeout: defaultTimeout})
}

func newWithClient(name string, cfg config.ProviderConfig, client httpDoer) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mem0 provider name is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(firstNonEmpty(cfg.BaseURL, defaultBaseURL)), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("mem0 provider base_url must be an absolute URL")
	}
	userID := strings.TrimSpace(cfg.UserID)
	agentID := strings.TrimSpace(cfg.AgentID)
	runID := strings.TrimSpace(cfg.RunID)
	if userID == "" && agentID == "" && runID == "" {
		return nil, errors.New("mem0 provider requires user_id, agent_id, or run_id")
	}
	if client == nil {
		return nil, errors.New("mem0 http client is required")
	}
	return &Provider{
		name:    name,
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(cfg.APIKey),
		userID:  userID,
		agentID: agentID,
		runID:   runID,
		infer:   cfg.Infer,
		client:  client,
	}, nil
}

func (p *Provider) Name() string {
	return p.name
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return nil, errors.New("mem0 search query is required")
	}
	request := searchRequest{
		Query:   text,
		Filters: p.searchFilters(query),
	}
	if query.Limit > 0 {
		request.TopK = intPtr(clampInt(query.Limit, 1, 100))
	}

	var response any
	if err := p.doJSON(ctx, http.MethodPost, "/search", request, &response); err != nil {
		return nil, err
	}
	hits := hitsFromResponse(response)
	for i := range hits {
		hits[i].Provider = p.name
	}
	return hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	refs, err := p.PutBatch(ctx, []memory.MemoryItem{item})
	if err != nil {
		return memory.MemoryRef{}, err
	}
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("mem0 add did not return memory ids")
	}
	return refs[0], nil
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var refs []memory.MemoryRef
	for _, item := range items {
		itemRefs, err := p.putOne(ctx, item)
		refs = append(refs, itemRefs...)
		if err != nil {
			return refs, err
		}
	}
	return refs, nil
}

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/openapi.json", nil)
	if err != nil {
		return err
	}
	p.applyAuth(request)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return p.statusError(response)
	}
	return nil
}

func (p *Provider) Delete(ctx context.Context, ref memory.MemoryRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := strings.TrimSpace(ref.ID)
	if id == "" {
		return errors.New("mem0 delete requires a memory id")
	}
	if ref.Provider != "" && ref.Provider != p.name {
		return fmt.Errorf("mem0 delete ref belongs to provider %q", ref.Provider)
	}
	return p.doJSON(ctx, http.MethodDelete, "/memories/"+url.PathEscape(id), nil, nil)
}

func (p *Provider) putOne(ctx context.Context, item memory.MemoryItem) ([]memory.MemoryRef, error) {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return nil, errors.New("memory text is required")
	}
	request := addRequest{
		Messages: []message{{Role: "user", Content: text}},
		UserID:   p.userID,
		AgentID:  p.agentID,
		RunID:    p.runID,
		Metadata: toMem0Metadata(item),
		Infer:    p.infer,
	}
	var response any
	if err := p.doJSON(ctx, http.MethodPost, "/memories", request, &response); err != nil {
		return nil, err
	}
	refs := refsFromResponse(p.name, response)
	if len(refs) == 0 {
		return nil, errors.New("mem0 add did not return memory ids")
	}
	return refs, nil
}

func (p *Provider) searchFilters(query memory.SearchQuery) map[string]any {
	filters := make(map[string]any)
	for _, key := range sortedKeys(query.Metadata) {
		if isReservedFilterKey(key) {
			continue
		}
		if value := strings.TrimSpace(query.Metadata[key]); value != "" {
			filters[key] = value
		}
	}
	if tiers := memory.NormalizeTiers(query.Tiers); len(tiers) == 1 {
		filters["paxm_tier"] = string(tiers[0])
	}
	addNonEmpty(filters, "user_id", p.userID)
	addNonEmpty(filters, "agent_id", p.agentID)
	addNonEmpty(filters, "run_id", p.runID)
	if len(filters) == 0 {
		return nil
	}
	return filters
}

func (p *Provider) doJSON(ctx context.Context, method, path string, payload any, out *any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	p.applyAuth(request)

	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return p.statusError(response)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode mem0 response: %w", err)
	}
	return nil
}

func (p *Provider) applyAuth(request *http.Request) {
	if p.apiKey == "" {
		return
	}
	if strings.HasPrefix(strings.ToLower(p.apiKey), "bearer ") {
		request.Header.Set("Authorization", p.apiKey)
		return
	}
	request.Header.Set("X-API-Key", p.apiKey)
}

func (p *Provider) statusError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	detail := response.Status
	if len(body) > 0 {
		detail = strings.TrimSpace(string(body))
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			for _, key := range []string{"detail", "error", "message"} {
				if value, ok := payload[key]; ok && value != nil {
					detail = fmt.Sprint(value)
					break
				}
			}
		}
	}
	return fmt.Errorf("mem0 %s %s: %s", response.Request.Method, response.Request.URL.Path, detail)
}

func refsFromResponse(provider string, value any) []memory.MemoryRef {
	var refs []memory.MemoryRef
	for _, object := range resultObjects(value) {
		id := stringField(object, "id", "memory_id", "uuid")
		if id == "" {
			continue
		}
		refs = append(refs, memory.MemoryRef{Provider: provider, ID: id})
	}
	return refs
}

func hitsFromResponse(value any) []memory.MemoryHit {
	var hits []memory.MemoryHit
	for _, object := range resultObjects(value) {
		id := stringField(object, "id", "memory_id", "uuid")
		text := strings.TrimSpace(stringField(object, "memory", "data", "text", "content"))
		if id == "" || text == "" {
			continue
		}
		relevance, rawScore, rawScoreKind := relevance(object)
		hits = append(hits, memory.MemoryHit{
			ID:           id,
			Text:         text,
			Relevance:    relevance,
			Score:        relevance,
			RawScore:     rawScore,
			RawScoreKind: rawScoreKind,
			Source:       "mem0",
			Metadata:     metadataFromResult(object),
			CreatedAt:    parseMem0Time(stringField(object, "created_at", "createdAt")),
		})
	}
	return hits
}

func resultObjects(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if results, ok := typed["results"]; ok {
			return resultObjects(results)
		}
		if result, ok := typed["result"]; ok {
			return resultObjects(result)
		}
		return []map[string]any{typed}
	case []any:
		objects := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				objects = append(objects, object)
			}
		}
		return objects
	default:
		return nil
	}
}

func relevance(object map[string]any) (float64, *float64, string) {
	for _, key := range []string{"score", "relevance", "similarity"} {
		score, ok := floatField(object[key])
		if !ok {
			continue
		}
		raw := score
		return normalizeScore(score), &raw, "mem0_" + key
	}
	return 1, nil, "mem0_unscored"
}

func normalizeScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score <= 1 {
		return score
	}
	return 1 / (1 + score)
}

func metadataFromResult(object map[string]any) map[string]string {
	metadata := make(map[string]string)
	if nested, ok := object["metadata"].(map[string]any); ok {
		for _, key := range sortedAnyKeys(nested) {
			if value := fmt.Sprint(nested[key]); strings.TrimSpace(value) != "" {
				metadata[key] = value
			}
		}
	}
	for _, key := range []string{"event", "user_id", "agent_id", "run_id", "hash", "updated_at", "expiration_date"} {
		if value := strings.TrimSpace(stringField(object, key)); value != "" {
			metadata["mem0_"+key] = value
		}
	}
	if details, ok := object["score_details"]; ok {
		if encoded, err := json.Marshal(details); err == nil && len(encoded) > 0 {
			metadata["mem0_score_details"] = string(encoded)
		}
	}
	return metadata
}

func toMem0Metadata(item memory.MemoryItem) map[string]any {
	metadata := make(map[string]any)
	addNonEmpty(metadata, "paxm_id", item.ID)
	addNonEmpty(metadata, "paxm_source", item.Source)
	addNonEmpty(metadata, "paxm_tier", string(memory.NormalizeTier(item.Tier)))
	if item.ExpiresAt != nil {
		metadata["paxm_expires_at"] = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.CreatedAt.IsZero() {
		metadata["paxm_created_at"] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, key := range sortedKeys(item.Metadata) {
		if strings.TrimSpace(key) == "" || isReservedFilterKey(key) {
			continue
		}
		addNonEmpty(metadata, key, item.Metadata[key])
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func stringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case json.Number:
			if strings.TrimSpace(typed.String()) != "" {
				return typed.String()
			}
		default:
			text := strings.TrimSpace(fmt.Sprint(typed))
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func floatField(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		score, err := typed.Float64()
		return score, err == nil
	case string:
		var score json.Number = json.Number(strings.TrimSpace(typed))
		parsed, err := score.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parseMem0Time(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func addNonEmpty(metadata map[string]any, key, value string) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return
	}
	metadata[key] = strings.TrimSpace(value)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedAnyKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isReservedFilterKey(key string) bool {
	switch key {
	case "user_id", "agent_id", "run_id":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func intPtr(value int) *int {
	return &value
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
