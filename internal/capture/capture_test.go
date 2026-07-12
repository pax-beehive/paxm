package capture

import (
	"context"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type captureProviderStub struct{}

func (captureProviderStub) Name() string { return "sqlite" }
func (captureProviderStub) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	return memory.MemoryRef{Provider: "sqlite", ID: "capture-ref"}, nil
}
func (captureProviderStub) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return []memory.MemoryHit{{Provider: "sqlite", ID: "capture-hit", Text: "captured", Relevance: 1, Score: 1}}, nil
}
func (captureProviderStub) Health(context.Context) error { return nil }

func TestServiceAdaptsIndependentCaptureEvents(t *testing.T) {
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: captureProviderStub{}, Read: true, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("config.yaml")
	agent := cfg.Agents["codex"]
	agent.Enabled = true
	hook := agent.Hooks["user_input"]
	hook.Recall.Enabled = true
	hook.Write.Enabled = true
	agent.Hooks["user_input"] = hook
	cfg.Agents["codex"] = agent

	service := New(facade.New(cfg, router))
	event := Event{Target: "codex", Event: "user_input", Query: "captured", Prompt: "remember this", Messages: []Message{{Role: "user", Text: "captured"}}}
	_ = service.BufferConfig(event)
	item, ok, err := service.WriteItem(event)
	if err != nil || !ok || item.Text == "" {
		t.Fatalf("WriteItem() = %#v, ok=%v, err=%v", item, ok, err)
	}
	result, err := service.Recall(context.Background(), event)
	if err != nil || result.Recall == nil || len(result.Recall.Hits) != 1 {
		t.Fatalf("Recall() = %#v, err=%v", result, err)
	}
}
