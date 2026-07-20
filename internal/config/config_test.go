package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCaptureQueueMergesMissingConcurrencyDefaults(t *testing.T) {
	t.Parallel()
	cfg := Normalize(Config{CaptureQueue: CaptureQueueConfig{ProviderConcurrency: map[string]int{"mem0": 8}}})
	if cfg.CaptureQueue.ProviderConcurrency["mem0"] != 8 || cfg.CaptureQueue.ProviderConcurrency["sqlite"] != 1 || cfg.CaptureQueue.ProviderConcurrency["default"] != 4 {
		t.Fatalf("capture queue concurrency defaults were not merged: %#v", cfg.CaptureQueue.ProviderConcurrency)
	}
}

func TestNormalizeDerivesAgentIdentityAndPersonalScopes(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig("config.yaml")
	cfg.Identity.UserID = "Todd Smith"
	custom := cfg.Agents["claude"]
	custom.AgentID = "Research Claude"
	cfg.Agents["claude"] = custom

	normalized := Normalize(cfg)
	if normalized.Identity.UserID != "todd-smith" {
		t.Fatalf("user_id = %q", normalized.Identity.UserID)
	}
	if got := normalized.Agents["codex"].AgentID; got != "codex-todd-smith" {
		t.Fatalf("codex agent_id = %q", got)
	}
	if got := normalized.Agents["claude"].AgentID; got != "research-claude" {
		t.Fatalf("explicit agent_id = %q", got)
	}
	for name, profile := range normalized.WriteProfiles {
		if profile.Scope != (MemoryScopeConfig{Type: "personal", ID: "todd-smith"}) {
			t.Fatalf("write profile %q scope = %#v", name, profile.Scope)
		}
	}
}

func TestNormalizePreservesExplicitTeamScope(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig("config.yaml")
	cfg.Identity.UserID = "todd"
	profile := cfg.WriteProfiles["ltm"]
	profile.Scope = MemoryScopeConfig{Type: "TEAM", ID: "PAX Core"}
	cfg.WriteProfiles["ltm"] = profile

	normalized := Normalize(cfg)
	if got := normalized.WriteProfiles["ltm"].Scope; got != (MemoryScopeConfig{Type: "team", ID: "pax-core"}) {
		t.Fatalf("team scope = %#v", got)
	}
}

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
	if provider := cfg.Providers["mem0"]; provider.Type != "mem0" || provider.Enabled || provider.BaseURL != "http://localhost:8888" || provider.ScoreSemantics != string(ScoreSemanticsSimilarity) || provider.SearchScopePayload != string(Mem0SearchScopePayloadAuto) {
		t.Fatalf("default mem0 provider is invalid: %#v", provider)
	}
	if provider := cfg.Providers["mem0_cloud"]; provider.ScoreSemantics != string(ScoreSemanticsSimilarity) {
		t.Fatalf("default mem0 cloud score semantics = %q", provider.ScoreSemantics)
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
	if len(passive.Providers) != 1 || passive.Providers[0].Timeout != "250ms" {
		t.Fatalf("passive providers should have a tight timeout: %#v", passive.Providers)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Timeout != "30s" {
		t.Fatalf("write providers should have a bounded timeout: %#v", writeRoutes)
	}
	initialProfile := cfg.RecallProfiles["passive_initial"]
	if initialProfile.MaxResults != 5 || initialProfile.Thresholds.MinRelevance != 0.35 || initialProfile.Thresholds.MinScore != 0.35 {
		t.Fatalf("unexpected initial passive profile: %#v", initialProfile)
	}
	if !reflect.DeepEqual(initialProfile.Tiers, []string{"ltm"}) {
		t.Fatalf("initial passive recall should read LTM only: %#v", initialProfile)
	}
	hook := cfg.Agents["codex"].Hooks["user_input"].Recall
	if hook.Profile != "passive" || hook.MaxResults != 2 || hook.Timeout != "" || hook.TimeoutExtra != "100ms" {
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
	toolWrite := cfg.Agents["claude"].Hooks["tool_use"].Write
	if !toolWrite.Enabled || toolWrite.Profile != "ltm" || toolWrite.Mode != "tool_use" || !toolWrite.Buffer.Enabled || toolWrite.Buffer.Flush {
		t.Fatalf("unexpected Claude tool-use defaults: %#v", toolWrite)
	}
	toolFailureWrite := cfg.Agents["claude"].Hooks["tool_failure"].Write
	if !toolFailureWrite.Enabled || toolFailureWrite.Profile != "ltm" || toolFailureWrite.Mode != "tool_failure" || !toolFailureWrite.Buffer.Enabled || toolFailureWrite.Buffer.Flush {
		t.Fatalf("unexpected Claude tool-failure defaults: %#v", toolFailureWrite)
	}
	if _, ok := cfg.Agents["codex"].Hooks["tool_use"]; ok {
		t.Fatal("Codex should capture tools from the turn transcript, not partial PostToolUse hooks")
	}
	for agentName, agent := range cfg.Agents {
		for eventName, hook := range agent.Hooks {
			if strings.Contains(hook.Write.Template, "raw_json") {
				t.Fatalf("%s %s default write template should not store raw hook JSON: %q", agentName, eventName, hook.Write.Template)
			}
			if hook.Write.Enabled && hook.Write.Template != defaultHookWriteTemplate {
				t.Fatalf("%s %s default write template = %q, want %q", agentName, eventName, hook.Write.Template, defaultHookWriteTemplate)
			}
		}
	}
	piTurnEnd := cfg.Agents["pi"].Hooks["turn_end"].Write
	if !cfg.Agents["pi"].Hooks["user_input"].Recall.Enabled {
		t.Fatalf("pi passive recall should be available when the agent is selected: %#v", cfg.Agents["pi"])
	}
	if !piTurnEnd.Enabled || piTurnEnd.Profile != "ltm" || piTurnEnd.Mode != "turn_end" || !piTurnEnd.Buffer.Flush {
		t.Fatalf("pi turn_end should default to best-effort buffered write: %#v", piTurnEnd)
	}
	openCode := cfg.Agents["opencode"]
	if openCode.Enabled || !openCode.Hooks["user_input"].Recall.Enabled {
		t.Fatalf("OpenCode passive recall should be opt-in and available: %#v", openCode)
	}
	openCodeTurnEnd := openCode.Hooks["turn_end"].Write
	if !openCodeTurnEnd.Enabled || openCodeTurnEnd.Profile != "ltm" || openCodeTurnEnd.Mode != "turn_end" || !openCodeTurnEnd.Buffer.Flush {
		t.Fatalf("OpenCode turn_end should default to durable buffered write: %#v", openCodeTurnEnd)
	}
	if stm := cfg.WriteProfiles["stm"]; stm.Tier != "stm" || stm.ExpiresAfter != defaultSTMExpiresAfter {
		t.Fatalf("stm write profile should be short-term: %#v", stm)
	}
	if ltm := cfg.WriteProfiles["ltm"]; ltm.Tier != "ltm" || ltm.ExpiresAfter != "" {
		t.Fatalf("ltm write profile should be long-term: %#v", ltm)
	}
}

func TestDefaultConfigIncludesRequestedAgentIntegrations(t *testing.T) {
	t.Parallel()

	agents := DefaultConfig(filepath.Join(t.TempDir(), "config.yaml")).Agents
	for _, name := range []string{"cursor", "trae", "trae-cn", "kimi", "zcode", "kiro", "cline"} {
		agent, ok := agents[name]
		if !ok {
			t.Fatalf("default config missing %q agent", name)
		}
		if agent.Enabled {
			t.Fatalf("new agent %q should be opt-in", name)
		}
		if !agent.ActiveRecall.Enabled || agent.ActiveRecall.Profile != "default" {
			t.Fatalf("agent %q active recall = %#v", name, agent.ActiveRecall)
		}
		for _, event := range []string{"session_start", "user_input", "turn_end"} {
			if _, ok := agent.Hooks[event]; !ok {
				t.Fatalf("agent %q missing %q hook", name, event)
			}
		}
	}

	if agents["cursor"].Hooks["user_input"].Recall.Enabled {
		t.Fatal("Cursor beforeSubmitPrompt cannot inject prompt-specific recall")
	}
	for _, name := range []string{"trae", "trae-cn", "kimi", "zcode", "kiro", "cline"} {
		if !agents[name].Hooks["user_input"].Recall.Enabled {
			t.Fatalf("agent %q should enable passive recall", name)
		}
	}
}

func TestScoreSemanticsConfigTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   string
		configured string
		want       ScoreSemantics
		wantErr    string
	}{
		{name: "mem0 default", provider: "mem0", want: ScoreSemanticsSimilarity},
		{name: "cloud default", provider: "mem0-cloud", want: ScoreSemanticsSimilarity},
		{name: "explicit similarity", provider: "mem0", configured: " Similarity ", want: ScoreSemanticsSimilarity},
		{name: "explicit distance", provider: "mem0", configured: "distance", want: ScoreSemanticsDistance},
		{name: "invalid value", provider: "mem0-cloud", configured: "cosine", wantErr: "score_semantics must be similarity or distance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Providers: map[string]ProviderConfig{
				"memory": {Type: tt.provider, ScoreSemantics: tt.configured},
			}}
			if err := Validate(cfg); tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			got := Normalize(cfg).Providers["memory"].ScoreSemantics
			if got != string(tt.want) {
				t.Fatalf("normalized score_semantics = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMem0SearchScopePayloadConfigTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provider   string
		configured string
		want       Mem0SearchScopePayload
		wantErr    string
	}{
		{name: "default auto", provider: "mem0", want: Mem0SearchScopePayloadAuto},
		{name: "explicit filters", provider: "mem0", configured: " Filters ", want: Mem0SearchScopePayloadFilters},
		{name: "explicit top level", provider: "mem0", configured: "top_level", want: Mem0SearchScopePayloadTopLevel},
		{name: "invalid self hosted value", provider: "mem0", configured: "both", wantErr: "search_scope_payload must be auto, filters, or top_level"},
		{name: "cloud ignores self hosted setting", provider: "mem0-cloud", configured: "platform-v3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Providers: map[string]ProviderConfig{
				"memory": {Type: tt.provider, SearchScopePayload: tt.configured},
			}}
			if err := Validate(cfg); tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			got := Normalize(cfg).Providers["memory"].SearchScopePayload
			if tt.provider == "mem0" && got != string(tt.want) {
				t.Fatalf("normalized search_scope_payload = %q, want %q", got, tt.want)
			}
			if tt.provider != "mem0" && got != tt.configured {
				t.Fatalf("non-Mem0 search_scope_payload changed to %q", got)
			}
		})
	}
}

func TestDefaultProviderRecallTimeoutUsesCloudBudget(t *testing.T) {
	if got := DefaultProviderRecallTimeout("mem0-cloud"); got != "800ms" {
		t.Fatalf("cloud timeout = %q", got)
	}
	if got := DefaultProviderRecallTimeout("memos-cloud"); got != "800ms" {
		t.Fatalf("memos cloud timeout = %q", got)
	}
	if got := DefaultProviderRecallTimeout("sqlite"); got != "250ms" {
		t.Fatalf("sqlite timeout = %q", got)
	}
}

func TestNormalizeMigratesLegacyPassiveTimeoutDefaults(t *testing.T) {
	cfg := Config{
		Version:   1,
		Providers: map[string]ProviderConfig{"cloud": {Type: "mem0-cloud", Enabled: true}},
		RecallProfiles: map[string]RecallProfileConfig{
			"passive": {Providers: []ProviderRouteConfig{{Name: "cloud", Timeout: "250ms"}}},
		},
		Agents: map[string]AgentConfig{"opencode": {Enabled: true, Hooks: map[string]AgentHookConfig{
			"user_input": {Recall: HookRecallConfig{Enabled: true, Profile: "passive", Timeout: "800ms"}},
		}}},
	}
	normalized := Normalize(cfg)
	route := normalized.RecallProfiles["passive"].Providers[0]
	if got := route.Timeout; got != "800ms" {
		t.Fatalf("cloud route timeout = %q", got)
	}
	if route.Thresholds == nil || route.Thresholds.MinRelevance != 0.20 || route.Thresholds.MinScore != 0.20 {
		t.Fatalf("cloud route thresholds = %#v", route.Thresholds)
	}
	if infer := normalized.Providers["cloud"].Infer; infer == nil || *infer {
		t.Fatalf("cloud infer = %#v, want false", infer)
	}
	recall := normalized.Agents["opencode"].Hooks["user_input"].Recall
	if recall.Timeout != "" || recall.TimeoutExtra != "100ms" {
		t.Fatalf("normalized recall = %#v", recall)
	}
}

func TestValidateAcceptsKnownIntegrationOwners(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	for _, tc := range []struct{ agent, owner string }{{"codex", ""}, {"codex", IntegrationOwnerPaxm}, {"codex", IntegrationOwnerCodexPlugin}, {"claude", IntegrationOwnerClaudePlugin}} {
		agent := cfg.Agents[tc.agent]
		agent.Integration.Owner = tc.owner
		cfg.Agents[tc.agent] = agent
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate() owner %q: %v", tc.owner, err)
		}
	}
	codex := cfg.Agents["codex"]
	codex.Integration.Owner = "unknown"
	cfg.Agents["codex"] = codex
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "invalid integration owner") {
		t.Fatalf("Validate() error = %v, want invalid integration owner", err)
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
	if DefaultOpenVikingBaseURL() != defaultOpenVikingBaseURL {
		t.Fatalf("default OpenViking base URL helper changed")
	}
	openviking := DefaultConfig(filepath.Join(t.TempDir(), "config.yaml")).Providers["openviking"]
	if openviking.Type != "openviking" || openviking.Enabled || openviking.BaseURL != defaultOpenVikingBaseURL {
		t.Fatalf("unexpected default OpenViking provider: %#v", openviking)
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
	if cfg.Telemetry.CaptureQueryPreview != nil {
		t.Fatalf("query preview should be unset (off) by default: %#v", cfg.Telemetry)
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
