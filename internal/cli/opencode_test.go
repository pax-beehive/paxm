package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeConfigDir(t *testing.T) {
	t.Run("paxm override", func(t *testing.T) {
		t.Setenv("PAXM_OPENCODE_CONFIG_DIR", "~/custom-opencode")
		t.Setenv("OPENCODE_CONFIG_DIR", "")
		if got := openCodeConfigDir(); !strings.HasSuffix(got, "/custom-opencode") {
			t.Fatalf("openCodeConfigDir() = %q", got)
		}
	})

	t.Run("xdg", func(t *testing.T) {
		t.Setenv("PAXM_OPENCODE_CONFIG_DIR", "")
		t.Setenv("OPENCODE_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		if got := openCodeConfigDir(); got != "/tmp/xdg/opencode" {
			t.Fatalf("openCodeConfigDir() = %q", got)
		}
	})
}

func TestInstallOpenCodeGlobalHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugins", "paxm.ts")
	sessionStart := filepath.Join(dir, "hooks", "opencode-session_start")
	userInput := filepath.Join(dir, "hooks", "opencode-user_input")
	turnEnd := filepath.Join(dir, "hooks", "opencode-turn_end")
	if err := installOpenCodeGlobalHook(path, map[string]string{
		"session_start": sessionStart,
		"user_input":    userInput,
		"turn_end":      turnEnd,
	}); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, expected := range []string{
		`import type { Plugin } from "@opencode-ai/plugin"`,
		`"chat.message"`,
		`"experimental.chat.messages.transform"`,
		`event.type !== "session.idle"`,
		`client.session.messages`,
		`paxm.opencode.user_input.v1`,
		`paxm.opencode.session_start.v1`,
		`additional_context`,
		`paxm.opencode.turn_end.v1`,
		`target: "opencode"`,
		`function runHook(command: string, payload: unknown, workspace: string)`,
		`cwd: workspace`,
		`if (result.error)`,
		`if (result.status !== 0)`,
		`return { ok: false, stdout`,
		userInput,
		sessionStart,
		turnEnd,
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("OpenCode plugin missing %q", expected)
		}
	}
	if strings.Contains(source, `type === "reasoning"`) {
		t.Fatalf("OpenCode plugin should select text parts instead of forwarding reasoning")
	}
	for _, command := range []string{
		"paxmSessionStartHookCommand",
		"paxmUserInputHookCommand",
		"paxmTurnEndHookCommand",
	} {
		start := strings.Index(source, "runHook("+command)
		if start < 0 {
			t.Fatalf("OpenCode plugin has no %s invocation", command)
		}
		end := strings.Index(source[start:], ");")
		if end < 0 {
			t.Fatalf("OpenCode plugin has incomplete %s invocation", command)
		}
		call := source[start : start+end+2]
		if !strings.HasSuffix(call, ", workspace);") {
			t.Fatalf("OpenCode %s invocation does not pass host workspace: %s", command, call)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("OpenCode plugin mode = %o, want 600", info.Mode().Perm())
	}
}

func TestInstallOpenCodeGlobalHookRequiresShim(t *testing.T) {
	if err := installOpenCodeGlobalHook(filepath.Join(t.TempDir(), "paxm.ts"), nil); err == nil {
		t.Fatal("installOpenCodeGlobalHook() error = nil")
	}
}
