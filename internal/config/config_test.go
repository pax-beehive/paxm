package config

import (
	"os"
	"path/filepath"
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

func TestDefaultConfigUsesConservativePassiveRecall(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	passive := cfg.RecallProfiles["passive"]
	if passive.MaxResults != 2 || passive.Thresholds.MinRelevance != 0.75 || passive.Thresholds.MinScore != 0.75 {
		t.Fatalf("unexpected passive profile: %#v", passive)
	}
	hook := cfg.Agents["codex"].Hooks["user_input"].Recall
	if hook.Profile != "passive" || hook.MaxResults != 2 {
		t.Fatalf("user_input hook should use passive profile: %#v", hook)
	}
	if hook.Insertion.MinScore != 0.8 || hook.Insertion.MaxItems != 2 || !hook.Insertion.RequireQueryTerms {
		t.Fatalf("unexpected passive insertion policy: %#v", hook.Insertion)
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
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "local" || writeRoutes[0].Required || writeRoutes[0].Weight != 2 {
		t.Fatalf("legacy write route was not migrated: %#v", writeRoutes)
	}
	hook := cfg.Agents["codex"].Hooks["user_input"].Recall
	if !hook.Enabled || hook.Profile != "default" || hook.MaxResults != 4 {
		t.Fatalf("legacy hook was not migrated: %#v", hook)
	}
	if _, ok := cfg.Agents["codex"].Hooks["user_prompt"]; ok {
		t.Fatalf("legacy user_prompt hook should be normalized to user_input: %#v", cfg.Agents["codex"].Hooks)
	}
	if cfg.Providers["local"].Read != nil || cfg.Hooks != nil {
		t.Fatalf("legacy fields should not survive normalization: %#v", cfg)
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
	if !cfg.Providers["local"].Enabled {
		t.Fatalf("legacy provider was not loaded: %#v", cfg.Providers)
	}
	if !Exists(yamlPath) {
		t.Fatalf("expected Exists to include legacy json fallback")
	}
}
