package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestRememberInputDoesNotExposeInternalTurnContext(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(RememberInput{
		Text: "internal boundary",
		Turn: &memory.TurnContext{SessionID: "session", TurnID: "turn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("paxm_turn")) || bytes.Contains(data, []byte("session")) {
		t.Fatalf("internal turn context leaked into agent-facing JSON: %s", data)
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
	query memory.SearchQuery
}

func (*providerStub) Name() string { return "sqlite" }
func (p *providerStub) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.item = item
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
		Text: "team decision", Profile: "ltm", AgentName: "codex",
		Metadata: map[string]string{memory.MetadataUserID: "mallory", memory.MetadataScopeID: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := memory.Provenance{UserID: "todd", AgentID: "codex-todd", ScopeType: "team", ScopeID: "pax"}
	if provider.item.Provenance != want || memory.ProvenanceFromMetadata(provider.item.Metadata) != want {
		t.Fatalf("provenance = %#v metadata=%#v", provider.item.Provenance, provider.item.Metadata)
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
