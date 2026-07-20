package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
)

func TestCLISetupProviderAndAgentFlags(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	t.Setenv("PAXM_CLAUDE_SETTINGS", filepath.Join(t.TempDir(), "claude", "settings.json"))
	var stdout, stderr bytes.Buffer

	code := Main([]string{
		"--config", configPath, "setup", "--yes",
		"--provider", "sqlite",
		"--agent", "codex",
		"--agent", "claude",
	}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, provider := range cfg.Providers {
		wantEnabled := name == "sqlite"
		if provider.Enabled != wantEnabled {
			t.Fatalf("provider %s enabled=%t, want %t", name, provider.Enabled, wantEnabled)
		}
	}
	for name, agent := range cfg.Agents {
		wantEnabled := name == "codex" || name == "claude"
		if agent.Enabled != wantEnabled {
			t.Fatalf("agent %s enabled=%t, want %t", name, agent.Enabled, wantEnabled)
		}
	}
}

func TestCLISetupFlagsSkipSelectionPrompts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout, stderr bytes.Buffer

	// Interactive mode (no --yes) with both selections pinned: only the
	// summary confirmation remains.
	code := Main([]string{
		"--config", configPath, "setup",
		"--provider", "sqlite", "--agent", "codex",
	}, strings.NewReader("\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Select memory providers to enable") {
		t.Fatalf("provider prompt should be skipped when --provider is given: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "Select agents for passive memory") {
		t.Fatalf("agent prompt should be skipped when --agent is given: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Apply this setup?") {
		t.Fatalf("summary confirmation should remain: %s", stdout.String())
	}
}

func TestCLISetupRejectsUnknownFlagValues(t *testing.T) {
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))

	var stdout, stderr bytes.Buffer
	code := Main([]string{
		"--config", filepath.Join(t.TempDir(), "config.yaml"), "setup",
		"--provider", "bogus",
	}, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatal("setup should fail for an unknown provider")
	}
	if !strings.Contains(stderr.String(), `unknown setup provider "bogus"`) || !strings.Contains(stderr.String(), "sqlite") {
		t.Fatalf("error should name the bad value and valid options: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{
		"--config", filepath.Join(t.TempDir(), "config.yaml"), "setup",
		"--agent", "bogus",
	}, nil, &stdout, &stderr)
	if code == 0 {
		t.Fatal("setup should fail for an unknown agent")
	}
	if !strings.Contains(stderr.String(), `unknown setup agent "bogus"`) || !strings.Contains(stderr.String(), "codex") {
		t.Fatalf("error should name the bad value and valid options: %s", stderr.String())
	}
}

func TestCLISetupFlagEnablesProviderRouting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout, stderr bytes.Buffer

	code := Main([]string{
		"--config", configPath, "setup", "--yes",
		"--provider", "sqlite", "--provider", "openviking",
	}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Providers["openviking"].Enabled {
		t.Fatalf("openviking should be enabled: %#v", cfg.Providers["openviking"])
	}
	if !recallProfileHasProvider(cfg.RecallProfiles["default"], "openviking") || !writeProfileHasProvider(cfg.WriteProfiles["default"], "openviking") {
		t.Fatalf("openviking should be routed for read/write: %#v", cfg.RecallProfiles["default"].Providers)
	}
}
