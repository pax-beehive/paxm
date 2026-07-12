package mem0cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

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
			_, _ = w.Write([]byte(`{"results":[{"id":"mem-1","memory":"prefers explicit search","score":0.91,"metadata":{"project":"paxm"}}]}`))
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
	}
	for _, cfg := range tests {
		if _, err := newWithClient("cloud", cfg, http.DefaultClient, func() string { return "id" }); err == nil {
			t.Fatalf("expected validation error for %#v", cfg)
		}
	}
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
