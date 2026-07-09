package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/config"
)

func TestCLISetupRememberRecallAndHookEvent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	codexConfigPath := filepath.Join(t.TempDir(), "codex.toml")
	t.Setenv("PAXM_CODEX_CONFIG", codexConfigPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("\n\n\n\n\n")
	code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Select memory providers to enable") {
		t.Fatalf("setup did not show provider selector: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Select agent hooks to install") {
		t.Fatalf("setup did not show hook selector: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup did not install hook shim: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "registered Codex global hook") {
		t.Fatalf("setup did not register global codex hook: %s", stdout.String())
	}
	codexConfig, err := os.ReadFile(codexConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	codexConfigText := string(codexConfig)
	for _, expected := range []string{
		"SessionStart",
		"UserPromptSubmit",
		"Stop",
		"codex-session_start",
		"codex-user_input",
		"codex-turn_end",
	} {
		if !strings.Contains(codexConfigText, expected) {
			t.Fatalf("codex config missing %q registration: %s", expected, codexConfigText)
		}
	}
	if strings.Count(stdout.String(), "installed hook shim") != 3 {
		t.Fatalf("setup should install three hook shims: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "remember", "--text", "paxm uses hook passive recall and provider fan-out"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remember failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "stored memory") {
		t.Fatalf("unexpected remember output: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "recall", "--query", "passive recall"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("recall failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hook passive recall") {
		t.Fatalf("unexpected recall output: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	event := strings.NewReader(`{"prompt":"passive recall","workspace":"/tmp/project"}`)
	code = Main([]string{"--config", configPath, "recall", "--hook-event", "--json"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook event recall failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"recall"`) || !strings.Contains(stdout.String(), "provider fan-out") {
		t.Fatalf("unexpected hook output: %s", stdout.String())
	}
}

func TestCLISetupInteractiveProviderChoices(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("1\n/custom/memory.jsonl\n3\n1\nnone\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup installed hook despite none selection: %s", stdout.String())
	}
	assertWriteOnlyConfig(t, configPath)

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "setup", "--force", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("force setup failed with code %d: %s", code, stderr.String())
	}
	assertWriteOnlyConfig(t, configPath)
}

func TestCLISetupInteractiveZepProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("2\nzep-key\n2\ngraph-1\nedges\n1\n2\nnone\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["local"].Enabled {
		t.Fatalf("local should be disabled when only zep was selected: %#v", cfg.Providers)
	}
	zep := cfg.Providers["zep"]
	if !zep.Enabled || zep.APIKey != "zep-key" || zep.GraphID != "graph-1" || zep.UserID != "" || zep.SearchScope != "edges" {
		t.Fatalf("unexpected zep provider config: %#v", zep)
	}
	recallRoutes := cfg.RecallProfiles["default"].Providers
	if len(recallRoutes) != 1 || recallRoutes[0].Name != "zep" || recallRoutes[0].Required {
		t.Fatalf("unexpected recall routes: %#v", recallRoutes)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "zep" || writeRoutes[0].Required {
		t.Fatalf("unexpected write routes: %#v", writeRoutes)
	}
	if strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup installed hook despite none selection: %s", stdout.String())
	}
}

func TestSetupBaseConfigMergesLegacyHookWriteDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	legacyPath := filepath.Join(dir, "config.json")
	legacy := `{
  "version": 1,
  "providers": {
    "local": {
      "type": "local",
      "enabled": true,
      "read": true,
      "write": true,
      "required": false,
      "path": "/tmp/paxm-memory.jsonl",
      "weight": 1
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
            "max_results": 8,
            "output": "markdown"
          }
        }
      }
    }
  }
}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := setupBaseConfig(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	userInput := cfg.Agents["codex"].Hooks["user_input"]
	if !userInput.Recall.Enabled || !userInput.Write.Enabled || !userInput.Write.Buffer.Enabled {
		t.Fatalf("legacy user prompt hook did not receive user_input write defaults: %#v", userInput)
	}
	if !cfg.Agents["codex"].Hooks["turn_end"].Write.Buffer.Flush {
		t.Fatalf("turn_end flush default was not merged: %#v", cfg.Agents["codex"].Hooks["turn_end"])
	}
}

func TestSetupRemovesLegacyHookShim(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	legacyShim := filepath.Join(filepath.Dir(configPath), "hooks", "codex-user_prompt")
	if err := os.MkdirAll(filepath.Dir(legacyShim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyShim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"--config", configPath, "setup", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(legacyShim); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy shim should be removed, stat err: %v", err)
	}
}

func TestCLISetupRequiresAProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	setupInput := strings.NewReader("none\n")

	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code == 0 {
		t.Fatalf("setup unexpectedly succeeded: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "setup requires at least one memory provider") {
		t.Fatalf("unexpected setup error: %s", stderr.String())
	}
}

func assertWriteOnlyConfig(t *testing.T, configPath string) {
	t.Helper()

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	local := cfg.Providers["local"]
	if local.Path != "/custom/memory.jsonl" {
		t.Fatalf("unexpected local path: %q", local.Path)
	}
	if recallHasProvider(cfg, "local") {
		t.Fatalf("local should not be in default recall profile: %#v", cfg.RecallProfiles["default"])
	}
	writeProfile := cfg.WriteProfiles["default"]
	if len(writeProfile.Providers) != 1 || writeProfile.Providers[0].Name != "local" || !writeProfile.Providers[0].Required {
		t.Fatalf("unexpected default write profile: %#v", writeProfile)
	}
	for eventName, hook := range cfg.Agents["codex"].Hooks {
		if hook.Recall.Enabled || hook.Write.Enabled {
			t.Fatalf("codex hook %s should be disabled: %#v", eventName, cfg.Agents["codex"])
		}
	}
}

func recallHasProvider(cfg config.Config, provider string) bool {
	for _, route := range cfg.RecallProfiles["default"].Providers {
		if route.Name == provider {
			return true
		}
	}
	return false
}

func TestInstallCodexGlobalHookPreservesExistingHooks(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[hooks]
UserPromptSubmit = [{ hooks = [{ type = "command", command = "paxl __agent-hook", async = false }] }, { hooks = [{ type = "command", command = "'/Users/toddzheng/.config/paxm/hooks/codex-user_prompt'", async = false, statusMessage = "Recalling paxm memory" }] }]

[hooks.state]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(t.TempDir(), "codex-user_input")
	if err := installCodexGlobalHook(path, scriptPath, "user_input"); err != nil {
		t.Fatal(err)
	}
	if err := installCodexGlobalHook(path, scriptPath, "user_input"); err != nil {
		t.Fatal(err)
	}
	updatedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updatedBytes)
	if !strings.Contains(updated, "paxl __agent-hook") {
		t.Fatalf("existing hook was not preserved: %s", updated)
	}
	if strings.Count(updated, "codex-user_input") != 1 {
		t.Fatalf("paxm hook was not installed exactly once: %s", updated)
	}
	if strings.Contains(updated, "codex-user_prompt") {
		t.Fatalf("legacy paxm hook was not pruned: %s", updated)
	}
	if strings.Index(updated, "[hooks.state]") < strings.Index(updated, "codex-user_input") {
		t.Fatalf("paxm hook was inserted outside [hooks]: %s", updated)
	}
}

func TestInstallCodexGlobalHookRegistersAllEvents(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	for _, event := range installedHookEvents() {
		scriptPath := filepath.Join(t.TempDir(), "codex-"+event.ConfigEvent)
		if err := installCodexGlobalHook(path, scriptPath, event.ConfigEvent); err != nil {
			t.Fatal(err)
		}
	}
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)
	for _, expected := range []string{
		"SessionStart",
		"startup|resume|clear|compact",
		"UserPromptSubmit",
		"Stop",
		"codex-session_start",
		"codex-user_input",
		"codex-turn_end",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("codex config missing %q: %s", expected, content)
		}
	}
}

func TestCLIDoesNotExposeHookOrProviderCommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"hook", "run"}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("hook command unexpectedly succeeded: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command "hook"`) {
		t.Fatalf("unexpected hook error: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"provider", "list"}, nil, &stdout, &stderr); code == 0 {
		t.Fatalf("provider command unexpectedly succeeded: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unknown command "provider"`) {
		t.Fatalf("unexpected provider error: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("help failed with code %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "__hook") || strings.Contains(stdout.String(), "hook run") {
		t.Fatalf("hidden hook commands leaked in help: %s", stdout.String())
	}
}
