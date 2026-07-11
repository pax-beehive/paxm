package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveWritesYAMLByDefault(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := DefaultConfig(path)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(bytes)
	if !strings.Contains(content, "recall_profiles:") || strings.Contains(content, `"recall_profiles"`) {
		t.Fatalf("expected YAML config, got: %s", content)
	}
}

func TestSaveRejectsInvalidMemoryPolicyWithoutChangingExistingConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := DefaultConfig(path)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.WriteProfiles["stm"]
	profile.Tier = "stn"
	cfg.WriteProfiles["stm"] = profile
	if err := Save(path, cfg); err == nil || !strings.Contains(err.Error(), `invalid tier "stn"`) {
		t.Fatalf("Save() error = %v, want invalid tier", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatal("invalid save changed the existing config file")
	}
}

func TestDefaultConfigUsesConservativePassiveRecall(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	if provider := cfg.Providers["sqlite"]; provider.Type != "sqlite" || !strings.HasSuffix(provider.Path, "memory.sqlite") {
		t.Fatalf("default sqlite provider is invalid: %#v", provider)
	}
	if provider := cfg.Providers["mem0"]; provider.Type != "mem0" || provider.Enabled || provider.BaseURL != "http://localhost:8888" {
		t.Fatalf("default mem0 provider is invalid: %#v", provider)
	}
	if provider := cfg.Providers["jsonrpc"]; provider.Type != "jsonrpc" || provider.Enabled || provider.Transport != "stdio" || provider.Timeout != "30s" {
		t.Fatalf("default jsonrpc provider is invalid: %#v", provider)
	}
	active := cfg.RecallProfiles["default"]
	if active.MaxResults != 3 {
		t.Fatalf("default active recall should return 3 results: %#v", active)
	}
	if !reflect.DeepEqual(active.Tiers, []string{"stm", "ltm"}) {
		t.Fatalf("default active recall should read STM and LTM: %#v", active)
	}
	passive := cfg.RecallProfiles["passive"]
	if passive.MaxResults != 2 || passive.Thresholds.MinRelevance != 0.75 || passive.Thresholds.MinScore != 0.75 {
		t.Fatalf("unexpected passive profile: %#v", passive)
	}
	if !reflect.DeepEqual(passive.Tiers, []string{"ltm"}) {
		t.Fatalf("passive recall should read LTM only: %#v", passive)
	}
	initialProfile := cfg.RecallProfiles["passive_initial"]
	if initialProfile.MaxResults != 5 || initialProfile.Thresholds.MinRelevance != 0.35 || initialProfile.Thresholds.MinScore != 0.35 {
		t.Fatalf("unexpected initial passive profile: %#v", initialProfile)
	}
	if !reflect.DeepEqual(initialProfile.Tiers, []string{"ltm"}) {
		t.Fatalf("initial passive recall should read LTM only: %#v", initialProfile)
	}
	hook := cfg.Agents["codex"].Hooks["user_input"].Recall
	if hook.Profile != "passive" || hook.MaxResults != 2 {
		t.Fatalf("user_input hook should use passive profile: %#v", hook)
	}
	if hook.Insertion.MinScore != 0.8 || hook.Insertion.MaxItems != 2 || !hook.Insertion.RequireQueryTerms {
		t.Fatalf("unexpected passive insertion policy: %#v", hook.Insertion)
	}
	if hook.Initial == nil || !hook.Initial.Enabled || hook.Initial.Profile != "passive_initial" || hook.Initial.MaxResults != 5 {
		t.Fatalf("user_input hook should include initial recall override: %#v", hook.Initial)
	}
	if hook.Initial.Insertion.MinScore != 0.35 || hook.Initial.Insertion.MaxItems != 5 || hook.Initial.Insertion.RequireQueryTerms {
		t.Fatalf("unexpected initial insertion policy: %#v", hook.Initial.Insertion)
	}
	claude := cfg.Agents["claude"]
	if claude.Enabled {
		t.Fatalf("claude hooks should be opt-in by default: %#v", claude)
	}
	claudeRecall := claude.Hooks["user_input"].Recall
	if !claudeRecall.Enabled || claudeRecall.Profile != "passive" || claudeRecall.Initial == nil || claudeRecall.Initial.Profile != "passive_initial" {
		t.Fatalf("unexpected Claude Code passive recall defaults: %#v", claudeRecall)
	}
	claudeTurnEnd := claude.Hooks["turn_end"].Write
	if !claudeTurnEnd.Enabled || claudeTurnEnd.Mode != "turn_end" || !claudeTurnEnd.Buffer.Flush {
		t.Fatalf("unexpected Claude Code turn-end defaults: %#v", claudeTurnEnd)
	}
	piTurnEnd := cfg.Agents["pi"].Hooks["turn_end"].Write
	if !cfg.Agents["pi"].Hooks["user_input"].Recall.Enabled {
		t.Fatalf("pi passive recall should be available when the agent is selected: %#v", cfg.Agents["pi"])
	}
	if !piTurnEnd.Enabled || piTurnEnd.Profile != "ltm" || piTurnEnd.Mode != "turn_end" || !piTurnEnd.Buffer.Flush {
		t.Fatalf("pi turn_end should default to best-effort buffered write: %#v", piTurnEnd)
	}
	if stm := cfg.WriteProfiles["stm"]; stm.Tier != "stm" || stm.ExpiresAfter != defaultSTMExpiresAfter {
		t.Fatalf("stm write profile should be short-term: %#v", stm)
	}
	if ltm := cfg.WriteProfiles["ltm"]; ltm.Tier != "ltm" || ltm.ExpiresAfter != "" {
		t.Fatalf("ltm write profile should be long-term: %#v", ltm)
	}
}

func TestPassiveRecallProfileBuildersCopyBaseRoutes(t *testing.T) {
	t.Parallel()

	base := RecallProfileConfig{
		Providers: []ProviderRouteConfig{{Name: "sqlite", Required: true, Weight: 2}},
		Ranking:   RankingConfig{RecencyBoost: 0.25},
	}
	passive := PassiveRecallProfileFrom(base)
	initial := PassiveInitialRecallProfileFrom(base)

	if passive.MaxResults != 2 || passive.Thresholds.MinRelevance != 0.75 || passive.Ranking.RecencyBoost != 0.25 || !reflect.DeepEqual(passive.Tiers, []string{"ltm"}) {
		t.Fatalf("unexpected passive profile: %#v", passive)
	}
	if initial.MaxResults != 5 || initial.Thresholds.MinRelevance != 0.35 || initial.Ranking.RecencyBoost != 0.25 || !reflect.DeepEqual(initial.Tiers, []string{"ltm"}) {
		t.Fatalf("unexpected initial profile: %#v", initial)
	}
	base.Providers[0].Name = "changed"
	if passive.Providers[0].Name != "sqlite" || initial.Providers[0].Name != "sqlite" {
		t.Fatalf("profile builders should copy provider routes: passive=%#v initial=%#v", passive, initial)
	}
}

func TestProviderRouteHelpers(t *testing.T) {
	t.Parallel()

	routes := []ProviderRouteConfig{{Name: "sqlite", Required: true, Weight: 3}}
	routes = UpsertProviderRoute(routes, "mem0", false)
	routes = UpsertProviderRoute(routes, "sqlite", false)

	required, ok := ProviderRouteRequired(routes, "sqlite")
	if !ok || required {
		t.Fatalf("sqlite route should exist and become best-effort: %#v", routes)
	}
	if routes[0].Weight != 3 {
		t.Fatalf("upsert should preserve existing route weight: %#v", routes)
	}
	required, ok = ProviderRouteRequired(routes, "mem0")
	if !ok || required || routes[1].Weight != 1 {
		t.Fatalf("new route should default weight and requested policy: %#v", routes)
	}
	routes = RemoveProviderRoute(routes, "sqlite")
	if _, ok := ProviderRouteRequired(routes, "sqlite"); ok {
		t.Fatalf("sqlite route should be removed: %#v", routes)
	}
	if len(routes) != 1 || routes[0].Name != "mem0" {
		t.Fatalf("unexpected remaining routes: %#v", routes)
	}
}

func TestDefaultRecallHelpers(t *testing.T) {
	t.Parallel()

	defaults := RecallProfileConfig{
		MaxResults: defaultRecallMaxResults,
		Thresholds: DefaultRecallThresholds(),
	}
	if !IsDefaultRecallProfile(defaults) || !IsDefaultRecallThresholds(defaults.Thresholds) {
		t.Fatalf("expected default recall helpers to recognize defaults: %#v", defaults)
	}
	defaults.MaxResults++
	if IsDefaultRecallProfile(defaults) {
		t.Fatalf("modified recall profile should not be treated as default: %#v", defaults)
	}
	if DefaultMem0BaseURL() != defaultMem0BaseURL {
		t.Fatalf("default mem0 base URL helper changed")
	}
	if DefaultSTMExpiresAfter() != defaultSTMExpiresAfter {
		t.Fatalf("default stm expiry helper changed")
	}
}

func TestDefaultConfigEnablesBoundedTelemetry(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := DefaultConfig(configPath)
	if cfg.Telemetry.Enabled == nil || !*cfg.Telemetry.Enabled {
		t.Fatalf("telemetry should be enabled by default: %#v", cfg.Telemetry)
	}
	if cfg.Telemetry.Dir != filepath.Join(filepath.Dir(configPath), "state") {
		t.Fatalf("unexpected telemetry dir: %q", cfg.Telemetry.Dir)
	}
	if cfg.Telemetry.EventsFile != "events.jsonl" || cfg.Telemetry.MetricsFile != "metrics.json" {
		t.Fatalf("unexpected telemetry files: %#v", cfg.Telemetry)
	}
	if cfg.Telemetry.MaxEventFileBytes <= 0 || cfg.Telemetry.MaxEventFiles != 3 || cfg.Telemetry.RetentionDays != 30 {
		t.Fatalf("unexpected telemetry bounds: %#v", cfg.Telemetry)
	}
	if cfg.Telemetry.CaptureQueryPreview {
		t.Fatalf("query preview should be off by default")
	}
}

func TestNormalizeBackfillsInitialPassiveRecall(t *testing.T) {
	t.Parallel()

	cfg := Normalize(Config{
		Version: 1,
		Providers: map[string]ProviderConfig{
			"local": {Type: "local", Enabled: true, Path: "/tmp/memory.jsonl"},
		},
		RecallProfiles: map[string]RecallProfileConfig{
			"default": {
				Providers: []ProviderRouteConfig{{Name: "local", Required: false, Weight: 1}},
			},
			"passive": {
				Providers: []ProviderRouteConfig{{Name: "local", Required: false, Weight: 1}},
				Thresholds: RecallThresholdConfig{
					MinRelevance: 0.75,
					MinScore:     0.75,
				},
			},
		},
		Agents: map[string]AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]AgentHookConfig{
					"user_input": {
						Recall: HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    2,
						},
					},
				},
			},
		},
	})

	initialProfile := cfg.RecallProfiles["passive_initial"]
	if len(initialProfile.Providers) != 1 || initialProfile.Providers[0].Name != "sqlite" || initialProfile.Providers[0].Required {
		t.Fatalf("initial profile should inherit passive routes: %#v", initialProfile)
	}
	initial := cfg.Agents["codex"].Hooks["user_input"].Recall.Initial
	if initial == nil || !initial.Enabled || initial.Profile != "passive_initial" || initial.MaxResults != 5 {
		t.Fatalf("user_input recall should receive initial override: %#v", initial)
	}
}

func TestLoadMigratesLegacyJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")
	legacy := `{
  "version": 1,
  "providers": {
    "local": {
      "type": "local",
      "enabled": true,
      "read": false,
      "write": true,
      "required": false,
      "path": "/tmp/paxm-memory.jsonl",
      "weight": 2
    }
  },
  "hooks": {
    "codex": {
      "enabled": true,
      "events": {
        "user_prompt": {
          "recall": {
            "enabled": true,
            "query_template": "{{ .prompt }}",
            "max_results": 4,
            "output": "markdown"
          }
        }
      }
    }
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RecallProfiles["default"].Providers) != 0 {
		t.Fatalf("legacy read=false should remove provider from recall profile: %#v", cfg.RecallProfiles["default"])
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "sqlite" || writeRoutes[0].Required || writeRoutes[0].Weight != 2 {
		t.Fatalf("legacy write route was not migrated: %#v", writeRoutes)
	}
	hook := cfg.Agents["codex"].Hooks["user_input"].Recall
	if !hook.Enabled || hook.Profile != "default" || hook.MaxResults != 4 {
		t.Fatalf("legacy hook was not migrated: %#v", hook)
	}
	if _, ok := cfg.Agents["codex"].Hooks["user_prompt"]; ok {
		t.Fatalf("legacy user_prompt hook should be normalized to user_input: %#v", cfg.Agents["codex"].Hooks)
	}
	if cfg.Providers["sqlite"].Read != nil || cfg.Hooks != nil {
		t.Fatalf("legacy fields should not survive normalization: %#v", cfg)
	}
	if _, ok := cfg.Providers["local"]; ok {
		t.Fatalf("legacy local provider should be renamed: %#v", cfg.Providers)
	}
	if cfg.Providers["sqlite"].Type != "sqlite" || cfg.Providers["sqlite"].Path != "/tmp/paxm-memory.sqlite" {
		t.Fatalf("legacy local provider should normalize to sqlite: %#v", cfg.Providers["sqlite"])
	}
}

func TestLoadRejectsInvalidMemoryTierAndTTLConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "unknown recall tier",
			config: `recall_profiles:
  default:
    tiers: [stn]
`,
			wantErr: `recall profile "default" has invalid tier "stn"`,
		},
		{
			name: "unknown write tier",
			config: `write_profiles:
  archive:
    tier: permanent
`,
			wantErr: `write profile "archive" has invalid tier "permanent"`,
		},
		{
			name: "stm missing ttl",
			config: `write_profiles:
  scratch:
    tier: stm
`,
			wantErr: `write profile "scratch" with tier stm requires expires_after`,
		},
		{
			name: "stm invalid ttl",
			config: `write_profiles:
  scratch:
    tier: stm
    expires_after: tomorrow
`,
			wantErr: `write profile "scratch" has invalid expires_after`,
		},
		{
			name: "stm non-positive ttl",
			config: `write_profiles:
  scratch:
    tier: stm
    expires_after: 0s
`,
			wantErr: `write profile "scratch" expires_after must be positive`,
		},
		{
			name: "ltm with ttl",
			config: `write_profiles:
  archive:
    tier: ltm
    expires_after: 24h
`,
			wantErr: `write profile "archive" with tier ltm must not set expires_after`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.config), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadFallsBackFromDefaultYAMLToLegacyJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "config.yaml")
	jsonPath := filepath.Join(dir, "config.json")
	legacy := `{"version":1,"providers":{"local":{"type":"local","enabled":true,"path":"/tmp/memory.jsonl"}}}`
	if err := os.WriteFile(jsonPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Providers["sqlite"].Enabled {
		t.Fatalf("legacy provider was not loaded: %#v", cfg.Providers)
	}
	if _, ok := cfg.Providers["local"]; ok {
		t.Fatalf("legacy local provider should be renamed: %#v", cfg.Providers)
	}
	if cfg.Providers["sqlite"].Type != "sqlite" || cfg.Providers["sqlite"].Path != "/tmp/memory.sqlite" {
		t.Fatalf("legacy local provider should normalize to sqlite: %#v", cfg.Providers["sqlite"])
	}
	if !Exists(yamlPath) {
		t.Fatalf("expected Exists to include legacy json fallback")
	}
}

func TestNormalizeMergesLegacyLocalRoutesIntoSQLite(t *testing.T) {
	t.Parallel()

	cfg := Normalize(Config{
		Version: 1,
		Providers: map[string]ProviderConfig{
			"local":  {Type: "local", Enabled: true, Path: "/tmp/memory.jsonl"},
			"sqlite": {Type: "sqlite", Enabled: true, Path: "/tmp/memory.sqlite"},
		},
		RecallProfiles: map[string]RecallProfileConfig{
			"default": {
				Providers: []ProviderRouteConfig{
					{Name: "local", Required: false, Weight: 1},
					{Name: "sqlite", Required: true, Weight: 2},
				},
			},
		},
		WriteProfiles: map[string]WriteProfileConfig{
			"default": {
				Providers: []ProviderRouteConfig{
					{Name: "local", Required: true, Weight: 1},
					{Name: "sqlite", Required: false, Weight: 3},
				},
			},
		},
	})

	recallRoutes := cfg.RecallProfiles["default"].Providers
	if len(recallRoutes) != 1 || recallRoutes[0].Name != "sqlite" || !recallRoutes[0].Required || recallRoutes[0].Weight != 2 {
		t.Fatalf("legacy recall routes should merge into sqlite: %#v", recallRoutes)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "sqlite" || !writeRoutes[0].Required || writeRoutes[0].Weight != 3 {
		t.Fatalf("legacy write routes should merge into sqlite: %#v", writeRoutes)
	}
}
