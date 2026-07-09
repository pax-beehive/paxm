package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLISetupRememberRecallAndHookEvent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
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
	if !strings.Contains(string(codexConfig), "UserPromptSubmit") || !strings.Contains(string(codexConfig), "codex-user_prompt") {
		t.Fatalf("codex config missing hook registration: %s", string(codexConfig))
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
	configPath := filepath.Join(t.TempDir(), "config.json")
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
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "config", "show"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("config show failed with code %d: %s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		`"path": "/custom/memory.jsonl"`,
		`"read": false`,
		`"write": true`,
		`"required": true`,
		`"enabled": false`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("config output missing %q: %s", want, output)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "setup", "--force", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("force setup failed with code %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "config", "show"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("config show after force failed with code %d: %s", code, stderr.String())
	}
	output = stdout.String()
	for _, want := range []string{
		`"path": "/custom/memory.jsonl"`,
		`"read": false`,
		`"write": true`,
		`"required": true`,
		`"enabled": false`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("force setup reset config; missing %q: %s", want, output)
		}
	}
}

func TestCLISetupRequiresAProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
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

func TestInstallCodexGlobalHookPreservesExistingHooks(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	original := `[hooks]
UserPromptSubmit = [{ hooks = [{ type = "command", command = "paxl __agent-hook", async = false }] }]

[hooks.state]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(t.TempDir(), "codex-user_prompt")
	if err := installCodexGlobalHook(path, scriptPath); err != nil {
		t.Fatal(err)
	}
	if err := installCodexGlobalHook(path, scriptPath); err != nil {
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
	if strings.Count(updated, "codex-user_prompt") != 1 {
		t.Fatalf("paxm hook was not installed exactly once: %s", updated)
	}
	if strings.Index(updated, "[hooks.state]") < strings.Index(updated, "codex-user_prompt") {
		t.Fatalf("paxm hook was inserted outside [hooks]: %s", updated)
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
}
