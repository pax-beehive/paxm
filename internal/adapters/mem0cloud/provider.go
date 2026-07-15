package mem0cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/scoresemantics"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const (
	defaultBaseURL    = "https://api.mem0.ai"
	defaultTimeout    = 2 * time.Minute
	eventPollInterval = 250 * time.Millisecond
	writeLookupTries  = 6
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name, baseURL, apiKey, userID, agentID, runID string
	infer                                         *bool
	scoreSemantics                                config.ScoreSemantics
	client                                        httpDoer
	writeID                                       func() string
	lookupDelay                                   func(int) time.Duration
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
	Filters map[string]any `json:"filters"`
	TopK    int            `json:"top_k,omitempty"`
}

type listRequest struct {
	Filters map[string]any `json:"filters"`
}

type eventResponse struct {
	Status  string `json:"status"`
	EventID string `json:"event_id"`
	Message string `json:"message"`
}

type listResponse struct {
	Results []result `json:"results"`
}

type result struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	Score     *float64       `json:"score"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt string         `json:"created_at"`
}

func New(name string, cfg config.ProviderConfig) (*Provider, error) {
	return newWithClient(name, cfg, &http.Client{Timeout: defaultTimeout}, randomID)
}

func newWithClient(name string, cfg config.ProviderConfig, client httpDoer, writeID func() string) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mem0 cloud provider name is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("mem0 cloud base_url must be an absolute URL")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("mem0 cloud api_key is required")
	}
	userID, agentID, runID := strings.TrimSpace(cfg.UserID), strings.TrimSpace(cfg.AgentID), strings.TrimSpace(cfg.RunID)
	if userID == "" && agentID == "" && runID == "" {
		return nil, errors.New("mem0 cloud requires user_id, agent_id, or run_id")
	}
	if client == nil || writeID == nil {
		return nil, errors.New("mem0 cloud http dependencies are required")
	}
	scoreSemantics, err := config.ParseScoreSemantics(cfg.ScoreSemantics)
	if err != nil {
		return nil, fmt.Errorf("mem0 cloud provider: %w", err)
	}
	return &Provider{name: name, baseURL: baseURL, apiKey: apiKey, userID: userID, agentID: agentID, runID: runID, infer: cfg.Infer, scoreSemantics: scoreSemantics, client: client, writeID: writeID, lookupDelay: writeLookupDelay}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var response listResponse
	return p.doJSON(ctx, http.MethodPost, "/v3/memories/?page=1&page_size=1", listRequest{Filters: p.scopeFilters(nil)}, &response)
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return nil, errors.New("mem0 cloud search query is required")
	}
	metadata := copyStrings(query.Metadata)
	if tiers := memory.NormalizeTiers(query.Tiers); len(tiers) == 1 {
		metadata["paxm_tier"] = string(tiers[0])
	}
	request := searchRequest{Query: text, Filters: p.scopeFilters(metadata)}
	if query.Limit > 0 {
		request.TopK = clamp(query.Limit, 1, 100)
	}
	var response listResponse
	if err := p.doJSON(ctx, http.MethodPost, "/v3/memories/search/", request, &response); err != nil {
		return nil, err
	}
	hits := make([]memory.MemoryHit, 0, len(response.Results))
	for _, item := range response.Results {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Memory) == "" {
			continue
		}
		score := 1.0
		kind := "mem0_cloud_unscored"
		if item.Score != nil {
			score = scoresemantics.Normalize(*item.Score, p.scoreSemantics)
			kind = scoresemantics.RawScoreKind("mem0_cloud", p.scoreSemantics)
		}
		hit := memory.MemoryHit{Provider: p.name, ID: item.ID, Text: item.Memory, Source: "mem0-cloud", Relevance: score, Score: score, RawScore: item.Score, RawScoreKind: kind, Metadata: stringMetadata(item.Metadata), CreatedAt: parseTime(item.CreatedAt)}
		hits = append(hits, memory.ApplyHitAttribution(hit))
	}
	return hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	refs, err := p.PutBatch(ctx, []memory.MemoryItem{item})
	if err != nil {
		return memory.MemoryRef{}, err
	}
	if len(refs) == 0 {
		return memory.MemoryRef{}, errors.New("mem0 cloud add did not produce memories")
	}
	return refs[0], nil
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var refs []memory.MemoryRef
	for _, item := range items {
		created, err := p.putOne(ctx, item)
		refs = append(refs, created...)
		if err != nil {
			return refs, err
		}
	}
	return refs, nil
}

func (p *Provider) putOne(ctx context.Context, item memory.MemoryItem) ([]memory.MemoryRef, error) {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return nil, errors.New("memory text is required")
	}
	writeID := p.writeID()
	metadata := itemMetadata(item)
	metadata["paxm_write_id"] = writeID
	request := addRequest{Messages: []message{{Role: "user", Content: text}}, UserID: p.userID, AgentID: p.agentID, RunID: p.runID, Metadata: metadata, Infer: p.infer}
	var event eventResponse
	if err := p.doJSON(ctx, http.MethodPost, "/v3/memories/add/", request, &event); err != nil {
		return nil, err
	}
	if strings.TrimSpace(event.EventID) == "" {
		return nil, errors.New("mem0 cloud add response has no event_id")
	}
	if err := p.waitEvent(ctx, event.EventID); err != nil {
		return nil, err
	}
	refs, err := p.lookupWriteRefs(ctx, writeID)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, errors.New("mem0 cloud event succeeded but created memories were not found")
	}
	return refs, nil
}

func (p *Provider) lookupWriteRefs(ctx context.Context, writeID string) ([]memory.MemoryRef, error) {
	filters := p.scopeFilters(map[string]string{"paxm_write_id": writeID})
	for attempt := 0; attempt < writeLookupTries; attempt++ {
		var listed listResponse
		if err := p.doJSON(ctx, http.MethodPost, "/v3/memories/?page=1&page_size=100", listRequest{Filters: filters}, &listed); err != nil {
			return nil, err
		}
		refs := refsForWrite(p.name, writeID, listed.Results)
		if len(refs) > 0 {
			return refs, nil
		}
		if attempt+1 < writeLookupTries {
			if err := waitContext(ctx, p.lookupDelay(attempt)); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

func refsForWrite(provider, writeID string, results []result) []memory.MemoryRef {
	refs := make([]memory.MemoryRef, 0, len(results))
	for _, item := range results {
		if item.ID != "" && fmt.Sprint(item.Metadata["paxm_write_id"]) == writeID {
			refs = append(refs, memory.MemoryRef{Provider: provider, ID: item.ID})
		}
	}
	return refs
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeLookupDelay(attempt int) time.Duration {
	delay := 100 * time.Millisecond << attempt
	if delay > 2*time.Second {
		return 2 * time.Second
	}
	return delay
}

func (p *Provider) waitEvent(ctx context.Context, eventID string) error {
	path := "/v1/event/" + url.PathEscape(eventID) + "/"
	deadline := time.NewTimer(defaultTimeout)
	defer deadline.Stop()
	for {
		var event eventResponse
		if err := p.doJSON(ctx, http.MethodGet, path, nil, &event); err != nil {
			return err
		}
		switch strings.ToUpper(strings.TrimSpace(event.Status)) {
		case "SUCCEEDED":
			return nil
		case "FAILED":
			return fmt.Errorf("mem0 cloud event failed: %s", strings.TrimSpace(event.Message))
		}
		timer := time.NewTimer(eventPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return errors.New("mem0 cloud event polling timed out")
		case <-timer.C:
		}
	}
}

func (p *Provider) Delete(ctx context.Context, ref memory.MemoryRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(ref.ID) == "" {
		return errors.New("mem0 cloud delete requires a memory id")
	}
	if ref.Provider != "" && ref.Provider != p.name {
		return fmt.Errorf("mem0 cloud delete ref belongs to provider %q", ref.Provider)
	}
	return p.doJSON(ctx, http.MethodDelete, "/v1/memories/"+url.PathEscape(ref.ID), nil, nil)
}

func (p *Provider) CleanupEvalScope(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.runID == "" {
		return errors.New("mem0 cloud eval cleanup requires run_id")
	}
	return p.doJSON(ctx, http.MethodDelete, "/v1/memories/?run_id="+url.QueryEscape(p.runID), nil, nil)
}

func (p *Provider) scopeFilters(extra map[string]string) map[string]any {
	filters := make(map[string]any)
	add(filters, "user_id", p.userID)
	add(filters, "agent_id", p.agentID)
	add(filters, "run_id", p.runID)
	metadata := make(map[string]any)
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "user_id" && key != "agent_id" && key != "run_id" {
			add(metadata, key, extra[key])
		}
	}
	if len(metadata) > 0 {
		filters["metadata"] = metadata
	}
	return filters
}

func (p *Provider) doJSON(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Token "+p.apiKey)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("mem0 cloud %s %s: %s", method, path, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode mem0 cloud response: %w", err)
	}
	return nil
}

func itemMetadata(item memory.MemoryItem) map[string]any {
	item = memory.PrepareProviderItem(item)
	metadata := make(map[string]any, len(item.Metadata)+5)
	add(metadata, "paxm_id", item.ID)
	add(metadata, "paxm_source", item.Source)
	add(metadata, "paxm_tier", string(memory.NormalizeTier(item.Tier)))
	if !item.CreatedAt.IsZero() {
		metadata["paxm_created_at"] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if item.ExpiresAt != nil {
		metadata["paxm_expires_at"] = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	keys := make([]string, 0, len(item.Metadata))
	for key := range item.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "user_id" && key != "agent_id" && key != "run_id" {
			add(metadata, key, item.Metadata[key])
		}
	}
	return metadata
}

func stringMetadata(values map[string]any) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			result[key] = text
		}
	}
	return result
}

func copyStrings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func add(values map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values[key] = value
	}
}
func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
