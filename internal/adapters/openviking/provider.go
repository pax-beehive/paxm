package openviking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const defaultTimeout = 30 * time.Second

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name    string
	baseURL string
	apiKey  string
	client  httpDoer
}

type envelope[T any] struct {
	Status string `json:"status"`
	Result T      `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type sessionResult struct {
	SessionID string `json:"session_id"`
}

type commitResult struct {
	Status string `json:"status"`
	TaskID string `json:"task_id"`
}

type findResult struct {
	Memories []matchedMemory `json:"memories"`
}

type matchedMemory struct {
	URI         string  `json:"uri"`
	Level       int     `json:"level"`
	Score       float64 `json:"score"`
	Category    string  `json:"category"`
	MatchReason string  `json:"match_reason"`
	Abstract    string  `json:"abstract"`
	Overview    string  `json:"overview"`
}

func New(name string, cfg config.ProviderConfig) (*Provider, error) {
	timeout := defaultTimeout
	if value := strings.TrimSpace(cfg.Timeout); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return nil, errors.New("openviking timeout must be a positive duration")
		}
		timeout = parsed
	}
	return newWithClient(name, cfg, &http.Client{Timeout: timeout})
}

func newWithClient(name string, cfg config.ProviderConfig, client httpDoer) (*Provider, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("openviking provider name is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("openviking base_url must be an absolute URL")
	}
	if client == nil {
		return nil, errors.New("openviking HTTP client is required")
	}
	return &Provider{name: name, baseURL: baseURL, apiKey: strings.TrimSpace(cfg.APIKey), client: client}, nil
}

func (p *Provider) Name() string { return p.name }

func (*Provider) PreserveTurnBoundaries() bool { return true }

func (p *Provider) Health(ctx context.Context) error {
	var response envelope[map[string]any]
	if err := p.doJSON(ctx, http.MethodGet, "/api/v1/stats/memories", nil, &response); err != nil {
		return err
	}
	return validateEnvelope(response.Status, response.Error)
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	text := strings.TrimSpace(query.Text)
	if text == "" {
		return nil, errors.New("openviking search query is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	payload := map[string]any{
		"query":        text,
		"context_type": "memory",
		"limit":        limit,
		"level":        2,
	}
	var response envelope[findResult]
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/search/find", payload, &response); err != nil {
		return nil, err
	}
	if err := validateEnvelope(response.Status, response.Error); err != nil {
		return nil, err
	}
	hits := make([]memory.MemoryHit, 0, min(limit, len(response.Result.Memories)))
	for _, result := range response.Result.Memories {
		uri := strings.TrimSpace(result.URI)
		content := strings.TrimSpace(result.Abstract)
		if content == "" {
			content = strings.TrimSpace(result.Overview)
		}
		if uri == "" || content == "" {
			continue
		}
		rawScore := result.Score
		relevance := min(1, max(0, result.Score))
		metadata := map[string]string{
			"openviking_uri":   uri,
			"openviking_level": strconv.Itoa(result.Level),
		}
		if result.Category != "" {
			metadata["openviking_category"] = result.Category
		}
		if result.MatchReason != "" {
			metadata["openviking_match_reason"] = result.MatchReason
		}
		if result.Overview != "" {
			metadata["openviking_overview"] = result.Overview
		}
		hit := memory.MemoryHit{
			Provider: p.name, ID: uri, Text: content, Relevance: relevance, Score: relevance,
			RawScore: &rawScore, RawScoreKind: "openviking_score", Source: "openviking", Metadata: metadata, Tier: memory.TierLTM,
		}
		hits = append(hits, memory.ApplyHitAttribution(hit))
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return memory.MemoryRef{}, errors.New("memory text is required")
	}
	var created envelope[sessionResult]
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/sessions", map[string]any{}, &created); err != nil {
		return memory.MemoryRef{}, err
	}
	if err := validateEnvelope(created.Status, created.Error); err != nil {
		return memory.MemoryRef{}, err
	}
	sessionID := strings.TrimSpace(created.Result.SessionID)
	if sessionID == "" {
		return memory.MemoryRef{}, errors.New("openviking create session returned no session_id")
	}
	message := map[string]any{"role": "user", "content": text}
	if !item.CreatedAt.IsZero() {
		message["created_at"] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	sessionPath := "/api/v1/sessions/" + url.PathEscape(sessionID)
	var added envelope[sessionResult]
	if err := p.doJSON(ctx, http.MethodPost, sessionPath+"/messages", message, &added); err != nil {
		return memory.MemoryRef{}, err
	}
	if err := validateEnvelope(added.Status, added.Error); err != nil {
		return memory.MemoryRef{}, err
	}
	var committed envelope[commitResult]
	if err := p.doJSON(ctx, http.MethodPost, sessionPath+"/commit", map[string]any{}, &committed); err != nil {
		return memory.MemoryRef{}, err
	}
	if err := validateEnvelope(committed.Status, committed.Error); err != nil {
		return memory.MemoryRef{}, err
	}
	receiptID := strings.TrimSpace(committed.Result.TaskID)
	if receiptID == "" {
		receiptID = sessionID
	}
	return memory.MemoryRef{Provider: p.name, ID: receiptID}, nil
}

func (p *Provider) doJSON(ctx context.Context, method, path string, payload, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if p.apiKey != "" {
		request.Header.Set("X-API-Key", p.apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("openviking request failed (%s): %s", response.Status, strings.TrimSpace(string(data)))
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode openviking response: %w", err)
	}
	return nil
}

func validateEnvelope(status string, apiError *struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}) error {
	if strings.EqualFold(strings.TrimSpace(status), "ok") {
		return nil
	}
	if apiError != nil && strings.TrimSpace(apiError.Message) != "" {
		return fmt.Errorf("openviking API error %s: %s", strings.TrimSpace(apiError.Code), strings.TrimSpace(apiError.Message))
	}
	return fmt.Errorf("openviking API returned status %q", status)
}
