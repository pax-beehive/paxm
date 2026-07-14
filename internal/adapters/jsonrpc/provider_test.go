package jsonrpc

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/contracttest"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestProviderAdapterContract(t *testing.T) {
	provider := newHelperProvider(t, "plugin")
	contracttest.Run(t, provider, contracttest.Expectation{
		Name: "plugin", Item: memory.MemoryItem{Text: "remember this"}, Query: memory.SearchQuery{Text: "paxm config", Limit: 3, Metadata: map[string]string{"workspace": "/tmp/project"}},
		RefID: "put-1", HitID: "hit-1", HitText: "YAML config is the paxm default",
	})
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]config.ProviderConfig{
		"missing command": {Transport: "stdio"},
		"bad transport":   {Transport: "http", Command: "plugin"},
		"bad timeout":     {Transport: "stdio", Command: "plugin", Timeout: "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New("plugin", tc); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestProviderSearchPutBatchAndHealth(t *testing.T) {
	t.Parallel()

	provider := newHelperProvider(t, "plugin")

	if err := provider.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{
		Text:     "paxm config",
		Limit:    3,
		Metadata: map[string]string{"workspace": "/tmp/project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected one hit, got %#v", hits)
	}
	if hits[0].Provider != "plugin" || hits[0].ID != "hit-1" || hits[0].Relevance != 0.9 {
		t.Fatalf("unexpected hit: %#v", hits[0])
	}
	if hits[0].Metadata["workspace"] != "/tmp/project" || hits[0].CreatedAt.IsZero() {
		t.Fatalf("hit metadata/time was not mapped: %#v", hits[0])
	}

	ref, err := provider.Put(context.Background(), memory.MemoryItem{Text: "remember this"})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Provider != "plugin" || ref.ID != "put-1" {
		t.Fatalf("unexpected put ref: %#v", ref)
	}

	refs, err := provider.PutBatch(context.Background(), []memory.MemoryItem{
		{Text: "first"},
		{Text: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Provider != "plugin" || refs[0].ID != "batch-1" || refs[1].ID != "batch-2" {
		t.Fatalf("unexpected batch refs: %#v", refs)
	}
}

func TestProviderMapsStructuredAttribution(t *testing.T) {
	provider := newHelperProvider(t, "put-attribution")
	item := memory.MemoryItem{
		Text:       "remember this",
		Provenance: memory.Provenance{UserID: "todd", AgentID: "codex", ScopeType: "team", ScopeID: "pax"},
		Turn:       &memory.TurnContext{SessionID: "session-7", TurnID: "turn-42"},
	}
	if _, err := provider.Put(context.Background(), item); err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "paxm config"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v", hits)
	}
	got := memory.ApplyHitAttribution(hits[0])
	if got.Origin != (memory.MemoryOrigin{UserID: "todd", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"}) {
		t.Fatalf("origin = %#v", got.Origin)
	}
	if got.Scope != (memory.MemoryScope{Type: "team", ID: "pax"}) {
		t.Fatalf("scope = %#v", got.Scope)
	}
}

func TestPutBatchFallsBackWhenPluginDoesNotSupportBatch(t *testing.T) {
	t.Parallel()

	provider := newHelperProvider(t, "nobatch")
	refs, err := provider.PutBatch(context.Background(), []memory.MemoryItem{
		{Text: "first"},
		{Text: "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].ID != "put-1" || refs[1].ID != "put-1" {
		t.Fatalf("unexpected fallback refs: %#v", refs)
	}
}

func TestProviderCapabilitiesAndDelete(t *testing.T) {
	provider := newHelperProvider(t, "lifecycle")
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.PutBatch || !capabilities.Delete {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if err := provider.Delete(context.Background(), memory.MemoryRef{Provider: "plugin", ID: "put-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCapabilitiesAreOptionalForLegacyPlugins(t *testing.T) {
	provider := newHelperProvider(t, "plugin")
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.PutBatch || capabilities.Delete {
		t.Fatalf("legacy capabilities = %#v", capabilities)
	}
}

func newHelperProvider(t *testing.T, mode string) *Provider {
	t.Helper()

	provider, err := New(mode, config.ProviderConfig{
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestJSONRPCPluginHelper", "--"},
		Env: map[string]string{
			"PAXM_JSONRPC_PLUGIN_HELPER": "1",
			"PAXM_JSONRPC_PLUGIN_MODE":   mode,
		},
		Timeout: "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestJSONRPCPluginHelper(t *testing.T) {
	if os.Getenv("PAXM_JSONRPC_PLUGIN_HELPER") != "1" {
		return
	}

	var request rpcRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      request.ID,
		Result:  json.RawMessage(`{}`),
	}
	switch request.Method {
	case methodHealth:
		response.Result = json.RawMessage(`{"ok":true}`)
	case methodSearch:
		var query memory.SearchQuery
		if err := remarshal(request.Params, &query); err != nil {
			t.Fatal(err)
		}
		createdAt := time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano)
		response.Result = mustRawJSON(searchResult{Hits: []memory.MemoryHit{{
			ID:        "hit-1",
			Text:      "YAML config is the paxm default",
			Relevance: 0.9,
			Score:     0.9,
			Metadata:  query.Metadata,
			CreatedAt: parseTimeForHelper(createdAt),
			Origin:    memory.MemoryOrigin{UserID: "todd", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"},
			Scope:     memory.MemoryScope{Type: "team", ID: "pax"},
		}}})
	case methodPut:
		if os.Getenv("PAXM_JSONRPC_PLUGIN_MODE") == "put-attribution" {
			var item memory.MemoryItem
			if err := remarshal(request.Params, &item); err != nil {
				t.Fatal(err)
			}
			if item.Origin.SessionID != "session-7" || item.Origin.TurnID != "turn-42" || item.Scope != (memory.MemoryScope{Type: "team", ID: "pax"}) {
				t.Fatalf("put attribution = origin %#v scope %#v", item.Origin, item.Scope)
			}
		}
		response.Result = mustRawJSON(refsResult{Ref: &memory.MemoryRef{ID: "put-1"}})
	case methodPutBatch:
		if os.Getenv("PAXM_JSONRPC_PLUGIN_MODE") == "nobatch" {
			response.Result = nil
			response.Error = &RPCError{Code: methodNotFound, Message: "method not found"}
			break
		}
		response.Result = mustRawJSON(refsResult{Refs: []memory.MemoryRef{{ID: "batch-1"}, {ID: "batch-2"}}})
	case methodCapabilities:
		if os.Getenv("PAXM_JSONRPC_PLUGIN_MODE") != "lifecycle" {
			response.Result = nil
			response.Error = &RPCError{Code: methodNotFound, Message: "method not found"}
			break
		}
		response.Result = mustRawJSON(Capabilities{PutBatch: true, Delete: true})
	case methodDelete:
		if os.Getenv("PAXM_JSONRPC_PLUGIN_MODE") != "lifecycle" {
			response.Result = nil
			response.Error = &RPCError{Code: methodNotFound, Message: "method not found"}
			break
		}
		response.Result = json.RawMessage(`{"deleted":true}`)
	default:
		response.Result = nil
		response.Error = &RPCError{Code: methodNotFound, Message: "method not found"}
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func remarshal(value any, out any) error {
	bytes, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, out)
}

func mustRawJSON(value any) json.RawMessage {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return bytes
}

func parseTimeForHelper(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
