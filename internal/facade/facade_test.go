package facade

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type captureProvider struct {
	query string
	hits  []memory.MemoryHit
	items []memory.MemoryItem
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

func (p *captureProvider) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.items = append(p.items, item)
	return memory.MemoryRef{Provider: "capture", ID: "1"}, nil
}

func (p *captureProvider) Health(context.Context) error {
	return nil
}

func TestIngestBatchToProviderPreservesHistoricalIdentityAndTime(t *testing.T) {
	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{Version: 1}, router)
	createdAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	_, err = service.IngestBatchToProvider(context.Background(), "capture", IngestBatchInput{Items: []IngestInput{{
		ID:        "historical-turn",
		Text:      "User: hello\nAssistant: hi",
		Source:    "backfill:codex",
		CreatedAt: createdAt,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 1 || provider.items[0].ID != "historical-turn" || !provider.items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("historical item was not preserved: %#v", provider.items)
	}
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

func TestRecallUsesProviderRouteThresholdOverride(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "provider-override", Text: "provider-specific threshold", Relevance: 0.4, Score: 0.4},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{
					{
						Name:     "capture",
						Required: true,
						Weight:   1,
						Thresholds: &config.RecallThresholdConfig{
							MinRelevance: 0.3,
							MinScore:     0.3,
						},
					},
				},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.8,
					MinScore:     0.8,
				},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
				},
			},
		},
	}, router)

	result, err := service.Recall(context.Background(), RecallInput{Query: "threshold"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "provider-override" {
		t.Fatalf("provider threshold override was not applied: %#v", result.Hits)
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

func TestRunHookUsesInitialRecallOverride(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "warmup", Text: "project bootstrap memory", Score: 0.4, Relevance: 0.4},
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
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.8,
					MinScore:     0.8,
				},
			},
			"passive_initial": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.3,
					MinScore:     0.3,
				},
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
							MaxResults:    2,
							Insertion: config.HookInsertionConfig{
								MinScore: 0.8,
							},
							Initial: &config.HookInitialRecall{
								Enabled:    true,
								Profile:    "passive_initial",
								MaxResults: 5,
								Insertion: config.HookInsertionConfig{
									MinScore: 0.3,
									MaxItems: 5,
								},
							},
						},
					},
				},
			},
		},
	}, router)

	strict, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "project bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strict.Recall == nil || len(strict.Recall.Hits) != 0 {
		t.Fatalf("strict user_input should filter low-confidence hits: %#v", strict.Recall)
	}

	initial, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "project bootstrap",
		Metadata: map[string]string{
			HookRecallPhaseMetadataKey: HookRecallPhaseInitial,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Recall == nil || len(initial.Recall.Hits) != 1 || initial.Recall.Hits[0].ID != "warmup" {
		t.Fatalf("initial user_input should use loose policy: %#v", initial.Recall)
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

func TestServiceIngestTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Config
		input     IngestInput
		wantErr   string
		wantItems []string
	}{
		{
			name:    "blank text is rejected",
			cfg:     writeConfigForProfiles("default"),
			input:   IngestInput{Text: " \n\t "},
			wantErr: "ingest text is required",
		},
		{
			name:    "missing write profile is rejected",
			cfg:     writeConfigForProfiles("default"),
			input:   IngestInput{Text: "memory", Profile: "missing"},
			wantErr: "write profile missing is not configured",
		},
		{
			name:      "default profile trims and writes",
			cfg:       writeConfigForProfiles("default"),
			input:     IngestInput{Text: "  keep this  ", Source: "cli", Metadata: map[string]string{"k": "v"}},
			wantItems: []string{"keep this"},
		},
		{
			name:      "explicit profile routes writes",
			cfg:       writeConfigForProfiles("archive"),
			input:     IngestInput{Text: "archive this", Profile: "archive"},
			wantItems: []string{"archive this"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &captureProvider{}
			router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
			if err != nil {
				t.Fatal(err)
			}
			service := New(tt.cfg, router)
			result, err := service.Ingest(context.Background(), tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Ingest() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ingest() error = %v", err)
			}
			if len(result.Refs) != len(tt.wantItems) {
				t.Fatalf("refs = %#v, want %d", result.Refs, len(tt.wantItems))
			}
			gotItems := itemTexts(provider.items)
			if !reflect.DeepEqual(gotItems, tt.wantItems) {
				t.Fatalf("items = %#v, want %#v", gotItems, tt.wantItems)
			}
		})
	}
}

func TestServiceIngestBatchTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Config
		input     IngestBatchInput
		wantErr   string
		wantItems []string
		wantRefs  int
	}{
		{
			name: "empty batch is a no-op",
			cfg:  writeConfigForProfiles("default"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: " "},
			}},
			wantItems: []string{},
		},
		{
			name: "groups default and explicit profiles",
			cfg:  writeConfigForProfiles("default", "archive"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: " default one "},
				{Text: "archive one", Profile: "archive"},
				{Text: ""},
			}},
			wantItems: []string{"archive one", "default one"},
			wantRefs:  2,
		},
		{
			name: "continues valid groups when another profile is missing",
			cfg:  writeConfigForProfiles("default"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: "keep"},
				{Text: "missing", Profile: "missing"},
			}},
			wantErr:   "write profile missing is not configured",
			wantItems: []string{"keep"},
			wantRefs:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &captureProvider{}
			router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
			if err != nil {
				t.Fatal(err)
			}
			service := New(tt.cfg, router)
			result, err := service.IngestBatch(context.Background(), tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("IngestBatch() error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("IngestBatch() error = %v", err)
			}
			gotItems := itemTexts(provider.items)
			if !reflect.DeepEqual(gotItems, tt.wantItems) {
				t.Fatalf("items = %#v, want %#v", gotItems, tt.wantItems)
			}
			if len(result.Refs) != tt.wantRefs {
				t.Fatalf("refs = %#v, want %d", result.Refs, tt.wantRefs)
			}
		})
	}
}

func TestHookBufferConfigTable(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Version: 1,
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{
							Buffer: config.HookBufferConfig{Enabled: true, Flush: true, FlushCount: 3},
						},
					},
				},
			},
			"disabled": {
				Enabled: false,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{Buffer: config.HookBufferConfig{Enabled: true}},
					},
				},
			},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: &captureProvider{}, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(cfg, router)

	tests := []struct {
		name  string
		event HookEvent
		want  config.HookBufferConfig
	}{
		{name: "blank target defaults to codex", event: HookEvent{Event: "user_input"}, want: config.HookBufferConfig{Enabled: true, Flush: true, FlushCount: 3}},
		{name: "missing hook returns zero config", event: HookEvent{Target: "codex", Event: "turn_end"}},
		{name: "disabled agent returns zero config", event: HookEvent{Target: "disabled", Event: "user_input"}},
		{name: "missing agent returns zero config", event: HookEvent{Target: "missing", Event: "user_input"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.HookBufferConfig(tt.event); got != tt.want {
				t.Fatalf("HookBufferConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRenderAndInsertionHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("templates", func(t *testing.T) {
		tests := []struct {
			name    string
			tmpl    string
			event   HookEvent
			want    string
			wantErr bool
		}{
			{name: "blank template", tmpl: "  ", want: ""},
			{name: "renders prompt workspace and raw json", tmpl: "{{ .prompt }} @ {{ .workspace }} {{ .raw_json }}", event: HookEvent{Prompt: "hello", Workspace: "/repo", Raw: json.RawMessage(`{"prompt":"hello"}`)}, want: `hello @ /repo {"prompt":"hello"}`},
			{name: "missing key renders zero value", tmpl: "{{ .metadata.missing }}", event: HookEvent{Metadata: map[string]string{"present": "yes"}}, want: ""},
			{name: "invalid template", tmpl: "{{", wantErr: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := renderHookTemplate(tt.tmpl, tt.event)
				if tt.wantErr {
					if err == nil {
						t.Fatal("expected error")
					}
					return
				}
				if err != nil {
					t.Fatalf("renderHookTemplate() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("renderHookTemplate() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("insertion filters", func(t *testing.T) {
		hits := []memory.MemoryHit{
			{ID: "low", Text: "paxm low score", Score: 0.2},
			{ID: "term", Text: "paxm matching term", Score: 0.9},
			{ID: "other", Text: "unrelated", Score: 0.95},
		}
		tests := []struct {
			name   string
			query  string
			policy config.HookInsertionConfig
			want   []string
		}{
			{name: "zero policy keeps all", query: "paxm", want: []string{"low", "term", "other"}},
			{name: "score term and limit", query: "please paxm", policy: config.HookInsertionConfig{MinScore: 0.8, MaxItems: 1, RequireQueryTerms: true}, want: []string{"term"}},
			{name: "stopwords do not match", query: "please check", policy: config.HookInsertionConfig{RequireQueryTerms: true}, want: []string{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := hitIDs(filterHookInsertionHits(hits, tt.query, tt.policy))
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("filtered hits = %#v, want %#v", got, tt.want)
				}
			})
		}
	})

	t.Run("misc helpers", func(t *testing.T) {
		if got := fmtMissingProfile("write", "archive").Error(); got != "write profile archive is not configured" {
			t.Fatalf("fmtMissingProfile() = %q", got)
		}
		if got := firstNonEmpty("", " ", "value"); got != "value" {
			t.Fatalf("firstNonEmpty() = %q", got)
		}
	})
}

func writeConfigForProfiles(names ...string) config.Config {
	profiles := make(map[string]config.WriteProfileConfig, len(names))
	for _, name := range names {
		profiles[name] = config.WriteProfileConfig{
			Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
		}
	}
	return config.Config{Version: 1, WriteProfiles: profiles}
}

func itemTexts(items []memory.MemoryItem) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Text)
	}
	sort.Strings(values)
	return values
}

func hitIDs(hits []memory.MemoryHit) []string {
	values := make([]string, 0, len(hits))
	for _, hit := range hits {
		values = append(values, hit.ID)
	}
	return values
}
