package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
)

func TestRequestedAgentLabelsAliasesAndEvents(t *testing.T) {
	wantLabels := map[string]string{
		"cursor":  "Cursor",
		"trae":    "TRAE",
		"trae-cn": "TRAE CN",
		"kimi":    "Kimi Code",
		"zcode":   "ZCode",
		"kiro":    "Kiro",
		"cline":   "Cline",
	}
	for name, want := range wantLabels {
		if got := agentDisplayName(name); got != want {
			t.Fatalf("agentDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
	for input, want := range map[string]string{
		"trae cn":   "trae-cn",
		"trae_cn":   "trae-cn",
		"kimi code": "kimi",
		"kimi-code": "kimi",
	} {
		if got := normalizeAgentName(input); got != want {
			t.Fatalf("normalizeAgentName(%q) = %q, want %q", input, got, want)
		}
	}

	wantEvents := map[string][]string{
		"cursor": {"sessionStart", "beforeSubmitPrompt", "afterAgentResponse"},
		"kiro":   {"agentSpawn", "userPromptSubmit", "stop"},
		"cline":  {"TaskStart", "UserPromptSubmit", "TaskComplete"},
		"kimi":   {"SessionStart", "UserPromptSubmit", "Stop"},
	}
	for name, want := range wantEvents {
		bindings := nativeHookBindings(name)
		got := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			got = append(got, binding.NativeEvent)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("nativeHookBindings(%q) = %#v, want %#v", name, got, want)
		}
	}
}

func TestRequestedAgentInstallAndUninstallPreserveForeignConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "paxm", "config.yaml")
	paths := integrationTestPaths(t, dir)

	if err := os.MkdirAll(filepath.Dir(paths.cursorHooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.cursorHooks, []byte(`{"version":1,"hooks":{"stop":[{"command":"keep-cursor"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.traeHooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.traeHooks, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"keep-trae"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.zcodeConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.zcodeConfig, []byte(`{"theme":"dark","hooks":{"enabled":true,"events":{"Stop":[{"hooks":[{"type":"command","command":"keep-zcode"}]}]}},"mcp":{"servers":{"keep":{"command":"keep"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig(configPath)
	for _, name := range requestedAgentNames() {
		agent := cfg.Agents[name]
		agent.Enabled = true
		cfg.Agents[name] = agent
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	r := runner{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	for _, name := range requestedAgentNames() {
		if err := r.installAgentHookIntegration(configPath, cfg.Agents[name], name); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}

	for name, path := range map[string]string{
		"cursor":  paths.cursorMCP,
		"trae":    paths.traeMCP,
		"trae-cn": paths.traeCNMCP,
		"kimi":    paths.kimiMCP,
		"kiro":    paths.kiroMCP,
		"cline":   paths.clineMCP,
	} {
		assertFileContains(t, path, `"paxm"`, `"--agent"`, name)
	}
	assertFileContains(t, paths.cursorHooks, "keep-cursor", "beforeSubmitPrompt", "cursor-user_input")
	assertFileContains(t, paths.traeHooks, "keep-trae", "UserPromptSubmit", "trae-user_input")
	assertFileContains(t, paths.traeCNHooks, "trae-cn-user_input")
	assertFileContains(t, paths.kimiConfig, `event = "UserPromptSubmit"`, "kimi-user_input")
	assertFileContains(t, paths.kiroAgent, `"userPromptSubmit"`, "kiro-user_input", `"includeMcpJson": true`)
	assertFileContains(t, filepath.Join(paths.clineHooks, "UserPromptSubmit"), "cline-user_input")
	assertFileContains(t, filepath.Join(filepath.Dir(configPath), "hooks", "cline-user_input"), "--cline")
	assertFileContains(t, paths.zcodeConfig, "keep-zcode", `"paxm"`, "zcode-user_input")

	for _, name := range requestedAgentNames() {
		if err := uninstallAgentIntegration(configPath, name); err != nil {
			t.Fatalf("uninstall %s: %v", name, err)
		}
	}
	assertFileContains(t, paths.cursorHooks, "keep-cursor")
	assertFileNotContains(t, paths.cursorHooks, "cursor-user_input")
	assertFileContains(t, paths.traeHooks, "keep-trae")
	assertFileNotContains(t, paths.traeHooks, "trae-user_input")
	assertFileContains(t, paths.zcodeConfig, "keep-zcode", `"keep"`, `"theme"`)
	assertFileNotContains(t, paths.zcodeConfig, "zcode-user_input", `"paxm"`)
	if _, err := os.Stat(paths.kiroAgent); !os.IsNotExist(err) {
		t.Fatalf("Kiro paxm agent still exists, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.clineHooks, "UserPromptSubmit")); !os.IsNotExist(err) {
		t.Fatalf("Cline paxm hook still exists, err=%v", err)
	}
}

func TestDecodeHookEventSupportsClineAndCursorPayloads(t *testing.T) {
	clineRaw := []byte(`{"taskId":"task-1","workspaceRoots":["/tmp/cline"],"userPromptSubmit":{"prompt":"remember cline"}}`)
	clineEvent, err := decodeHookEvent(clineRaw, "cline", "user_input")
	if err != nil {
		t.Fatal(err)
	}
	if clineEvent.Prompt != "remember cline" || clineEvent.Workspace != "/tmp/cline" || clineEvent.Metadata["session_id"] != "task-1" {
		t.Fatalf("decoded Cline event = %#v", clineEvent)
	}

	cursorRaw := []byte(`{"conversation_id":"cursor-1","workspace_roots":["/tmp/cursor"],"text":"Cursor answer"}`)
	cursorEvent, err := decodeHookEvent(cursorRaw, "cursor", "turn_end")
	if err != nil {
		t.Fatal(err)
	}
	if cursorEvent.Assistant != "Cursor answer" || cursorEvent.Workspace != "/tmp/cursor" || cursorEvent.Metadata["session_id"] != "cursor-1" {
		t.Fatalf("decoded Cursor event = %#v", cursorEvent)
	}

	kiroRaw := []byte(`{"session_id":"kiro-1","cwd":"/tmp/kiro","assistant_response":"Kiro answer"}`)
	kiroEvent, err := decodeHookEvent(kiroRaw, "kiro", "turn_end")
	if err != nil {
		t.Fatal(err)
	}
	if kiroEvent.Assistant != "Kiro answer" || kiroEvent.Workspace != "/tmp/kiro" || kiroEvent.Metadata["session_id"] != "kiro-1" {
		t.Fatalf("decoded Kiro event = %#v", kiroEvent)
	}
}

func TestClineHookOutputIsNativeJSON(t *testing.T) {
	var output bytes.Buffer
	result := capture.Result{Target: "cline", Event: "user_input"}
	if err := writeClineHookOutput(&output, result, "remembered context"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Cancel              bool   `json:"cancel"`
		ContextModification string `json:"contextModification"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Cancel || got.ContextModification != "remembered context" {
		t.Fatalf("Cline hook output = %#v", got)
	}
}

func TestCursorBeforeSubmitOutputDoesNotClaimContextInjection(t *testing.T) {
	var output bytes.Buffer
	result := capture.Result{Target: "cursor", Event: "user_input"}
	if err := writeCursorHookOutput(&output, result, "also omitted", false); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["continue"] != true {
		t.Fatalf("Cursor hook output = %#v", got)
	}
	if _, ok := got["additional_context"]; ok {
		t.Fatalf("Cursor beforeSubmitPrompt output claimed unsupported context injection: %#v", got)
	}
}

type integrationPaths struct {
	cursorHooks, cursorMCP string
	traeHooks, traeMCP     string
	traeCNHooks, traeCNMCP string
	kimiConfig, kimiMCP    string
	kiroAgent, kiroMCP     string
	clineHooks, clineMCP   string
	zcodeConfig            string
}

func integrationTestPaths(t *testing.T, dir string) integrationPaths {
	t.Helper()
	paths := integrationPaths{
		cursorHooks: filepath.Join(dir, "cursor", "hooks.json"), cursorMCP: filepath.Join(dir, "cursor", "mcp.json"),
		traeHooks: filepath.Join(dir, "trae", "hooks.json"), traeMCP: filepath.Join(dir, "trae", "mcp.json"),
		traeCNHooks: filepath.Join(dir, "trae-cn", "hooks.json"), traeCNMCP: filepath.Join(dir, "trae-cn", "mcp.json"),
		kimiConfig: filepath.Join(dir, "kimi", "config.toml"), kimiMCP: filepath.Join(dir, "kimi", "mcp.json"),
		kiroAgent: filepath.Join(dir, "kiro", "agents", "paxm.json"), kiroMCP: filepath.Join(dir, "kiro", "settings", "mcp.json"),
		clineHooks: filepath.Join(dir, "cline", "hooks"), clineMCP: filepath.Join(dir, "cline", "settings", "cline_mcp_settings.json"),
		zcodeConfig: filepath.Join(dir, "zcode", "config.json"),
	}
	for key, value := range map[string]string{
		"PAXM_OPENCODE_CONFIG_DIR": filepath.Join(dir, "opencode"),
		"PAXM_CURSOR_HOOKS":        paths.cursorHooks, "PAXM_CURSOR_MCP": paths.cursorMCP,
		"PAXM_TRAE_HOOKS": paths.traeHooks, "PAXM_TRAE_MCP": paths.traeMCP,
		"PAXM_TRAE_CN_HOOKS": paths.traeCNHooks, "PAXM_TRAE_CN_MCP": paths.traeCNMCP,
		"PAXM_KIMI_CONFIG": paths.kimiConfig, "PAXM_KIMI_MCP": paths.kimiMCP,
		"PAXM_KIRO_AGENT": paths.kiroAgent, "PAXM_KIRO_MCP": paths.kiroMCP,
		"PAXM_CLINE_HOOKS_DIR": paths.clineHooks, "PAXM_CLINE_MCP": paths.clineMCP,
		"PAXM_ZCODE_CONFIG": paths.zcodeConfig,
	} {
		t.Setenv(key, value)
	}
	return paths
}

func assertFileContains(t *testing.T, path string, values ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if !strings.Contains(string(content), value) {
			t.Fatalf("%s missing %q: %s", path, value, content)
		}
	}
}

func assertFileNotContains(t *testing.T, path string, values ...string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if strings.Contains(string(content), value) {
			t.Fatalf("%s unexpectedly contains %q: %s", path, value, content)
		}
	}
}
