package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

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

type providerStub struct{ item memory.MemoryItem }

func (*providerStub) Name() string { return "sqlite" }
func (p *providerStub) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.item = item
	return memory.MemoryRef{Provider: "sqlite", ID: "one"}, nil
}
func (p *providerStub) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return []memory.MemoryHit{{Provider: "sqlite", ID: "one", Text: p.item.Text, Relevance: 1, Score: 1}}, nil
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
	if _, err := agent.Remember(context.Background(), RememberInput{Text: "operator and tools are separate"}); err != nil {
		t.Fatal(err)
	}
	result, err := agent.Recall(context.Background(), RecallInput{Query: "operator tools"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Text != "operator and tools are separate" {
		t.Fatalf("result=%#v", result)
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
