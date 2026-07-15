package mem0cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestCloudMetadataIncludesStructuredAttribution(t *testing.T) {
	metadata := itemMetadata(memory.MemoryItem{
		Text:   "remember",
		Origin: memory.MemoryOrigin{UserID: "todd", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"},
		Scope:  memory.MemoryScope{Type: "team", ID: "pax"},
	})
	for key, want := range map[string]string{
		memory.MetadataUserID: "todd", memory.MetadataAgentID: "codex", memory.MetadataSessionID: "session-7",
		memory.MetadataTurnID: "turn-42", memory.MetadataScopeType: "team", memory.MetadataScopeID: "pax",
	} {
		if metadata[key] != want {
			t.Fatalf("metadata[%q] = %#v, want %q", key, metadata[key], want)
		}
	}
}

func TestCloudLifecycle(t *testing.T) {
	t.Parallel()
	var add addRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token cloud-key" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/memories/add/":
			if err := json.NewDecoder(r.Body).Decode(&add); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"status":"PENDING","event_id":"evt-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/event/evt-1/":
			_, _ = w.Write([]byte(`{"status":"SUCCEEDED"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v3/memories/":
			var listed listRequest
			if err := json.NewDecoder(r.Body).Decode(&listed); err != nil {
				t.Fatal(err)
			}
			if listed.Filters["run_id"] != "eval-1" {
				t.Fatalf("list filters = %#v", listed.Filters)
			}
			if r.URL.Query().Get("page_size") == "100" {
				metadata, ok := listed.Filters["metadata"].(map[string]any)
				if !ok || metadata["paxm_write_id"] != "write-1" {
					t.Fatalf("list metadata filters = %#v", listed.Filters)
				}
			}
			_, _ = w.Write([]byte(`{"count":1,"results":[{"id":"mem-1","memory":"prefers explicit search","metadata":{"paxm_write_id":"write-1"}}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v3/memories/search/":
			_, _ = w.Write([]byte(`{"results":[{"id":"mem-1","memory":"prefers explicit search","score":0.91,"metadata":{"project":"paxm","paxm_user_id":"todd","paxm_agent_id":"codex","paxm_session_id":"session-7","paxm_turn_id":"turn-42","paxm_scope_type":"team","paxm_scope_id":"pax"}}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/memories/mem-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	provider, err := newWithClient("cloud", config.ProviderConfig{
		BaseURL: server.URL, APIKey: "cloud-key", RunID: "eval-1",
	}, server.Client(), func() string { return "write-1" })
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, provider, contracttest.Expectation{Name: "cloud", Item: memory.MemoryItem{ID: "local-1", Text: "prefers explicit search"}, Query: memory.SearchQuery{Text: "search", Limit: 5}, RefID: "mem-1", HitID: "mem-1", HitText: "prefers explicit search"})
	if add.RunID != "eval-1" || add.Metadata["paxm_write_id"] != "write-1" {
		t.Fatalf("add request = %#v", add)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "search", Limit: 1})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search hits = %#v err=%v", hits, err)
	}
	if hits[0].Origin != (memory.MemoryOrigin{UserID: "todd", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"}) || hits[0].Scope != (memory.MemoryScope{Type: "team", ID: "pax"}) {
		t.Fatalf("attribution was not restored: %#v", hits[0])
	}
	if hits[0].RawScore == nil || *hits[0].RawScore != 0.91 || hits[0].RawScoreKind != "mem0_cloud_similarity" {
		t.Fatalf("score semantics were not preserved: %#v", hits[0])
	}
	if err := provider.Delete(context.Background(), memory.MemoryRef{Provider: "cloud", ID: "mem-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudCleanupEvalScope(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/memories/" || r.URL.Query().Get("run_id") != "eval-run" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	provider, err := newWithClient("cloud", config.ProviderConfig{BaseURL: server.URL, APIKey: "key", RunID: "eval-run"}, server.Client(), func() string { return "write" })
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.CleanupEvalScope(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloudConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []config.ProviderConfig{
		{BaseURL: "https://api.mem0.ai", UserID: "user"},
		{BaseURL: "not-a-url", APIKey: "key", UserID: "user"},
		{BaseURL: "https://api.mem0.ai", APIKey: "key"},
		{BaseURL: "https://api.mem0.ai", APIKey: "key", UserID: "user", ScoreSemantics: "cosine"},
	}
	for _, cfg := range tests {
		if _, err := newWithClient("cloud", cfg, http.DefaultClient, func() string { return "id" }); err == nil {
			t.Fatalf("expected validation error for %#v", cfg)
		}
	}
}

func TestCloudScoreSemanticsDefaultsAndExplicitDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.ProviderConfig
		want config.ScoreSemantics
	}{
		{name: "backward compatible default", cfg: config.ProviderConfig{BaseURL: "https://api.mem0.ai", APIKey: "key", UserID: "user"}, want: config.ScoreSemanticsSimilarity},
		{name: "explicit distance", cfg: config.ProviderConfig{BaseURL: "https://api.mem0.ai", APIKey: "key", UserID: "user", ScoreSemantics: "distance"}, want: config.ScoreSemanticsDistance},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := newWithClient("cloud", tt.cfg, http.DefaultClient, func() string { return "id" })
			if err != nil {
				t.Fatal(err)
			}
			if provider.scoreSemantics != tt.want {
				t.Fatalf("score semantics = %q, want %q", provider.scoreSemantics, tt.want)
			}
		})
	}
}

func TestCloudDistanceSemanticsMapsLowDistanceToHigherRelevance(t *testing.T) {
	t.Parallel()

	client := cloudHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"id":"low","memory":"regulatory scope","score":0.479},{"id":"high","memory":"latest progress","score":0.840}]}`)),
			Request:    request,
		}, nil
	})
	provider, err := newWithClient("cloud", config.ProviderConfig{BaseURL: "https://mem0.test", APIKey: "key", UserID: "user", ScoreSemantics: "distance"}, client, func() string { return "id" })
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "scope", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Relevance <= hits[1].Relevance {
		t.Fatalf("low distance did not rank higher: %#v", hits)
	}
	if hits[0].RawScoreKind != "mem0_cloud_distance" || hits[1].RawScoreKind != "mem0_cloud_distance" {
		t.Fatalf("raw score kinds = %#v", hits)
	}
}

type cloudHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f cloudHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCloudEventFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/memories/add/":
			_, _ = w.Write([]byte(`{"status":"PENDING","event_id":"evt-1"}`))
		case "/v1/event/evt-1/":
			_, _ = w.Write([]byte(`{"status":"FAILED","message":"extraction failed"}`))
		}
	}))
	defer server.Close()
	provider, err := newWithClient("cloud", config.ProviderConfig{BaseURL: server.URL, APIKey: "key", UserID: "user"}, server.Client(), func() string { return "write" })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Put(context.Background(), memory.MemoryItem{Text: "hello"}); err == nil {
		t.Fatal("expected event failure")
	}
}

func TestCloudPutRetriesWriteLookupAfterEventSuccess(t *testing.T) {
	t.Parallel()
	lookups := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/memories/add/":
			_, _ = w.Write([]byte(`{"status":"PENDING","event_id":"evt-1"}`))
		case "/v1/event/evt-1/":
			_, _ = w.Write([]byte(`{"status":"SUCCEEDED"}`))
		case "/v3/memories/":
			lookups++
			if lookups < 3 {
				_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"count":1,"results":[{"id":"mem-1","memory":"hello","metadata":{"paxm_write_id":"write"}}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	provider, err := newWithClient("cloud", config.ProviderConfig{BaseURL: server.URL, APIKey: "key", UserID: "user"}, server.Client(), func() string { return "write" })
	if err != nil {
		t.Fatal(err)
	}
	provider.lookupDelay = func(int) time.Duration { return 0 }
	ref, err := provider.Put(context.Background(), memory.MemoryItem{Text: "hello"})
	if err != nil || ref.ID != "mem-1" || lookups != 3 {
		t.Fatalf("ref = %#v, lookups = %d, err = %v", ref, lookups, err)
	}
}
