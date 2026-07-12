package mem0

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestProviderAdapterContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
		case "/memories":
			_, _ = w.Write([]byte(`{"results":[{"id":"mem-write","memory":"mem0 adapter contract"}]}`))
		case "/search":
			_, _ = w.Write([]byte(`{"results":[{"id":"mem-hit","memory":"mem0 adapter contract","score":0.9}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider, err := New("mem0", config.ProviderConfig{BaseURL: server.URL, UserID: "contract-user"})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, provider, contracttest.Expectation{
		Name: "mem0", Item: memory.MemoryItem{Text: "mem0 adapter contract"}, Query: memory.SearchQuery{Text: "mem0 adapter contract", Limit: 3},
		RefID: "mem-write", HitID: "mem-hit", HitText: "mem0 adapter contract",
	})
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewValidatesMem0Config(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]config.ProviderConfig{
		"missing target": {BaseURL: "http://localhost:8888"},
		"bad base url":   {BaseURL: "localhost:8888", UserID: "user-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := newWithClient("mem0", tc, http.DefaultClient); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestPutCreatesMem0Memory(t *testing.T) {
	t.Parallel()

	var captured addRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/memories" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "mem0-key" {
			t.Fatalf("unexpected api key header: %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"results":[{"id":"mem-1","memory":"Todd prefers YAML config","event":"ADD"}]}`))
	}))
	defer server.Close()

	infer := false
	provider, err := New("mem0", config.ProviderConfig{
		BaseURL: server.URL,
		APIKey:  "mem0-key",
		UserID:  "user-1",
		Infer:   &infer,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC)
	ref, err := provider.Put(context.Background(), memory.MemoryItem{
		ID:        "memory-1",
		Text:      "Todd prefers YAML config",
		Source:    "test",
		Metadata:  map[string]string{"project": "paxm"},
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provider != "mem0" || ref.ID != "mem-1" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
	if captured.UserID != "user-1" || captured.AgentID != "" || captured.RunID != "" {
		t.Fatalf("unexpected target: %#v", captured)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" || captured.Messages[0].Content != "Todd prefers YAML config" {
		t.Fatalf("unexpected messages: %#v", captured.Messages)
	}
	if captured.Infer == nil || *captured.Infer {
		t.Fatalf("infer flag was not forwarded: %#v", captured.Infer)
	}
	if captured.Metadata["paxm_id"] != "memory-1" || captured.Metadata["paxm_source"] != "test" || captured.Metadata["project"] != "paxm" {
		t.Fatalf("metadata was not mapped: %#v", captured.Metadata)
	}
}

func TestDeleteRemovesMem0MemoryByRef(t *testing.T) {
	t.Parallel()
	client := httpDoerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/memories/mem-1" || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	provider, err := newWithClient("mem0", config.ProviderConfig{BaseURL: "http://mem0.test", RunID: "eval-run"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete(context.Background(), memory.MemoryRef{Provider: "mem0", ID: "mem-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchMapsMem0Results(t *testing.T) {
	t.Parallel()

	var captured searchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{
			"results": [
				{
					"id": "mem-1",
					"memory": "YAML config is the paxm default",
					"score": 0.82,
					"user_id": "user-1",
					"metadata": {"project": "paxm"},
					"created_at": "2026-07-09T01:02:03Z",
					"score_details": {"semantic": 0.82}
				}
			]
		}`))
	}))
	defer server.Close()

	provider, err := New("mem0", config.ProviderConfig{
		BaseURL: server.URL,
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:     "paxm config",
		Limit:    200,
		Metadata: map[string]string{"project": "paxm", "user_id": "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Query != "paxm config" || captured.TopK == nil || *captured.TopK != 100 {
		t.Fatalf("unexpected search request: %#v", captured)
	}
	if captured.Filters["user_id"] != "user-1" || captured.Filters["project"] != "paxm" {
		t.Fatalf("unexpected filters: %#v", captured.Filters)
	}
	if len(hits) != 1 {
		t.Fatalf("expected one hit, got %#v", hits)
	}
	hit := hits[0]
	if hit.ID != "mem-1" || hit.Text != "YAML config is the paxm default" || hit.Relevance != 0.82 {
		t.Fatalf("unexpected hit: %#v", hit)
	}
	if hit.RawScore == nil || *hit.RawScore != 0.82 || hit.RawScoreKind != "mem0_score" {
		t.Fatalf("unexpected score mapping: %#v", hit)
	}
	if hit.Metadata["project"] != "paxm" || hit.Metadata["mem0_user_id"] != "user-1" || hit.Metadata["mem0_score_details"] == "" {
		t.Fatalf("unexpected metadata: %#v", hit.Metadata)
	}
	if hit.CreatedAt.IsZero() {
		t.Fatalf("created_at was not parsed: %#v", hit)
	}
}

func TestHealthChecksOpenAPI(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
	}))
	defer server.Close()

	provider, err := New("mem0", config.ProviderConfig{BaseURL: server.URL, AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderHTTPAndMappingHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("constructor name auth and health errors", func(t *testing.T) {
		tests := []struct {
			name     string
			provider string
			cfg      config.ProviderConfig
			client   httpDoer
			ctx      func() context.Context
			wantName string
			wantErr  string
			wantAuth string
			wantXKey string
		}{
			{
				name:     "empty provider name",
				provider: "",
				cfg:      config.ProviderConfig{UserID: "user"},
				client:   http.DefaultClient,
				wantErr:  "provider name is required",
			},
			{
				name:     "nil client",
				provider: "mem0",
				cfg:      config.ProviderConfig{UserID: "user"},
				wantErr:  "http client is required",
			},
			{
				name:     "bearer auth and status detail",
				provider: "company",
				cfg:      config.ProviderConfig{UserID: "user", APIKey: "Bearer token"},
				client: httpDoerFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusBadGateway,
						Status:     "502 Bad Gateway",
						Body:       io.NopCloser(strings.NewReader(`{"detail":"offline"}`)),
						Request:    request,
					}, nil
				}),
				wantName: "company",
				wantErr:  "offline",
				wantAuth: "Bearer token",
			},
			{
				name:     "x api key health success",
				provider: "mem0",
				cfg:      config.ProviderConfig{AgentID: "agent", APIKey: "plain-key"},
				client: httpDoerFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Request:    request,
					}, nil
				}),
				wantName: "mem0",
				wantXKey: "plain-key",
			},
			{
				name:     "canceled context",
				provider: "mem0",
				cfg:      config.ProviderConfig{RunID: "run"},
				client:   http.DefaultClient,
				ctx: func() context.Context {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					return ctx
				},
				wantName: "mem0",
				wantErr:  context.Canceled.Error(),
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var seenAuth, seenXKey string
				client := tt.client
				if clientFunc, ok := client.(httpDoerFunc); ok {
					client = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
						seenAuth = request.Header.Get("Authorization")
						seenXKey = request.Header.Get("X-API-Key")
						return clientFunc(request)
					})
				}
				provider, err := newWithClient(tt.provider, tt.cfg, client)
				if err != nil {
					if tt.wantErr == "" || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("newWithClient() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if provider.Name() != tt.wantName {
					t.Fatalf("Name() = %q, want %q", provider.Name(), tt.wantName)
				}
				ctx := context.Background()
				if tt.ctx != nil {
					ctx = tt.ctx()
				}
				err = provider.Health(ctx)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("Health() error = %v, want %q", err, tt.wantErr)
					}
					if errors.Is(err, context.Canceled) {
						return
					}
				} else if err != nil {
					t.Fatalf("Health() error = %v", err)
				}
				if seenAuth != tt.wantAuth || seenXKey != tt.wantXKey {
					t.Fatalf("auth headers = %q/%q, want %q/%q", seenAuth, seenXKey, tt.wantAuth, tt.wantXKey)
				}
			})
		}
	})

	t.Run("response helpers", func(t *testing.T) {
		objects := map[string]any{
			"result": []any{
				map[string]any{"memory_id": "mem-1", "data": "first", "similarity": json.Number("2"), "metadata": map[string]any{"project": "paxm"}},
				map[string]any{"uuid": "mem-2", "content": "second", "score": "-1", "createdAt": "2026-07-09T01:02:03Z"},
				"ignored",
			},
		}
		refs := refsFromResponse("mem0", objects)
		if len(refs) != 2 || refs[0].ID != "mem-1" || refs[1].ID != "mem-2" {
			t.Fatalf("refsFromResponse() = %#v", refs)
		}
		hits := hitsFromResponse(objects)
		if len(hits) != 2 {
			t.Fatalf("hitsFromResponse() = %#v", hits)
		}
		if hits[0].Relevance != 1.0/3.0 || hits[0].RawScoreKind != "mem0_similarity" || hits[0].Metadata["project"] != "paxm" {
			t.Fatalf("first hit was not normalized: %#v", hits[0])
		}
		if hits[1].Relevance != 0 || hits[1].CreatedAt.IsZero() {
			t.Fatalf("second hit was not normalized: %#v", hits[1])
		}
		if got := resultObjects("not objects"); got != nil {
			t.Fatalf("resultObjects(non-object) = %#v", got)
		}
	})

	t.Run("scalar helpers", func(t *testing.T) {
		floatTests := []struct {
			name string
			in   any
			want float64
			ok   bool
		}{
			{name: "float", in: 0.5, want: 0.5, ok: true},
			{name: "json number", in: json.Number("0.7"), want: 0.7, ok: true},
			{name: "string", in: "0.9", want: 0.9, ok: true},
			{name: "bad string", in: "bad"},
			{name: "nil", in: nil},
		}
		for _, tt := range floatTests {
			t.Run(tt.name, func(t *testing.T) {
				got, ok := floatField(tt.in)
				if got != tt.want || ok != tt.ok {
					t.Fatalf("floatField() = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
				}
			})
		}
		if got := normalizeScore(-1); got != 0 {
			t.Fatalf("normalizeScore(-1) = %v", got)
		}
		if got := normalizeScore(2); got != 1.0/3.0 {
			t.Fatalf("normalizeScore(2) = %v", got)
		}
		if got := stringField(map[string]any{"n": json.Number("12"), "empty": " "}, "empty", "n"); got != "12" {
			t.Fatalf("stringField() = %q", got)
		}
		if got := toMem0Metadata(memory.MemoryItem{ID: "id", Source: "source", Metadata: map[string]string{"user_id": "ignored", "k": "v"}, CreatedAt: time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC)}); !reflect.DeepEqual(got["k"], "v") || got["user_id"] != nil || got["paxm_created_at"] == nil {
			t.Fatalf("toMem0Metadata() = %#v", got)
		}
	})
}
