package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/adapters"
	zepadapter "github.com/pax-beehive/memory-adaptor/internal/adapters/zep"
	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
	"github.com/pax-beehive/memory-adaptor/internal/telemetry"
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
	if !strings.Contains(stdout.String(), "Select agents for passive memory") {
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

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "history", "--days", "7"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("history failed with code %d: %s", code, stderr.String())
	}
	history := stdout.String()
	for _, expected := range []string{
		"== paxm history ==",
		"window: last 7 days",
		"status: ok",
		"recall funnel",
		"recalls  hits  inserted  insert_rate",
		"write pipeline",
		"write_events  items  provider_writes  provider_refs  flushes  provider_ref_rate",
		"by profile",
		"default",
		"passive",
		"by agent",
		"codex",
		"passive_recalls",
		"by provider",
		"sqlite",
		"provider_errors",
	} {
		if !strings.Contains(history, expected) {
			t.Fatalf("history missing %q: %s", expected, history)
		}
	}
}

func TestCLISetupCodexPluginOwnsHooks(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	codexConfigPath := filepath.Join(t.TempDir(), "codex.toml")
	t.Setenv("PAXM_CODEX_CONFIG", codexConfigPath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := Main([]string{"--config", configPath, "setup", "--integration", "codex-plugin"}, strings.NewReader("\n\n\n\n\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("plugin setup failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Codex hooks are owned by the paxm-memory plugin") {
		t.Fatalf("plugin ownership was not reported: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "registered Codex global hook") {
		t.Fatalf("plugin setup should not register a global Codex hook: %s", stdout.String())
	}
	if _, err := os.Stat(codexConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin setup should not create Codex config, stat err: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if owner := cfg.Agents["codex"].Integration.Owner; owner != config.IntegrationOwnerCodexPlugin {
		t.Fatalf("Codex integration owner = %q, want %q", owner, config.IntegrationOwnerCodexPlugin)
	}
}

func TestCLIHookSourceMatchesConfiguredCodexOwner(t *testing.T) {
	cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	event := facade.HookEvent{Target: "codex", Event: "user_input"}
	if !hookSourceAllowed(cfg, event) {
		t.Fatal("paxm-owned Codex hooks should be allowed without a plugin marker")
	}
	codex := cfg.Agents["codex"]
	codex.Integration.Owner = config.IntegrationOwnerCodexPlugin
	cfg.Agents["codex"] = codex
	if hookSourceAllowed(cfg, event) {
		t.Fatal("legacy paxm hook should be ignored after plugin ownership is configured")
	}
	t.Setenv("PAXM_INTEGRATION_OWNER", config.IntegrationOwnerCodexPlugin)
	if !hookSourceAllowed(cfg, event) {
		t.Fatal("plugin hook should be allowed after plugin ownership is configured")
	}
}

func TestCLILogsTailHumanAndJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	recorder := telemetry.NewRecorder(cfg.Telemetry, configPath)
	if err := recorder.Record(telemetry.Event{
		Time:        time.Date(2026, 7, 10, 10, 1, 0, 0, time.UTC),
		Kind:        "recall",
		Source:      "cli",
		Command:     "recall",
		Profile:     "default",
		Success:     true,
		HitCount:    2,
		DurationMS:  12,
		QueryLength: 18,
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main([]string{"--config", configPath, "logs", "--tail", "1"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("logs failed with code %d: %s", code, stderr.String())
	}
	for _, want := range []string{"2026-07-10T10:01:00Z", "OK", "recall", "command=recall", "hits=2", "duration_ms=12"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("human logs missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "logs", "--tail", "1", "--json"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("JSON logs failed with code %d: %s", code, stderr.String())
	}
	var event telemetry.Event
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil {
		t.Fatalf("decode JSON log: %v: %s", err, stdout.String())
	}
	if event.Kind != "recall" || event.HitCount != 2 {
		t.Fatalf("unexpected JSON log event: %#v", event)
	}
}

func TestCLIMCPServeInitialize(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	code := Main([]string{"--config", configPath, "mcp", "serve"}, input, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("mcp serve failed with code %d: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"serverInfo"`) || !strings.Contains(stdout.String(), `"tools"`) {
		t.Fatalf("unexpected mcp initialize response: %s", stdout.String())
	}
}

func TestCLISetupInteractiveProviderChoices(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n3\n1\nnone\n")
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

func TestCLISetupInstallsPiHookExtension(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	piAgentDir := filepath.Join(t.TempDir(), "pi-agent")
	t.Setenv("PAXM_PI_AGENT_DIR", piAgentDir)
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n1\n1\n3\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	output := stdout.String()
	if strings.Count(output, "installed hook shim") != 2 {
		t.Fatalf("pi setup should install two hook shims: %s", output)
	}
	if !strings.Contains(output, "pi-user_input") {
		t.Fatalf("pi setup did not install user_input shim: %s", output)
	}
	if !strings.Contains(output, "pi-turn_end") {
		t.Fatalf("pi setup did not install turn_end shim: %s", output)
	}
	if strings.Contains(output, "registered Codex global hook") {
		t.Fatalf("pi setup should not register Codex: %s", output)
	}
	if !strings.Contains(output, "registered Pi agent extension") {
		t.Fatalf("pi setup did not report extension registration: %s", output)
	}

	extensionPath := filepath.Join(piAgentDir, "extensions", "paxm-hook", "index.ts")
	extension, err := os.ReadFile(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	extensionText := string(extension)
	for _, expected := range []string{
		`pi.on("before_agent_start"`,
		`onRuntimeEvent("message_end"`,
		`onRuntimeEvent("turn_end"`,
		`onRuntimeEvent("session_shutdown"`,
		`schema_version: "paxm.pi.user_input.v1"`,
		`schema_version: "paxm.pi.turn_end.v1"`,
		`target: "pi"`,
		`event: "user_input"`,
		`event: "turn_end"`,
		`customType: "paxm-memory-recall"`,
		`pi-user_input`,
		`pi-turn_end`,
	} {
		if !strings.Contains(extensionText, expected) {
			t.Fatalf("pi extension missing %q: %s", expected, extensionText)
		}
	}
	if strings.Contains(extensionText, "raw_event") {
		t.Fatalf("pi extension should not forward raw runtime events into paxm payloads: %s", extensionText)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agents["pi"].Hooks["user_input"].Recall.Enabled {
		t.Fatalf("pi user_input recall should be enabled: %#v", cfg.Agents["pi"])
	}
	if !cfg.Agents["pi"].Hooks["turn_end"].Write.Enabled || !cfg.Agents["pi"].Hooks["turn_end"].Write.Buffer.Flush {
		t.Fatalf("pi turn_end write should be enabled and flush buffered writes: %#v", cfg.Agents["pi"].Hooks["turn_end"])
	}
	if !cfg.Agents["pi"].Enabled {
		t.Fatalf("pi agent should be enabled: %#v", cfg.Agents["pi"])
	}
	if cfg.Agents["pi"].PassiveWriteStartedAt == "" {
		t.Fatalf("pi integration time should be recorded for historical backfill: %#v", cfg.Agents["pi"])
	}
	if cfg.Agents["codex"].Enabled {
		t.Fatalf("codex agent should be disabled when only pi is selected: %#v", cfg.Agents["codex"])
	}
}

func TestCLISetupInstallsClaudeCodeHooks(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	claudeSettingsPath := filepath.Join(t.TempDir(), "claude", "settings.json")
	t.Setenv("PAXM_CLAUDE_SETTINGS", claudeSettingsPath)
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n1\n1\n2\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	output := stdout.String()
	if strings.Count(output, "installed hook shim") != 3 {
		t.Fatalf("Claude Code setup should install three hook shims: %s", output)
	}
	if !strings.Contains(output, "registered Claude Code global hook") {
		t.Fatalf("setup did not report Claude Code registration: %s", output)
	}
	if strings.Contains(output, "registered Codex global hook") || strings.Contains(output, "registered Pi agent extension") {
		t.Fatalf("Claude-only setup registered another agent: %s", output)
	}

	settingsBytes, err := os.ReadFile(claudeSettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := string(settingsBytes)
	if _, err := os.Stat(claudeSettingsPath + ".paxm.bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new Claude Code settings should not create a backup, stat err: %v", err)
	}
	for _, expected := range []string{
		`"SessionStart"`,
		`"UserPromptSubmit"`,
		`"Stop"`,
		`claude-session_start`,
		`claude-user_input`,
		`claude-turn_end`,
		`"timeout": 60`,
	} {
		if !strings.Contains(settings, expected) {
			t.Fatalf("Claude Code settings missing %q: %s", expected, settings)
		}
	}
	for _, event := range []string{"session_start", "user_input", "turn_end"} {
		shimPath := filepath.Join(filepath.Dir(configPath), "hooks", "claude-"+event)
		shimBytes, err := os.ReadFile(shimPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(shimBytes), " --json") {
			t.Fatalf("Claude Code shim should emit plain context: %s", shimBytes)
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agents["claude"].Enabled || !cfg.Agents["claude"].Hooks["user_input"].Recall.Enabled {
		t.Fatalf("Claude Code hooks should be enabled: %#v", cfg.Agents["claude"])
	}
	if cfg.Agents["codex"].Enabled || cfg.Agents["pi"].Enabled {
		t.Fatalf("only Claude Code should be enabled: %#v", cfg.Agents)
	}
}

func TestCLISetupConfiguresSelectedAgentsInOrder(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CLAUDE_SETTINGS", filepath.Join(t.TempDir(), "claude", "settings.json"))
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader(strings.Join([]string{
		"1",                     // sqlite
		"/custom/memory.sqlite", // path
		"1",                     // read/write
		"1",                     // required
		"1,2",                   // codex and claude
		"1",                     // codex: recall only
		"passive",               // codex recall profile
		"passive_initial",       // codex initial profile
		"2",                     // claude: write only
		"default",               // claude write profile
		"3",                     // claude turn_end only
		"y",                     // apply
	}, "\n") + "\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	output := stdout.String()
	codexIndex := strings.Index(output, "Configure Codex (1/2)")
	claudeIndex := strings.Index(output, "Configure Claude Code (2/2)")
	if codexIndex == -1 || claudeIndex == -1 || codexIndex > claudeIndex {
		t.Fatalf("agents were not configured in stable order: %s", output)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	codex := cfg.Agents["codex"]
	if !codex.Enabled || !codex.Hooks["user_input"].Recall.Enabled {
		t.Fatalf("codex recall should be enabled: %#v", codex)
	}
	for eventName, hook := range codex.Hooks {
		if hook.Write.Enabled {
			t.Fatalf("codex write hook %s should be disabled: %#v", eventName, hook)
		}
	}
	claude := cfg.Agents["claude"]
	if !claude.Enabled || claude.Hooks["user_input"].Recall.Enabled {
		t.Fatalf("claude should be write-only: %#v", claude)
	}
	for eventName, hook := range claude.Hooks {
		wantEnabled := eventName == "turn_end"
		if hook.Write.Enabled != wantEnabled {
			t.Fatalf("claude write hook %s enabled=%t, want %t", eventName, hook.Write.Enabled, wantEnabled)
		}
	}
	hooksDir := filepath.Join(filepath.Dir(configPath), "hooks")
	for _, path := range []string{
		filepath.Join(hooksDir, "codex-user_input"),
		filepath.Join(hooksDir, "claude-turn_end"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("enabled hook shim missing: %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(hooksDir, "codex-session_start"),
		filepath.Join(hooksDir, "codex-turn_end"),
		filepath.Join(hooksDir, "claude-session_start"),
		filepath.Join(hooksDir, "claude-user_input"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("disabled hook shim should not exist: %s (stat err: %v)", path, err)
		}
	}
}

func TestCLIUninstallRemovesOnlySelectedAgentAndIsIdempotent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	claudeSettingsPath := filepath.Join(t.TempDir(), "claude", "settings.json")
	codexConfigPath := filepath.Join(t.TempDir(), "codex.toml")
	t.Setenv("PAXM_CLAUDE_SETTINGS", claudeSettingsPath)
	t.Setenv("PAXM_CODEX_CONFIG", codexConfigPath)
	if err := os.MkdirAll(filepath.Dir(claudeSettingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existingClaudeHook := `{
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "/tmp/existing-claude-hook"}]}
    ]
  }
}
`
	if err := os.WriteFile(claudeSettingsPath, []byte(existingClaudeHook), 0o600); err != nil {
		t.Fatal(err)
	}

	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n1\n1\n1,2\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	for _, path := range []string{hookSessionStatePath(configPath), hookSocketPath(configPath)} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "uninstall", "--agent", "claude", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "uninstalled Claude Code passive integration") {
		t.Fatalf("unexpected uninstall output: %s", stdout.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents["claude"].Enabled {
		t.Fatalf("claude should be disabled: %#v", cfg.Agents["claude"])
	}
	if !cfg.Agents["codex"].Enabled {
		t.Fatalf("codex should remain enabled: %#v", cfg.Agents["codex"])
	}
	settingsBytes, err := os.ReadFile(claudeSettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := string(settingsBytes)
	if !strings.Contains(settings, "/tmp/existing-claude-hook") || strings.Contains(settings, "/hooks/claude-") {
		t.Fatalf("Claude settings were not cleaned selectively: %s", settings)
	}
	for _, event := range installedHookEvents() {
		shimPath := filepath.Join(filepath.Dir(configPath), "hooks", "claude-"+event.ConfigEvent)
		if _, err := os.Stat(shimPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Claude shim still exists: %s (stat err: %v)", shimPath, err)
		}
		codexShimPath := filepath.Join(filepath.Dir(configPath), "hooks", "codex-"+event.ConfigEvent)
		if _, err := os.Stat(codexShimPath); err != nil {
			t.Fatalf("Codex shim should remain: %s: %v", codexShimPath, err)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "uninstall", "--agent", "claude", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("idempotent uninstall failed with code %d: %s", code, stderr.String())
	}
}

func TestCLIUninstallRemovesAllPassiveIntegrations(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	claudeSettingsPath := filepath.Join(t.TempDir(), "claude", "settings.json")
	codexConfigPath := filepath.Join(t.TempDir(), "codex.toml")
	piAgentDir := filepath.Join(t.TempDir(), "pi-agent")
	t.Setenv("PAXM_CLAUDE_SETTINGS", claudeSettingsPath)
	t.Setenv("PAXM_CODEX_CONFIG", codexConfigPath)
	t.Setenv("PAXM_PI_AGENT_DIR", piAgentDir)

	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n1\n1\nall\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	hooksDir := filepath.Join(filepath.Dir(configPath), "hooks")
	for _, path := range []string{hookSessionStatePath(configPath), hookSocketPath(configPath)} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "uninstall", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("uninstall failed with code %d: %s", code, stderr.String())
	}
	for _, name := range []string{"Codex", "Claude Code", "Pi"} {
		if !strings.Contains(stdout.String(), "uninstalled "+name+" passive integration") {
			t.Fatalf("uninstall output missing %s: %s", name, stdout.String())
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, agent := range cfg.Agents {
		if agent.Enabled {
			t.Fatalf("agent %s should be disabled: %#v", name, agent)
		}
	}
	for _, target := range []string{"codex", "claude", "pi"} {
		for _, event := range installedHookEvents() {
			path := filepath.Join(hooksDir, target+"-"+event.ConfigEvent)
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hook shim still exists: %s (stat err: %v)", path, err)
			}
		}
	}
	for _, path := range []string{codexConfigPath, claudeSettingsPath} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "/hooks/codex-") || strings.Contains(string(content), "/hooks/claude-") {
			t.Fatalf("agent config still contains paxm hook: %s", content)
		}
	}
	if _, err := os.Stat(filepath.Join(piAgentDir, "extensions", "paxm-hook")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Pi extension still exists, stat err: %v", err)
	}
	if _, err := os.Stat(hooksDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared hook state directory still exists, stat err: %v", err)
	}
}

func TestInternalHookDoesNotBufferWhenHookWriteDisabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{
		Type:    "sqlite",
		Enabled: true,
		Path:    filepath.Join(t.TempDir(), "memory.sqlite"),
	}
	pi := cfg.Agents["pi"]
	pi.Enabled = true
	hook := pi.Hooks["user_input"]
	hook.Recall.Enabled = true
	hook.Write.Enabled = false
	pi.Hooks["user_input"] = hook
	cfg.Agents["pi"] = pi
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	event := strings.NewReader(`{"prompt":"recall only","workspace":"/tmp/project"}`)
	code := Main([]string{"--config", configPath, "__hook", "--target", "pi", "--event", "user_input", "--json"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook failed with code %d: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "hook buffer skipped") {
		t.Fatalf("recall-only hook should not touch buffer: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"target": "pi"`) {
		t.Fatalf("unexpected hook output: %s", stdout.String())
	}
}

func TestDecodeHookEventExtractsSafeWriteFields(t *testing.T) {
	event, err := decodeHookEvent([]byte(`{
		"session_id": "volatile-session",
		"last_assistant_message": "Final answer only.",
		"messages": [
			{"role": "user", "text": "visible prompt"},
			{"role": "assistant", "text": "visible answer"},
			{"role": "tool", "text": "tool output"}
		],
		"tool_use": {"name": "Read"},
		"thinking": "private"
	}`), "claude", "turn_end")
	if err != nil {
		t.Fatal(err)
	}
	if event.Assistant != "Final answer only." {
		t.Fatalf("assistant = %q", event.Assistant)
	}
	if len(event.Messages) != 3 || event.Messages[0].Text != "visible prompt" || event.Messages[2].Role != "tool" {
		t.Fatalf("messages not decoded: %#v", event.Messages)
	}
	if event.Metadata["session_id"] != "volatile-session" {
		t.Fatalf("metadata not preserved: %#v", event.Metadata)
	}
	if strings.TrimSpace(string(event.Raw)) == "" {
		t.Fatal("raw event should remain available for explicit custom templates")
	}
}

func TestHookBufferShutdownFlushesPendingItems(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	provider := cfg.Providers["sqlite"]
	provider.Path = filepath.Join(t.TempDir(), "memory.sqlite")
	cfg.Providers["sqlite"] = provider
	router, err := adapters.DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := facade.New(cfg, router)
	buffer := []facade.IngestInput{{
		Text:    "shutdown flush sentinel",
		Profile: "default",
		Source:  "test",
	}}
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})
	type result struct {
		flushed  int
		shutdown bool
		err      error
	}
	resultCh := make(chan result, 1)
	cleanupScheduled := make(chan struct{}, 1)
	go func() {
		flushed, shutdown, err := handleHookBufferConn(context.Background(), service, serverConn, &buffer, func() {
			cleanupScheduled <- struct{}{}
		})
		resultCh <- result{flushed: flushed, shutdown: shutdown, err: err}
	}()
	if err := writeJSON(clientConn, hookBufferRequest{Action: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	var response hookBufferResponse
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	got := <-resultCh
	if got.err != nil || !got.shutdown || got.flushed != 1 || !response.OK || response.Flushed != 1 {
		t.Fatalf("unexpected shutdown result: result=%#v response=%#v", got, response)
	}
	if len(buffer) != 0 {
		t.Fatalf("buffer was not cleared: %#v", buffer)
	}
	select {
	case <-cleanupScheduled:
	default:
		t.Fatal("shutdown flush did not schedule cleanup")
	}
	recalled, err := service.Recall(context.Background(), facade.RecallInput{Query: "shutdown flush sentinel", Profile: "default", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled.Hits) == 0 || !strings.Contains(recalled.Hits[0].Text, "shutdown flush sentinel") {
		t.Fatalf("flushed item was not persisted: %#v", recalled.Hits)
	}
}

func TestHookCleanupWorkerSchedulesWithoutBlockingAndDrainsOnClose(t *testing.T) {
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	cleanupFinished := make(chan struct{})
	worker := newHookCleanupWorker(func(context.Context) {
		close(cleanupStarted)
		<-releaseCleanup
		close(cleanupFinished)
	})

	scheduled := make(chan struct{})
	go func() {
		worker.Schedule()
		close(scheduled)
	}()
	select {
	case <-scheduled:
	case <-time.After(time.Second):
		t.Fatal("cleanup scheduling blocked the hook path")
	}
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled cleanup did not start")
	}

	closed := make(chan struct{})
	go func() {
		worker.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("worker closed before scheduled cleanup completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCleanup)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("worker did not close after cleanup completed")
	}
	select {
	case <-cleanupFinished:
	default:
		t.Fatal("worker close did not drain scheduled cleanup")
	}
}

func TestInitialUserInputRecallStateOnlyMarksFirstSessionInput(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	r := runner{configPath: configPath, stderr: &bytes.Buffer{}}

	first := r.markInitialUserInputRecall(cfg, facade.HookEvent{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-a",
		},
	})
	if first.Metadata[facade.HookRecallPhaseMetadataKey] != facade.HookRecallPhaseInitial {
		t.Fatalf("first user_input should use initial recall: %#v", first.Metadata)
	}

	second := r.markInitialUserInputRecall(cfg, facade.HookEvent{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-a",
		},
	})
	if second.Metadata[facade.HookRecallPhaseMetadataKey] != "" {
		t.Fatalf("second user_input should stay strict: %#v", second.Metadata)
	}

	nextSession := r.markInitialUserInputRecall(cfg, facade.HookEvent{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-b",
		},
	})
	if nextSession.Metadata[facade.HookRecallPhaseMetadataKey] != facade.HookRecallPhaseInitial {
		t.Fatalf("new session should use initial recall: %#v", nextSession.Metadata)
	}
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
	if cfg.Providers["sqlite"].Enabled {
		t.Fatalf("sqlite should be disabled when only zep was selected: %#v", cfg.Providers)
	}
	zep := cfg.Providers["zep"]
	if !zep.Enabled || zep.APIKey != "zep-key" || zep.GraphID != "graph-1" || zep.UserID != "" || zep.SearchScope != "edges" {
		t.Fatalf("unexpected zep provider config: %#v", zep)
	}
	recallRoutes := cfg.RecallProfiles["default"].Providers
	if len(recallRoutes) != 1 || recallRoutes[0].Name != "zep" || recallRoutes[0].Required {
		t.Fatalf("unexpected recall routes: %#v", recallRoutes)
	}
	passiveRoutes := cfg.RecallProfiles["passive"].Providers
	if len(passiveRoutes) != 1 || passiveRoutes[0].Name != "zep" || passiveRoutes[0].Required {
		t.Fatalf("unexpected passive recall routes: %#v", passiveRoutes)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "zep" || writeRoutes[0].Required {
		t.Fatalf("unexpected write routes: %#v", writeRoutes)
	}
	if strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup installed hook despite none selection: %s", stdout.String())
	}
}

func TestCLISetupInteractiveMem0Provider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("3\nhttp://mem0.local:8888\nmem0-key\n1\ntoddzheng\n1\n2\nnone\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["sqlite"].Enabled || cfg.Providers["zep"].Enabled {
		t.Fatalf("only mem0 should be enabled: %#v", cfg.Providers)
	}
	mem0 := cfg.Providers["mem0"]
	if !mem0.Enabled || mem0.APIKey != "mem0-key" || mem0.BaseURL != "http://mem0.local:8888" || mem0.UserID != "toddzheng" {
		t.Fatalf("unexpected mem0 provider config: %#v", mem0)
	}
	recallRoutes := cfg.RecallProfiles["default"].Providers
	if len(recallRoutes) != 1 || recallRoutes[0].Name != "mem0" || recallRoutes[0].Required {
		t.Fatalf("unexpected recall routes: %#v", recallRoutes)
	}
	passiveRoutes := cfg.RecallProfiles["passive"].Providers
	if len(passiveRoutes) != 1 || passiveRoutes[0].Name != "mem0" || passiveRoutes[0].Required {
		t.Fatalf("unexpected passive recall routes: %#v", passiveRoutes)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "mem0" || writeRoutes[0].Required {
		t.Fatalf("unexpected write routes: %#v", writeRoutes)
	}
	if strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup installed hook despite none selection: %s", stdout.String())
	}
}

func TestCLISetupInteractiveJSONRPCProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	setupInput := strings.NewReader("4\n1\n/opt/paxm/plugins/corp-memory\n--config /etc/corp-memory.yaml\n15s\n1\n2\nnone\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["sqlite"].Enabled || cfg.Providers["zep"].Enabled || cfg.Providers["mem0"].Enabled {
		t.Fatalf("only jsonrpc should be enabled: %#v", cfg.Providers)
	}
	provider := cfg.Providers["jsonrpc"]
	if !provider.Enabled || provider.Transport != "stdio" || provider.Command != "/opt/paxm/plugins/corp-memory" || provider.Timeout != "15s" {
		t.Fatalf("unexpected jsonrpc provider config: %#v", provider)
	}
	if len(provider.Args) != 2 || provider.Args[0] != "--config" || provider.Args[1] != "/etc/corp-memory.yaml" {
		t.Fatalf("unexpected jsonrpc args: %#v", provider.Args)
	}
	recallRoutes := cfg.RecallProfiles["default"].Providers
	if len(recallRoutes) != 1 || recallRoutes[0].Name != "jsonrpc" || recallRoutes[0].Required {
		t.Fatalf("unexpected recall routes: %#v", recallRoutes)
	}
	writeRoutes := cfg.WriteProfiles["default"].Providers
	if len(writeRoutes) != 1 || writeRoutes[0].Name != "jsonrpc" || writeRoutes[0].Required {
		t.Fatalf("unexpected write routes: %#v", writeRoutes)
	}
	if strings.Contains(stdout.String(), "installed hook shim") {
		t.Fatalf("setup installed hook despite none selection: %s", stdout.String())
	}
}

func TestCLISetupEnsuresZepUserTarget(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var ensured config.ProviderConfig
	deps := Dependencies{
		EnsureZepUser: func(_ context.Context, cfg config.ProviderConfig) (zepadapter.EnsureUserResult, error) {
			ensured = cfg
			return zepadapter.EnsureUserResult{UserID: cfg.UserID, Created: true}, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	setupInput := strings.NewReader("2\nzep-key\n1\ntoddzheng\n6\n1\n2\nnone\n")
	if code := MainWithDependencies([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	if ensured.UserID != "toddzheng" || ensured.APIKey != "zep-key" || ensured.GraphID != "" {
		t.Fatalf("unexpected ensured zep config: %#v", ensured)
	}
	if !strings.Contains(stdout.String(), "ensured Zep user: toddzheng (created)") {
		t.Fatalf("setup did not report ensured Zep user: %s", stdout.String())
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
	if userInput.Recall.Profile != "passive" || userInput.Recall.MaxResults != 2 {
		t.Fatalf("legacy user prompt hook did not move to passive recall: %#v", userInput.Recall)
	}
	if userInput.Recall.Insertion.MinScore != 0.8 || userInput.Recall.Insertion.MaxItems != 2 || !userInput.Recall.Insertion.RequireQueryTerms {
		t.Fatalf("legacy user prompt hook did not receive passive insertion policy: %#v", userInput.Recall.Insertion)
	}
	if !cfg.Agents["codex"].Hooks["turn_end"].Write.Buffer.Flush {
		t.Fatalf("turn_end flush default was not merged: %#v", cfg.Agents["codex"].Hooks["turn_end"])
	}
}

func TestSetupBaseConfigMigratesOldDefaultRecallLimit(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	profile := cfg.RecallProfiles["default"]
	profile.MaxResults = 8
	cfg.RecallProfiles["default"] = profile
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	merged, err := setupBaseConfig(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if merged.RecallProfiles["default"].MaxResults != 3 {
		t.Fatalf("old default recall limit should migrate to 3: %#v", merged.RecallProfiles["default"])
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

func TestWriteRecallMarkdownShowsScores(t *testing.T) {
	t.Parallel()

	rawScore := 0.42
	var stdout bytes.Buffer
	writeRecallMarkdown(&stdout, facade.RecallResult{
		Hits: []memory.MemoryHit{
			{
				Provider:     "sqlite",
				Text:         "Todd memory",
				Score:        0.87654,
				Relevance:    0.76543,
				RawScore:     &rawScore,
				RawScoreKind: "keyword_ratio",
				Source:       "cli",
			},
		},
	})
	output := stdout.String()
	for _, expected := range []string{
		"Score: 0.8765",
		"Relevance: 0.7654",
		"Raw score: 0.4200 (keyword_ratio)",
		"Source: cli",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("recall markdown missing %q: %s", expected, output)
		}
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

func TestCLISetupCancellationDoesNotWriteFiles(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	setupInput := strings.NewReader("1\n/custom/memory.sqlite\n1\n1\nnone\nn\n")

	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("cancelled setup failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "setup cancelled") {
		t.Fatalf("setup did not report cancellation: %s", stdout.String())
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled setup wrote config, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(configPath), "hooks")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled setup wrote hooks, stat err: %v", err)
	}
}

func TestCLISetupReusesDisabledAgentPassiveChoices(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	claude := cfg.Agents["claude"]
	claude.Enabled = false
	userInput := claude.Hooks["user_input"]
	userInput.Recall.Enabled = false
	userInput.Write.Enabled = false
	claude.Hooks["user_input"] = userInput
	sessionStart := claude.Hooks["session_start"]
	sessionStart.Write.Enabled = false
	claude.Hooks["session_start"] = sessionStart
	turnEnd := claude.Hooks["turn_end"]
	turnEnd.Write.Enabled = true
	claude.Hooks["turn_end"] = turnEnd
	cfg.Agents["claude"] = claude
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAXM_CLAUDE_SETTINGS", filepath.Join(t.TempDir(), "claude", "settings.json"))
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))

	setupInput := strings.NewReader("\n\n\n\n2\n\n\n\n\ny\n")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"--config", configPath, "setup", "--force"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	updated, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	claude = updated.Agents["claude"]
	if !claude.Enabled || claude.Hooks["user_input"].Recall.Enabled {
		t.Fatalf("Claude passive behavior was not preserved: %#v", claude)
	}
	for eventName, hook := range claude.Hooks {
		wantEnabled := eventName == "turn_end"
		if hook.Write.Enabled != wantEnabled {
			t.Fatalf("Claude write event %s enabled=%t, want %t", eventName, hook.Write.Enabled, wantEnabled)
		}
	}
}

func assertWriteOnlyConfig(t *testing.T, configPath string) {
	t.Helper()

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlite := cfg.Providers["sqlite"]
	if sqlite.Type != "sqlite" {
		t.Fatalf("unexpected sqlite type: %q", sqlite.Type)
	}
	if sqlite.Path != "/custom/memory.sqlite" {
		t.Fatalf("unexpected sqlite path: %q", sqlite.Path)
	}
	if recallHasProvider(cfg, "sqlite") {
		t.Fatalf("sqlite should not be in default recall profile: %#v", cfg.RecallProfiles["default"])
	}
	if recallProfileHasProvider(cfg.RecallProfiles["passive"], "sqlite") {
		t.Fatalf("sqlite should not be in passive recall profile: %#v", cfg.RecallProfiles["passive"])
	}
	writeProfile := cfg.WriteProfiles["default"]
	if len(writeProfile.Providers) != 1 || writeProfile.Providers[0].Name != "sqlite" || !writeProfile.Providers[0].Required {
		t.Fatalf("unexpected default write profile: %#v", writeProfile)
	}
	if cfg.Agents["claude"].Enabled || cfg.Agents["codex"].Enabled || cfg.Agents["pi"].Enabled {
		t.Fatalf("agents should be disabled when no hooks are selected: %#v", cfg.Agents)
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

func TestInstallClaudeGlobalHooksPreservesExistingHooksAndIsIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "/tmp/existing-hook"}]}
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	scriptPaths := make(map[string]string)
	for _, event := range installedHookEvents() {
		scriptPaths[event.ConfigEvent] = filepath.Join(t.TempDir(), "claude-"+event.ConfigEvent)
	}
	if err := installClaudeGlobalHooks(path, scriptPaths); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeGlobalHooks(path, scriptPaths); err != nil {
		t.Fatal(err)
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)
	for _, expected := range []string{
		`"permissions"`,
		`Bash(go test:*)`,
		`/tmp/existing-hook`,
		`"SessionStart"`,
		`"UserPromptSubmit"`,
		`"Stop"`,
		`"matcher": "startup|resume|clear|compact"`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Claude Code settings missing %q: %s", expected, content)
		}
	}
	for _, event := range installedHookEvents() {
		if count := strings.Count(content, "claude-"+event.ConfigEvent); count != 1 {
			t.Fatalf("Claude Code hook %s installed %d times: %s", event.ConfigEvent, count, content)
		}
	}
	backup, err := os.ReadFile(path + ".paxm.bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatalf("Claude Code backup changed:\n%s", backup)
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
	if !strings.Contains(stdout.String(), "uninstall [--agent AGENT] [--yes]") {
		t.Fatalf("uninstall command missing from help: %s", stdout.String())
	}
}

func TestCLIVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Main([]string{"version"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("version failed with code %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("version output was empty")
	}
}

func TestSetupOptionHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("provider and agent options", func(t *testing.T) {
		cfg := config.Config{
			Providers: map[string]config.ProviderConfig{
				"custom": {Type: "custom"},
				"mem":    {Type: "mem0"},
				"rpc":    {Type: "jsonrpc"},
				"sqlite": {Type: "sqlite"},
				"zed":    {Type: "zep"},
			},
			Agents: map[string]config.AgentConfig{
				"other":  {Enabled: true},
				"pi":     {Enabled: true},
				"codex":  {Enabled: true},
				"claude": {Enabled: true},
			},
		}
		if got, want := providerOptionIDs(cfg), []string{"sqlite", "zed", "mem", "rpc", "custom"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("providerOptionIDs() = %#v, want %#v", got, want)
		}
		wantHooks := []setupOption{
			{ID: "codex", Label: "Codex"},
			{ID: "claude", Label: "Claude Code"},
			{ID: "pi", Label: "Pi"},
			{ID: "other", Label: "other"},
		}
		if got := hookOptions(cfg); !reflect.DeepEqual(got, wantHooks) {
			t.Fatalf("hookOptions() = %#v, want %#v", got, wantHooks)
		}
	})

	t.Run("labels and priorities", func(t *testing.T) {
		tests := []struct {
			name         string
			providerName string
			provider     config.ProviderConfig
			wantLabel    string
			wantPriority int
		}{
			{name: "sqlite default", providerName: "sqlite", provider: config.ProviderConfig{Type: "sqlite"}, wantLabel: "SQLite", wantPriority: 0},
			{name: "named zep", providerName: "team", provider: config.ProviderConfig{Type: "zep"}, wantLabel: "team (Zep)", wantPriority: 1},
			{name: "named mem0", providerName: "company", provider: config.ProviderConfig{Type: "mem0"}, wantLabel: "company (Mem0)", wantPriority: 2},
			{name: "jsonrpc default", providerName: "jsonrpc", provider: config.ProviderConfig{Type: "jsonrpc"}, wantLabel: "JSON-RPC", wantPriority: 3},
			{name: "unknown", providerName: "other", provider: config.ProviderConfig{Type: "other"}, wantLabel: "other", wantPriority: 100},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := providerPromptLabel(tt.providerName, tt.provider); got != tt.wantLabel {
					t.Fatalf("providerPromptLabel() = %q, want %q", got, tt.wantLabel)
				}
				if got := providerOptionPriority(tt.provider.Type); got != tt.wantPriority {
					t.Fatalf("providerOptionPriority() = %d, want %d", got, tt.wantPriority)
				}
			})
		}
	})

	t.Run("provider routing modes", func(t *testing.T) {
		tests := []struct {
			name       string
			mode       string
			required   bool
			wantMode   string
			wantPolicy string
			wantRead   bool
			wantWrite  bool
		}{
			{name: "read write required", mode: "read_write", required: true, wantMode: "read_write", wantPolicy: "required", wantRead: true, wantWrite: true},
			{name: "read only best effort", mode: "read_only", required: false, wantMode: "read_only", wantPolicy: "best_effort", wantRead: true},
			{name: "write only required", mode: "write_only", required: true, wantMode: "write_only", wantPolicy: "required", wantWrite: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
				cfg.Providers["archive"] = config.ProviderConfig{Type: "sqlite", Enabled: true}
				removeProviderFromDefaultProfiles(&cfg, "archive")
				setDefaultProviderMode(&cfg, "archive", tt.mode, tt.required)
				if got := currentProviderMode(cfg, "archive"); got != tt.wantMode {
					t.Fatalf("currentProviderMode() = %q, want %q", got, tt.wantMode)
				}
				if got := currentProviderPolicy(cfg, "archive"); got != tt.wantPolicy {
					t.Fatalf("currentProviderPolicy() = %q, want %q", got, tt.wantPolicy)
				}
				if got := recallProfileHasProvider(cfg.RecallProfiles["default"], "archive"); got != tt.wantRead {
					t.Fatalf("read route = %v, want %v", got, tt.wantRead)
				}
				if got := writeProfileHasProvider(cfg.WriteProfiles["default"], "archive"); got != tt.wantWrite {
					t.Fatalf("write route = %v, want %v", got, tt.wantWrite)
				}
			})
		}
	})

	t.Run("selection helpers", func(t *testing.T) {
		options := []setupOption{{ID: "b"}, {ID: "a"}}
		defaults := map[string]bool{"a": true, "ignored": true}
		if got, want := defaultSelections(options, defaults), map[string]bool{"a": true, "b": false}; !reflect.DeepEqual(got, want) {
			t.Fatalf("defaultSelections() = %#v, want %#v", got, want)
		}
		if !anySelected(map[string]bool{"a": false, "b": true}) || anySelected(map[string]bool{"a": false}) {
			t.Fatal("anySelected returned unexpected result")
		}
		if got, want := sortedSelected(map[string]bool{"b": true, "a": false}), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("sortedSelected() = %#v, want %#v", got, want)
		}
		if got := boolInt(true); got != 1 {
			t.Fatalf("boolInt(true) = %d", got)
		}
		if got := boolInt(false); got != 0 {
			t.Fatalf("boolInt(false) = %d", got)
		}
	})
}

func TestPromptParserHelpersTable(t *testing.T) {
	t.Parallel()

	options := []setupOption{{ID: "one", Label: "One"}, {ID: "two", Label: "Two"}}

	t.Run("bool", func(t *testing.T) {
		tests := []struct {
			name    string
			input   string
			def     bool
			want    bool
			wantOut string
		}{
			{name: "yes", input: "y\n", want: true},
			{name: "no", input: "no\n", def: true, want: false},
			{name: "default", input: "\n", def: true, want: true},
			{name: "invalid then eof returns default", input: "maybe", want: false, wantOut: "Please answer yes or no."},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var out bytes.Buffer
				got, err := promptBool(bufio.NewReader(strings.NewReader(tt.input)), &out, "Continue?", tt.def)
				if err != nil {
					t.Fatalf("promptBool() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("promptBool() = %v, want %v", got, tt.want)
				}
				if tt.wantOut != "" && !strings.Contains(out.String(), tt.wantOut) {
					t.Fatalf("prompt output missing %q: %s", tt.wantOut, out.String())
				}
			})
		}
	})

	t.Run("single select", func(t *testing.T) {
		tests := []struct {
			name    string
			input   string
			def     string
			want    string
			wantErr string
		}{
			{name: "default", input: "\n", def: "two", want: "two"},
			{name: "number", input: "1\n", def: "two", want: "one"},
			{name: "id", input: "two\n", def: "one", want: "two"},
			{name: "fallback default", input: "\n", def: "missing", want: "one"},
			{name: "no options", input: "\n", wantErr: "has no options"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var out bytes.Buffer
				selectOptions := options
				if tt.wantErr != "" {
					selectOptions = nil
				}
				got, err := promptSingleSelect(bufio.NewReader(strings.NewReader(tt.input)), &out, "Pick", selectOptions, tt.def)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("promptSingleSelect() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("promptSingleSelect() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("promptSingleSelect() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("multi select", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			want  map[string]bool
		}{
			{name: "defaults", input: "\n", want: map[string]bool{"one": true, "two": false}},
			{name: "all", input: "all\n", want: map[string]bool{"one": true, "two": true}},
			{name: "none", input: "none\n", want: map[string]bool{"one": false, "two": false}},
			{name: "numbers", input: "2\n", want: map[string]bool{"one": false, "two": true}},
			{name: "invalid then eof defaults", input: "9", want: map[string]bool{"one": true, "two": false}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var out bytes.Buffer
				got, err := promptMultiSelect(bufio.NewReader(strings.NewReader(tt.input)), &out, "Pick many", options, map[string]bool{"one": true})
				if err != nil {
					t.Fatalf("promptMultiSelect() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("promptMultiSelect() = %#v, want %#v", got, tt.want)
				}
			})
		}
	})

	t.Run("direct helpers", func(t *testing.T) {
		selected, err := parseMultiSelect("1, 2", options)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]bool{"one": true, "two": true}; !reflect.DeepEqual(selected, want) {
			t.Fatalf("parseMultiSelect() = %#v, want %#v", selected, want)
		}
		if _, err := parseMultiSelect("bad", options); err == nil {
			t.Fatal("expected parseMultiSelect error")
		}
		if got := defaultSelectionText(options, map[string]bool{"two": true}); got != "2" {
			t.Fatalf("defaultSelectionText() = %q", got)
		}
		if got := optionIndex(options, "missing"); got != -1 {
			t.Fatalf("optionIndex() = %d", got)
		}
		if got, err := promptString(bufio.NewReader(strings.NewReader("\n")), &bytes.Buffer{}, "Path", "default"); err != nil || got != "default" {
			t.Fatalf("promptString() = %q, %v", got, err)
		}
		if got := minInt(3, 10); got != 3 {
			t.Fatalf("minInt() = %d", got)
		}
	})
}

func TestCodexTomlAndPathHelpersTable(t *testing.T) {
	t.Run("paths from environment", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(dir, "codex.toml"))
		t.Setenv("PAXM_CLAUDE_SETTINGS", filepath.Join(dir, "claude.json"))
		t.Setenv("PAXM_PI_AGENT_DIR", filepath.Join(dir, "pi-agent"))
		if got := codexConfigPath(); got != filepath.Join(dir, "codex.toml") {
			t.Fatalf("codexConfigPath() = %q", got)
		}
		if got := claudeSettingsPath(); got != filepath.Join(dir, "claude.json") {
			t.Fatalf("claudeSettingsPath() = %q", got)
		}
		if got := piAgentDir(); got != filepath.Join(dir, "pi-agent") {
			t.Fatalf("piAgentDir() = %q", got)
		}
		if got := piExtensionPath(); got != filepath.Join(dir, "pi-agent", "extensions", "paxm-hook", "index.ts") {
			t.Fatalf("piExtensionPath() = %q", got)
		}
	})

	t.Run("inline toml arrays", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want []string
		}{
			{name: "nested and quoted comma", body: `{ command = "a,b" }, { hooks = [{ command = "c" }] }`, want: []string{`{ command = "a,b" }`, `{ hooks = [{ command = "c" }] }`}},
			{name: "empty", body: " ", want: nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := splitTopLevelInlineEntries(tt.body); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("splitTopLevelInlineEntries() = %#v, want %#v", got, tt.want)
				}
			})
		}

		line := `UserPromptSubmit = [{ command = "keep" }, { command = "codex-user_prompt" }]` + "\n"
		if got := removeInlineTomlArrayEntries(line, "codex-user_prompt"); strings.Contains(got, "codex-user_prompt") || !strings.HasSuffix(got, "\n") {
			t.Fatalf("removeInlineTomlArrayEntries() = %q", got)
		}
		if got := appendInlineTomlArray("Stop = []\n", `{ command = "paxm" }`); got != "Stop = [{ command = \"paxm\" }]\n" {
			t.Fatalf("appendInlineTomlArray(empty) = %q", got)
		}
		if got := appendInlineTomlArray("Stop = [old]\n", "new"); got != "Stop = [old, new]\n" {
			t.Fatalf("appendInlineTomlArray(existing) = %q", got)
		}
	})

	t.Run("codex hook upsert and prune", func(t *testing.T) {
		tests := []struct {
			name      string
			content   string
			eventName string
			entry     string
			want      []string
		}{
			{name: "empty config", eventName: "Stop", entry: `{ command = "paxm" }`, want: []string{"[hooks]", `Stop = [{ command = "paxm" }]`}},
			{name: "append hooks section", content: "model = \"gpt\"\n", eventName: "Stop", entry: `{ command = "paxm" }`, want: []string{"model = \"gpt\"", "[hooks]", `Stop = [{ command = "paxm" }]`}},
			{name: "append existing event", content: "[hooks]\nStop = [{ command = \"old\" }]\n", eventName: "Stop", entry: `{ command = "paxm" }`, want: []string{"old", "paxm"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := upsertCodexHook(tt.content, tt.eventName, tt.entry)
				for _, want := range tt.want {
					if !strings.Contains(got, want) {
						t.Fatalf("upsertCodexHook() missing %q: %s", want, got)
					}
				}
			})
		}

		pruned, changed := pruneLegacyCodexUserPromptHook("[hooks]\nUserPromptSubmit = [{ command = \"keep\" }, { command = \"codex-user_prompt\" }]\n")
		if !changed || strings.Contains(pruned, "codex-user_prompt") {
			t.Fatalf("pruneLegacyCodexUserPromptHook() = changed %v content %q", changed, pruned)
		}
		if same, changed := pruneLegacyCodexUserPromptHook("[hooks]\nStop = []\n"); changed || same == "" {
			t.Fatalf("unexpected prune on clean config: changed=%v content=%q", changed, same)
		}
	})

	t.Run("quoting and config flags", func(t *testing.T) {
		if got := shellQuote("a'b"); got != `'a'"'"'b'` {
			t.Fatalf("shellQuote() = %q", got)
		}
		if got := shellQuote(""); got != "''" {
			t.Fatalf("shellQuote(empty) = %q", got)
		}
		if got := escapeTomlString(`a\"b`); got != `a\\\"b` {
			t.Fatalf("escapeTomlString() = %q", got)
		}
		if got := jsonStringLiteral("a\nb"); got != "\"a\\nb\"" {
			t.Fatalf("jsonStringLiteral() = %q", got)
		}
		args, cfg, err := extractConfigFlag([]string{"--config", "cfg.yaml", "recall", "--query", "x"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg != "cfg.yaml" || !reflect.DeepEqual(args, []string{"recall", "--query", "x"}) {
			t.Fatalf("extractConfigFlag() args=%#v cfg=%q", args, cfg)
		}
		args, cfg, err = extractConfigFlag([]string{"--config=inline.yaml", "version"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg != "inline.yaml" || !reflect.DeepEqual(args, []string{"version"}) {
			t.Fatalf("extractConfigFlag(inline) args=%#v cfg=%q", args, cfg)
		}
		if _, _, err := extractConfigFlag([]string{"--config"}); err == nil {
			t.Fatal("expected missing config path error")
		}
	})
}

func TestHistoryFormattingHelpersTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary telemetry.HistorySummary
		want    []string
	}{
		{
			name: "quiet history",
			summary: telemetry.HistorySummary{
				Days: 7,
				Storage: telemetry.StorageInfo{
					EventsFile:  "/tmp/events.jsonl",
					MetricsFile: "/tmp/metrics.json",
				},
			},
			want: []string{"status: quiet", "no telemetry events recorded yet", "storage"},
		},
		{
			name: "active history",
			summary: telemetry.HistorySummary{
				Days: 1,
				Totals: telemetry.Counter{
					Events:         4,
					Successes:      3,
					Errors:         1,
					Recalls:        2,
					Hits:           5,
					Inserted:       1,
					Writes:         2,
					Items:          3,
					Flushes:        1,
					ProviderErrors: 1,
				},
				Daily:      []telemetry.DatedCounter{{Date: "2026-07-09", Counter: telemetry.Counter{Recalls: 2, Hits: 5, Inserted: 1, Writes: 2, Errors: 1}}},
				Profiles:   []telemetry.NamedCounter{{Name: "default", Counter: telemetry.Counter{Recalls: 2, Hits: 5}}},
				Agents:     []telemetry.NamedCounter{{Name: "codex", Counter: telemetry.Counter{Recalls: 2, Writes: 1, Inserted: 1, Flushes: 1}}},
				HookEvents: []telemetry.NamedCounter{{Name: "codex/user_input", Counter: telemetry.Counter{Recalls: 2, Inserted: 1}}},
				Providers:  []telemetry.NamedCounter{{Name: "sqlite", Counter: telemetry.Counter{Writes: 2, Refs: 1, Recalls: 2, Hits: 5, ProviderErrors: 1}}},
				Storage: telemetry.StorageInfo{
					EventBytes: 10,
					TotalBytes: 20,
					MaxBytes:   30,
					MaxFiles:   2,
				},
			},
			want: []string{"status: attention", "overview", "recall funnel", "20.0%", "write pipeline", "50.0%", "by day", "by profile", "by agent", "by hook", "by provider"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writeHistorySummary(&out, tt.summary)
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("history output missing %q: %s", want, out.String())
				}
			}
		})
	}

	statusTests := []struct {
		counter telemetry.Counter
		want    string
	}{
		{counter: telemetry.Counter{Errors: 1}, want: "attention"},
		{counter: telemetry.Counter{ProviderErrors: 1}, want: "attention"},
		{counter: telemetry.Counter{Skipped: 1}, want: "partial"},
		{counter: telemetry.Counter{}, want: "ok"},
	}
	for _, tt := range statusTests {
		if got := historyStatus(tt.counter); got != tt.want {
			t.Fatalf("historyStatus() = %q, want %q", got, tt.want)
		}
	}
	if got := sumNamedCounters([]telemetry.NamedCounter{{Counter: telemetry.Counter{Hits: 2}}, {Counter: telemetry.Counter{Hits: 3}}}, func(counter telemetry.Counter) int { return counter.Hits }); got != 5 {
		t.Fatalf("sumNamedCounters() = %d", got)
	}
	if got := formatPercent(1, 0); got != "n/a" {
		t.Fatalf("formatPercent() = %q", got)
	}
	if got := firstNonEmpty("", " ", "x"); got != "x" {
		t.Fatalf("firstNonEmpty() = %q", got)
	}
}
