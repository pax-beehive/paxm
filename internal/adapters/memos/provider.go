package memos

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
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const defaultTimeout = 30 * time.Second

type dialect uint8

const (
	selfHosted dialect = iota
	cloud
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name, baseURL, apiKey, userID, agentID, cubeID, searchMode string
	dialect                                                    dialect
	client                                                     httpDoer
	newID                                                      func() string
}

func NewSelfHosted(name string, cfg config.ProviderConfig) (*Provider, error) {
	return newProvider(name, cfg, selfHosted, &http.Client{Timeout: defaultTimeout}, randomID)
}

type CloudProvider struct{ provider *Provider }

func NewCloud(name string, cfg config.ProviderConfig) (*CloudProvider, error) {
	provider, err := newProvider(name, cfg, cloud, &http.Client{Timeout: defaultTimeout}, randomID)
	if err != nil {
		return nil, err
	}
	return &CloudProvider{provider: provider}, nil
}

func (p *CloudProvider) Name() string { return p.provider.Name() }

func (p *CloudProvider) Health(ctx context.Context) error { return p.provider.Health(ctx) }

func (p *CloudProvider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	return p.provider.Search(ctx, query)
}

func (p *CloudProvider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	return p.provider.Put(ctx, item)
}

func (p *CloudProvider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	return p.provider.PutBatch(ctx, items)
}

func newProvider(name string, cfg config.ProviderConfig, kind dialect, client httpDoer, newID func() string) (*Provider, error) {
	label := "memos"
	if kind == cloud {
		label = "memos cloud"
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%s provider name is required", label)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%s base_url must be an absolute URL", label)
	}
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		return nil, fmt.Errorf("%s user_id is required", label)
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if kind == cloud && apiKey == "" {
		return nil, errors.New("memos cloud api_key is required")
	}
	cubeID := strings.TrimSpace(cfg.MemCubeID)
	if kind == selfHosted && cubeID == "" {
		return nil, errors.New("memos mem_cube_id is required")
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.SearchMode))
	if mode == "" {
		mode = "fast"
	}
	if kind == selfHosted && mode != "fast" && mode != "fine" && mode != "mixture" {
		return nil, errors.New("memos search_mode must be fast, fine, or mixture")
	}
	if client == nil || newID == nil {
		return nil, fmt.Errorf("%s http dependencies are required", label)
	}
	return &Provider{name: name, baseURL: baseURL, apiKey: apiKey, userID: userID, agentID: strings.TrimSpace(cfg.AgentID), cubeID: cubeID, searchMode: mode, dialect: kind, client: client, newID: newID}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.dialect == selfHosted {
		return p.doJSON(ctx, http.MethodGet, "/health", nil, nil)
	}
	var out any
	return p.doJSON(ctx, http.MethodPost, "/search/memory", p.cloudSearchRequest("paxm health check", 1), &out)
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return nil, errors.New("memos search query is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	var payload any
	if p.dialect == selfHosted {
		payload = map[string]any{"query": text, "user_id": p.userID, "readable_cube_ids": []string{p.cubeID}, "mode": p.searchMode, "top_k": limit, "relativity": 0, "include_preference": false, "search_tool_memory": false, "include_skill_memory": false, "internet_search": false}
	} else {
		payload = p.cloudSearchRequest(text, limit)
	}
	var out any
	path := "/product/search"
	if p.dialect == cloud {
		path = "/search/memory"
	}
	if err := p.doJSON(ctx, http.MethodPost, path, payload, &out); err != nil {
		return nil, err
	}
	hits := p.hits(out, limit)
	for i := range hits {
		hits[i] = memory.ApplyHitAttribution(hits[i])
	}
	return hits, nil
}

func (p *Provider) cloudSearchRequest(query string, limit int) map[string]any {
	request := map[string]any{"user_id": p.userID, "query": query, "memory_limit_number": limit, "include_preference": false, "include_tool_memory": false}
	if p.agentID != "" {
		request["filter"] = map[string]any{"agent_id": p.agentID}
	}
	return request
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	if err := ctx.Err(); err != nil {
		return memory.MemoryRef{}, err
	}
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return memory.MemoryRef{}, errors.New("memory text is required")
	}
	writeID := p.newID()
	metadata := itemMetadata(item)
	metadata["paxm_write_id"] = writeID
	path := "/product/add"
	payload := map[string]any{"user_id": p.userID, "writable_cube_ids": []string{p.cubeID}, "async_mode": "sync", "mode": "fast", "messages": []map[string]string{{"role": "user", "content": text}}, "info": metadata}
	if p.dialect == cloud {
		path = "/add/message"
		payload = map[string]any{"user_id": p.userID, "conversation_id": writeID, "messages": []map[string]string{{"role": "user", "content": text}}, "metadata": metadata, "source": "paxm"}
		if p.agentID != "" {
			payload["agent_id"] = p.agentID
		}
	}
	var out any
	if err := p.doJSON(ctx, http.MethodPost, path, payload, &out); err != nil {
		return memory.MemoryRef{}, err
	}
	if id := firstID(out); id != "" {
		return memory.MemoryRef{Provider: p.name, ID: id}, nil
	}
	// OpenMem's add endpoint acknowledges ingestion but does not guarantee a
	// memory id. Keep the locally unique receipt useful without advertising
	// delete support for cloud writes.
	return memory.MemoryRef{Provider: p.name, ID: writeID}, nil
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	refs := make([]memory.MemoryRef, 0, len(items))
	for _, item := range items {
		ref, err := p.Put(ctx, item)
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (p *Provider) Delete(ctx context.Context, ref memory.MemoryRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.dialect == cloud {
		return errors.New("memos cloud does not expose reliable per-memory deletion through the OpenMem API")
	}
	id := strings.TrimSpace(ref.ID)
	if id == "" {
		return errors.New("memos delete requires a memory id")
	}
	if ref.Provider != "" && ref.Provider != p.name {
		return fmt.Errorf("memos delete ref belongs to provider %q", ref.Provider)
	}
	payload := map[string]any{"writable_cube_ids": []string{p.cubeID}, "memory_ids": []string{id}, "user_id": p.userID}
	var out any
	return p.doJSON(ctx, http.MethodPost, "/product/delete_memory", payload, &out)
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
	p.applyAuth(request)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("memos request failed (%s): %s", response.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode memos response: %w", err)
	}
	return validateEnvelope(out)
}

func validateEnvelope(decoded any) error {
	if pointer, ok := decoded.(*any); ok {
		decoded = *pointer
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil
	}
	if code, exists := firstNumber(root, "code"); exists && code != 0 && code != 200 {
		return fmt.Errorf("memos API error code %v: %s", code, firstString(root, "message", "error"))
	}
	data, _ := root["data"].(map[string]any)
	if strings.EqualFold(firstString(data, "status"), "failure") {
		return fmt.Errorf("memos API operation failed: %s", firstString(root, "message", "error"))
	}
	return nil
}

func (p *Provider) applyAuth(request *http.Request) {
	if p.apiKey == "" {
		return
	}
	if p.dialect == cloud {
		request.Header.Set("Authorization", "Token "+p.apiKey)
		return
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
