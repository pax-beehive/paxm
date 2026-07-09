package facade

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type captureProvider struct {
	query string
	hits  []memory.MemoryHit
}

func (p *captureProvider) Name() string {
	return "capture"
}

func (p *captureProvider) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	p.query = query.Text
	if p.hits != nil {
		return p.hits, nil
	}
	return []memory.MemoryHit{{ID: "1", Text: "hit", Score: 1}}, nil
}

func (p *captureProvider) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	return memory.MemoryRef{Provider: "capture", ID: "1"}, nil
}

func (p *captureProvider) Health(context.Context) error {
	return nil
}

func TestRunHookUsesExplicitQueryBeforeTemplate(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers:  []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				MaxResults: 8,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
				},
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Recall: config.HookRecallConfig{
							Enabled:       true,
							Profile:       "default",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    8,
						},
					},
				},
			},
		},
	}, router)

	_, err = service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Query:  "explicit query",
		Prompt: "prompt query",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query != "explicit query" {
		t.Fatalf("expected explicit query, got %q", provider.query)
	}
}

func TestRecallUsesAgentActiveRecallProfile(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"active": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "active",
				},
			},
		},
	}, router)

	_, err = service.Recall(context.Background(), RecallInput{Query: "active query"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query != "active query" {
		t.Fatalf("active recall did not hit provider, got query %q", provider.query)
	}
}

func TestRunHookAppliesInsertionPolicy(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "low-score", Text: "paxm passive recall low score", Score: 0.7, Relevance: 0.7},
			{ID: "no-term", Text: "unrelated high score memory", Score: 0.95, Relevance: 0.95},
			{ID: "keep", Text: "paxm passive recall should be conservative", Score: 0.9, Relevance: 0.9},
			{ID: "over-limit", Text: "another paxm passive recall memory", Score: 0.85, Relevance: 0.85},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"passive": {
				Providers:  []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				MaxResults: 8,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Recall: config.HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    8,
							Insertion: config.HookInsertionConfig{
								MinScore:          0.8,
								MaxItems:          1,
								RequireQueryTerms: true,
							},
						},
					},
				},
			},
		},
	}, router)

	result, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "paxm passive recall",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recall == nil {
		t.Fatal("expected recall result")
	}
	if len(result.Recall.Hits) != 1 || result.Recall.Hits[0].ID != "keep" {
		t.Fatalf("unexpected inserted hits: %#v", result.Recall.Hits)
	}
}

func TestHookWriteItemRendersTemplateAndMetadata(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		WriteProfiles: map[string]config.WriteProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{
							Enabled:  true,
							Profile:  "default",
							Template: "User input: {{ .prompt }} / {{ .raw_json }}",
							Mode:     "user_input",
							Buffer: config.HookBufferConfig{
								Enabled: true,
							},
						},
					},
				},
			},
		},
	}, router)

	item, ok, err := service.HookWriteItem(HookEvent{
		Target:    "codex",
		Event:     "user_input",
		Prompt:    "remember this",
		Workspace: "/tmp/project",
		Metadata:  map[string]string{"project": "paxm"},
		Raw:       json.RawMessage(`{"prompt":"remember this"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected hook write item")
	}
	if item.Text != `User input: remember this / {"prompt":"remember this"}` {
		t.Fatalf("unexpected hook write text: %q", item.Text)
	}
	if item.Source != "hook:codex:user_input" || item.Profile != "default" {
		t.Fatalf("unexpected hook write routing: %#v", item)
	}
	if item.Metadata["hook_event"] != "user_input" || item.Metadata["workspace"] != "/tmp/project" || item.Metadata["project"] != "paxm" {
		t.Fatalf("unexpected hook metadata: %#v", item.Metadata)
	}
}
