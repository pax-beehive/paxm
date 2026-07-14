package openviking

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestPutStoresMemoryThroughCommittedSession(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		if request.Header.Get("X-API-Key") != "secret" {
			t.Fatalf("X-API-Key = %q, want secret", request.Header.Get("X-API-Key"))
		}
		switch request.URL.Path {
		case "/api/v1/sessions":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"session_id":"session-1"}}`))
		case "/api/v1/sessions/session-1/messages":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["role"] != "user" || body["content"] != "Todd prefers self-hosted memory" {
				t.Fatalf("message body = %#v", body)
			}
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"session_id":"session-1","message_count":1}}`))
		case "/api/v1/sessions/session-1/commit":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"status":"accepted","task_id":"task-1"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := New("openviking", config.ProviderConfig{BaseURL: server.URL, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := provider.Put(context.Background(), memory.MemoryItem{Text: "Todd prefers self-hosted memory"})
	if err != nil {
		t.Fatal(err)
	}
	if ref != (memory.MemoryRef{Provider: "openviking", ID: "task-1"}) {
		t.Fatalf("ref = %#v", ref)
	}
	wantPaths := []string{
		"POST /api/v1/sessions",
		"POST /api/v1/sessions/session-1/messages",
		"POST /api/v1/sessions/session-1/commit",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestSearchMapsOpenVikingMemories(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/search/find" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["query"] != "memory backend" || body["context_type"] != "memory" || body["limit"] != float64(3) || body["level"] != float64(2) {
			t.Fatalf("search body = %#v", body)
		}
		_, _ = writer.Write([]byte(`{
			"status":"ok",
			"result":{
				"memories":[{
					"context_type":"memory",
					"uri":"viking://user/todd/memories/preferences/backend.md",
					"level":2,
					"score":0.87,
					"category":"preferences",
					"match_reason":"semantic match",
					"abstract":"Todd prefers a self-hosted memory backend.",
					"overview":"Backend infrastructure preference"
				}],
				"resources":[],
				"skills":[],
				"total":1
			}
		}`))
	}))
	defer server.Close()

	provider, err := New("openviking", config.ProviderConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "memory backend", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v", hits)
	}
	hit := hits[0]
	if hit.Provider != "openviking" || hit.ID != "viking://user/todd/memories/preferences/backend.md" || hit.Text != "Todd prefers a self-hosted memory backend." {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.Relevance != 0.87 || hit.Score != 0.87 || hit.RawScore == nil || *hit.RawScore != 0.87 || hit.RawScoreKind != "openviking_score" {
		t.Fatalf("score mapping = %#v", hit)
	}
	if hit.Source != "openviking" || hit.Metadata["openviking_category"] != "preferences" || hit.Metadata["openviking_overview"] != "Backend infrastructure preference" {
		t.Fatalf("metadata = %#v", hit.Metadata)
	}
	if hit.Scope.Type != "unknown" || hit.Origin != (memory.MemoryOrigin{}) {
		t.Fatalf("OpenViking must not synthesize attribution: %#v", hit)
	}
}

func TestSearchClampsNativeScoreForRouter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"ok","result":{"memories":[{"uri":"viking://user/memories/events/high.md","level":2,"score":1.4,"abstract":"high native score"}]}}`))
	}))
	defer server.Close()

	provider, err := New("openviking", config.ProviderConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Relevance != 1 || hits[0].Score != 1 {
		t.Fatalf("normalized hit = %#v", hits)
	}
	if hits[0].RawScore == nil || *hits[0].RawScore != 1.4 {
		t.Fatalf("raw score = %#v", hits[0].RawScore)
	}
}

func TestHealthChecksSelfHostedServer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/stats/memories" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"status":"ok","result":{"total":12}}`))
	}))
	defer server.Close()

	provider, err := New("openviking", config.ProviderConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/stats/memories":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"total":1}}`))
		case "/api/v1/sessions":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"session_id":"session-1"}}`))
		case "/api/v1/sessions/session-1/messages":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"session_id":"session-1"}}`))
		case "/api/v1/sessions/session-1/commit":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"status":"accepted","task_id":"task-1"}}`))
		case "/api/v1/search/find":
			_, _ = writer.Write([]byte(`{"status":"ok","result":{"memories":[{"uri":"viking://user/memories/events/contract.md","level":2,"score":0.8,"abstract":"contract memory"}]}}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := New("openviking-test", config.ProviderConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Run(t, provider, contracttest.Expectation{
		Name: "openviking-test", Item: memory.MemoryItem{Text: "contract memory"}, Query: memory.SearchQuery{Text: "contract", Limit: 3},
		RefID: "task-1", HitID: "viking://user/memories/events/contract.md", HitText: "contract memory",
	})
}

func TestProviderValidationAndAPIErrors(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		providerName string
		cfg          config.ProviderConfig
		want         string
	}{
		"name":     {cfg: config.ProviderConfig{BaseURL: "http://localhost:1933"}, want: "name"},
		"base URL": {providerName: "openviking", cfg: config.ProviderConfig{BaseURL: "localhost:1933"}, want: "base_url"},
		"timeout":  {providerName: "openviking", cfg: config.ProviderConfig{BaseURL: "http://localhost:1933", Timeout: "later"}, want: "timeout"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(test.providerName, test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"status":"error","error":{"code":"UNAVAILABLE","message":"vector index offline"}}`))
	}))
	defer server.Close()
	provider, err := New("openviking", config.ProviderConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "query"}); err == nil || !strings.Contains(err.Error(), "vector index offline") {
		t.Fatalf("search error = %v", err)
	}
}
