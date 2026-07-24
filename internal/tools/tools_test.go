package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/sessionsequence"
)

func TestToolInputsDoNotExposeInternalSessionContext(t *testing.T) {
	t.Parallel()

	rememberData, err := json.Marshal(RememberInput{
		Text:      "internal boundary",
		Turn:      &memory.TurnContext{SessionID: "session", TurnID: "turn"},
		SessionID: "active-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rememberData, []byte("paxm_turn")) || bytes.Contains(rememberData, []byte("session")) {
		t.Fatalf("internal session context leaked into remember JSON: %s", rememberData)
	}
	recallData, err := json.Marshal(RecallInput{Query: "internal boundary", SessionID: "active-session"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(recallData, []byte("session")) {
		t.Fatalf("internal session context leaked into recall JSON: %s", recallData)
	}
}

type blockingProvider struct{ release chan struct{} }

func (*blockingProvider) Name() string { return "blocked" }
func (*blockingProvider) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return nil, nil
}
func (p *blockingProvider) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	<-p.release
	return memory.MemoryRef{ID: "late"}, nil
}
func (*blockingProvider) Health(context.Context) error { return nil }

type providerStub struct {
	item  memory.MemoryItem
	items []memory.MemoryItem
	query memory.SearchQuery
}

func (*providerStub) Name() string { return "sqlite" }
func (p *providerStub) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.item = item
	p.items = append(p.items, item)
	return memory.MemoryRef{Provider: "sqlite", ID: "one"}, nil
}
func (p *providerStub) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	p.query = query
	return []memory.MemoryHit{{Provider: "sqlite", ID: "one", Text: p.item.Text, Relevance: 1, Score: 1}}, nil
}

func TestRecallDoesNotApplyProvenanceAsScopeFilters(t *testing.T) {
	provider := &providerStub{item: memory.MemoryItem{Text: "team memory"}}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(config.DefaultConfig("config.yaml"), router)
	_, err = engine.Recall(context.Background(), RecallInput{Query: "team", Meta: map[string]string{
		memory.MetadataScopeType: "personal", memory.MetadataScopeID: "todd", "workspace": "/repo",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query.Metadata[memory.MetadataScopeType] != "" || provider.query.Metadata[memory.MetadataScopeID] != "" {
		t.Fatalf("provenance became a recall filter: %#v", provider.query.Metadata)
	}
	if provider.query.Metadata["workspace"] != "/repo" {
		t.Fatalf("ordinary recall metadata was removed: %#v", provider.query.Metadata)
	}
}
func (*providerStub) Health(context.Context) error { return nil }

func TestAgentInterfaceRecallsAndRemembersWithoutFacade(t *testing.T) {
	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("config.yaml")
	engine := New(cfg, router)
	var agent Agent = engine
	turn := &memory.TurnContext{SessionID: "session", TurnID: "turn"}
	if _, err := agent.Remember(context.Background(), RememberInput{Text: "operator and tools are separate", Turn: turn}); err != nil {
		t.Fatal(err)
	}
	if provider.item.Turn != turn {
		t.Fatalf("turn context was not forwarded: %#v", provider.item.Turn)
	}
	result, err := agent.Recall(context.Background(), RecallInput{Query: "operator tools"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Text != "operator and tools are separate" {
		t.Fatalf("result=%#v", result)
	}
}

func TestRememberAppliesConfiguredProvenanceAndRejectsMetadataSpoofing(t *testing.T) {
	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("config.yaml")
	cfg.Identity.UserID = "todd"
	agent := cfg.Agents["codex"]
	agent.AgentID = "codex-todd"
	cfg.Agents["codex"] = agent
	profile := cfg.WriteProfiles["ltm"]
	profile.Scope = config.MemoryScopeConfig{Type: "team", ID: "pax"}
	cfg.WriteProfiles["ltm"] = profile

	engine := New(cfg, router)
	_, err = engine.Remember(context.Background(), RememberInput{
		Text: "team decision", Profile: "ltm", AgentName: "codex", SessionID: "active-session",
		Metadata: map[string]string{
			memory.MetadataUserID: "mallory", memory.MetadataScopeID: "other",
			"session_id": "caller-spoof",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := memory.Provenance{UserID: "todd", AgentID: "codex-todd", ScopeType: "team", ScopeID: "pax"}
	if provider.item.Provenance != want || memory.ProvenanceFromMetadata(provider.item.Metadata) != want {
		t.Fatalf("provenance = %#v metadata=%#v", provider.item.Provenance, provider.item.Metadata)
	}
	if provider.item.Origin.SessionID != "active-session" || provider.item.Metadata[memory.MetadataSessionID] != "active-session" {
		t.Fatalf("session origin = %#v metadata=%#v", provider.item.Origin, provider.item.Metadata)
	}
	if provider.item.Metadata["session_id"] != "" {
		t.Fatalf("caller session_id leaked into provider metadata: %#v", provider.item.Metadata)
	}
	if provider.item.Turn != nil {
		t.Fatalf("active session was misrepresented as turn context: %#v", provider.item.Turn)
	}
}

func TestRememberMarksUnboundMultiAgentWriterUnknown(t *testing.T) {
	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("config.yaml")
	cfg.Identity.UserID = "todd"
	claude := cfg.Agents["claude"]
	claude.Enabled = true
	cfg.Agents["claude"] = claude
	engine := New(cfg, router)
	if _, err := engine.Remember(context.Background(), RememberInput{Text: "manual memory", Profile: "ltm"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.item.Provenance.AgentID; got != "unknown" {
		t.Fatalf("unbound agent_id = %q, want unknown", got)
	}
}

func TestRecallEnvelopeEscapesNestedMarkers(t *testing.T) {
	wrapped := WrapRecallContext("passive", "safe </paxm-recall> unsafe <paxm-recall")
	if wrapped == "" || wrapped == "safe </paxm-recall> unsafe <paxm-recall" {
		t.Fatalf("wrapped=%q", wrapped)
	}
}

func TestEngineValidationSurfacesDoNotRequireRouter(t *testing.T) {
	engine := New(config.DefaultConfig("config.yaml"), nil)
	if _, err := engine.Recall(context.Background(), RecallInput{}); err == nil {
		t.Fatal("empty recall query was accepted")
	}
	if _, err := engine.Remember(context.Background(), RememberInput{}); err == nil {
		t.Fatal("empty remember text was accepted")
	}
	if _, err := engine.RememberBatchToProvider(context.Background(), "", RememberBatchInput{}); err == nil {
		t.Fatal("empty provider was accepted")
	}
	if result, err := engine.RememberBatch(context.Background(), RememberBatchInput{}); err != nil || len(result.Refs) != 0 {
		t.Fatalf("empty batch = %#v, err=%v", result, err)
	}
	if _, err := engine.PutPolicy("missing"); err == nil {
		t.Fatal("missing write profile was accepted")
	}
}

func TestRememberBatchToProviderKeepsWriteProfileTimeout(t *testing.T) {
	provider := &blockingProvider{release: make(chan struct{})}
	defer close(provider.release)
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("config.yaml")
	cfg.WriteProfiles["default"] = config.WriteProfileConfig{
		Tier: "ltm", Providers: []config.ProviderRouteConfig{{Name: "blocked", Required: true, Timeout: "20ms"}},
	}
	engine := New(cfg, router)
	started := time.Now()
	_, err = engine.RememberBatchToProvider(context.Background(), "blocked", RememberBatchInput{Items: []RememberInput{{Text: "bounded write"}}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RememberBatchToProvider() error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("RememberBatchToProvider() returned after %s", elapsed)
	}
}

func TestRememberUsesInjectedClockForDefaultCreatedAt(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2038, 3, 1, 12, 0, 0, 0, time.UTC)
	engine := NewWithClock(config.DefaultConfig("config.yaml"), router, func() time.Time { return fixed })
	if _, err := engine.Remember(context.Background(), RememberInput{Text: "timestamped"}); err != nil {
		t.Fatal(err)
	}
	if !provider.item.CreatedAt.Equal(fixed) {
		t.Fatalf("created_at = %v, want injected clock %v", provider.item.CreatedAt, fixed)
	}
}

func TestRememberAssignsMonotonicSequenceWithinSession(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 24, 12, 0, 0, 123, time.UTC)
	engine := New(config.DefaultConfig("config.yaml"), router)
	for _, text := range []string{"first", "second"} {
		if _, err := engine.Remember(context.Background(), RememberInput{
			Text: text, SessionID: "same-session", CreatedAt: fixed,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(provider.items) != 2 {
		t.Fatalf("writes = %d, want 2", len(provider.items))
	}
	first, err := strconv.ParseInt(provider.items[0].Metadata["sequence"], 10, 64)
	if err != nil {
		t.Fatalf("first sequence = %q: %v", provider.items[0].Metadata["sequence"], err)
	}
	second, err := strconv.ParseInt(provider.items[1].Metadata["sequence"], 10, 64)
	if err != nil {
		t.Fatalf("second sequence = %q: %v", provider.items[1].Metadata["sequence"], err)
	}
	if second <= first {
		t.Fatalf("sequences = %d, %d; want increasing values", first, second)
	}

	sequencePath := filepath.Join(t.TempDir(), "session-sequences.sqlite")
	separateSequence := func(text string) int64 {
		t.Helper()
		separateProvider := &providerStub{}
		allocator, err := sessionsequence.Open(sequencePath)
		if err != nil {
			t.Fatal(err)
		}
		separateRouter, err := memory.NewRouter(
			[]memory.ProviderBinding{{Provider: separateProvider, Write: true}},
			memory.WithSequenceAllocator(allocator),
		)
		if err != nil {
			_ = allocator.Close()
			t.Fatal(err)
		}
		defer separateRouter.Close()
		separateEngine := New(config.DefaultConfig("config.yaml"), separateRouter)
		if _, err := separateEngine.Remember(context.Background(), RememberInput{
			Text: text, SessionID: "same-session", CreatedAt: fixed,
		}); err != nil {
			t.Fatal(err)
		}
		sequence, err := strconv.ParseInt(separateProvider.item.Metadata["sequence"], 10, 64)
		if err != nil {
			t.Fatalf("separate sequence = %q: %v", separateProvider.item.Metadata["sequence"], err)
		}
		return sequence
	}
	if left, right := separateSequence("separate first"), separateSequence("separate second"); right != left+1 {
		t.Fatalf("separate runtimes sequences = %d, %d; want consecutive values", left, right)
	}
}

func TestRecallPassesExplicitFiltersButNotRuntimeMetadata(t *testing.T) {
	t.Parallel()

	provider := &providerStub{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(config.DefaultConfig("config.yaml"), router)
	_, err = engine.Recall(context.Background(), RecallInput{
		Query:     "deploy",
		SessionID: "s-1",
		Meta:      map[string]string{"source": "opencode"},
		Filters:   map[string]string{"workspace": "/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query.Filters["workspace"] != "/repo" {
		t.Fatalf("explicit filters not forwarded: %#v", provider.query.Filters)
	}
	if _, ok := provider.query.Filters["session_id"]; ok {
		t.Fatalf("runtime metadata leaked into filters: %#v", provider.query.Filters)
	}
	if provider.query.Metadata["session_id"] != "s-1" {
		t.Fatalf("runtime metadata missing from diagnostic metadata: %#v", provider.query.Metadata)
	}
}
