package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
	paxeval "github.com/pax-beehive/paxm/internal/eval"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

func TestEvalProviderJSONRPCPublicCommand(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sample-provider")
	build := exec.Command("go", "build", "-o", binary, "../../examples/jsonrpc-provider")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sample: %v: %s", err, output)
	}
	t.Setenv("PAXM_SAMPLE_PROVIDER_STORE", filepath.Join(dir, "store.json"))
	var stdout, stderr bytes.Buffer
	exit := Main([]string{"eval", "provider", "jsonrpc", "--command", binary, "--json"}, nil, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var result struct {
		Passed bool `json:"passed"`
		Checks []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Checks) < 7 {
		t.Fatalf("result=%#v", result)
	}
}

func TestEvalReportIncludesConversationWriteMetrics(t *testing.T) {
	var output bytes.Buffer
	writeEvalReport(&output, paxeval.Result{
		Suite: "conversation-write", Version: 1, CaseCount: 40, Passed: 40,
		RecallAtK: 1, PrecisionAtK: 1, MRR: 1,
		WriteCaseCount: 40, Writes: 40, WriteRecall: 1, WritePrecision: 0.95,
		WriteFalsePositiveRate: 0.05, ResultCount: 40, ReturnedContextBytes: 4096, WriteDurationUS: 50000, RecallDurationUS: 25000,
	})
	for _, expected := range []string{"writes: 40/40", "write recall: 1.000", "write precision: 0.950", "write false-positive rate: 0.050", "results: 40", "returned context: 4096 bytes", "write total: 50.000ms", "recall total: 25.000ms"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("eval report missing %q: %s", expected, output.String())
		}
	}
}

func TestEvalRunLoCoMoUsesConfiguredProviderAndReturnsJSON(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	datasetPath := filepath.Join(dir, "locomo.json")
	dataset := `[{"sample_id":"sample-1","qa":[{"question":"What did Alice adopt?","answer":"A dog","evidence":["D1:1"],"category":1}],"conversation":{"speaker_a":"Alice","speaker_b":"Bob","session_1_date_time":"1 June 2023","session_1":[{"speaker":"Alice","dia_id":"D1:1","text":"I adopted a dog."}]}}]`
	if err := os.WriteFile(datasetPath, []byte(dataset), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Main([]string{"--config", configPath, "eval", "retrieval", "locomo", "--dataset", datasetPath, "--provider", "sqlite", "--manifest-dir", filepath.Join(dir, "runs"), "--run-id", "cli-test", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	var result paxeval.LoCoMoResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if result.Benchmark != "locomo-text-qa-retrieval" || result.Passed != 1 || result.RecallAtK != 1 {
		t.Fatalf("result = %#v", result)
	}
}

type cliAgentExecutor struct{}

func (cliAgentExecutor) Execute(_ context.Context, request paxeval.AgentRequest) (paxeval.AgentResponse, error) {
	answer := "a cat"
	if request.Arm != paxeval.AgentArmControl {
		answer = "a dog"
	}
	return paxeval.AgentResponse{Text: answer, InputTokens: 10, OutputTokens: 2}, nil
}

func TestEvalRunLoCoMoUsesAgentArms(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	datasetPath := filepath.Join(dir, "locomo.json")
	dataset := `[{"sample_id":"sample-1","qa":[{"question":"What did Alice adopt?","answer":"A dog","evidence":["D1:1"],"category":1}],"conversation":{"speaker_a":"Alice","speaker_b":"Bob","session_1_date_time":"1 June 2023","session_1":[{"speaker":"Alice","dia_id":"D1:1","text":"I adopted a dog."}]}}]`
	if err := os.WriteFile(datasetPath, []byte(dataset), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := MainWithDependencies([]string{"--config", configPath, "eval", "run", "locomo", "--dataset", datasetPath, "--agent", "opencode", "--model", "test/model", "--provider", "sqlite", "--max-questions", "1", "--manifest-dir", filepath.Join(dir, "runs"), "--run-id", "agent-cli-test", "--json"}, nil, &stdout, &stderr, Dependencies{AgentExecutor: cliAgentExecutor{}})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	var result paxeval.LoCoMoAgentResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.QuestionCount != 1 || result.TrialCount != 3 || result.PassiveLift != 1 || result.ActiveLift != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestEvalCleanupRemovesStaleSQLiteRun(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runsDir := filepath.Join(dir, "runs")
	scope, err := paxeval.PrepareProviderScope(cfg, "sqlite", paxeval.ScopeOptions{RunID: "stale-run", ManifestDir: runsDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(scope.Config.Providers["sqlite"].Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scope.Config.Providers["sqlite"].Path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scope.SetStatus(paxeval.EvalStatusComplete, nil); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exit := Main([]string{"--config", configPath, "eval", "cleanup", "--stale", "--manifest-dir", runsDir}, nil, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(runsDir, "stale-run")); !os.IsNotExist(err) {
		t.Fatalf("stale run still exists: %v", err)
	}
}

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
	for _, marker := range []string{`<paxm-recall version="1" mode="active">`, `</paxm-recall>`} {
		if !strings.Contains(stdout.String(), marker) {
			t.Fatalf("active recall output omitted envelope %q: %s", marker, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "recall", "--query", "passive recall", "--json"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("JSON recall failed with code %d: %s", code, stderr.String())
	}
	var recalledJSON struct {
		PaxmContext struct {
			Kind string `json:"kind"`
			Mode string `json:"mode"`
		} `json:"paxm_context"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &recalledJSON); err != nil {
		t.Fatalf("JSON recall output is invalid: %v\n%s", err, stdout.String())
	}
	if recalledJSON.PaxmContext.Kind != "recall" || recalledJSON.PaxmContext.Mode != "active" {
		t.Fatalf("JSON recall output omitted provenance: %s", stdout.String())
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

	if code := Main([]string{"--config", configPath, "setup", "--integration", "codex-plugin", "--user-id", "Todd", "--team-id", "PAX Core"}, strings.NewReader("\n\n\n\n\n"), &stdout, &stderr); code != 0 {
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
	if cfg.Identity.UserID != "todd" || cfg.Agents["codex"].AgentID != "codex-todd" {
		t.Fatalf("setup identity = %#v agent=%#v", cfg.Identity, cfg.Agents["codex"])
	}
	if scope := cfg.WriteProfiles["ltm"].Scope; scope != (config.MemoryScopeConfig{Type: "personal", ID: "todd"}) {
		t.Fatalf("default write scope = %#v", scope)
	}
	if scope := cfg.WriteProfiles["team-pax-core"].Scope; scope != (config.MemoryScopeConfig{Type: "team", ID: "pax-core"}) {
		t.Fatalf("team write scope = %#v", scope)
	}
}

func TestCLIHookSourceMatchesConfiguredCodexOwner(t *testing.T) {
	cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	event := capture.Event{Target: "codex", Event: "user_input"}
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
	if strings.Count(output, "installed hook shim") != 3 {
		t.Fatalf("pi setup should install three hook shims: %s", output)
	}
	if !strings.Contains(output, "pi-session_start") {
		t.Fatalf("pi setup did not install session_start shim: %s", output)
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
		`<paxm-recall version="1" mode="passive">`,
		`.split("</paxm-recall>").join("&lt;/paxm-recall&gt;")`,
		`onRuntimeEvent("message_end"`,
		`onRuntimeEvent("tool_execution_start"`,
		`onRuntimeEvent("tool_execution_end"`,
		`onRuntimeEvent("agent_end"`,
		`onRuntimeEvent("session_shutdown"`,
		`schema_version: "paxm.pi.user_input.v1"`,
		`schema_version: "paxm.pi.session_start.v1"`,
		`additional_context`,
		`schema_version: "paxm.pi.turn_end.v1"`,
		`target: "pi"`,
		`event: "user_input"`,
		`event: "turn_end"`,
		`customType: "paxm-memory-recall"`,
		`appendBufferedMessage("tool_call"`,
		`appendBufferedMessage("tool_result"`,
		`event?.toolName`,
		`pendingToolArgs.set`,
		`pendingToolArgs.get`,
		`pendingToolArgs.delete`,
		`event?.result`,
		`"thinking", "reasoning", "analysis", "redacted_thinking"`,
		`pi-user_input`,
		`pi-session_start`,
		`pi-turn_end`,
	} {
		if !strings.Contains(extensionText, expected) {
			t.Fatalf("pi extension missing %q: %s", expected, extensionText)
		}
	}
	if strings.Contains(extensionText, "raw_event") {
		t.Fatalf("pi extension should not forward raw runtime events into paxm payloads: %s", extensionText)
	}
	if strings.Contains(extensionText, `onRuntimeEvent("turn_end"`) {
		t.Fatalf("pi extension should flush once at agent_end, not after each model turn: %s", extensionText)
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
	if strings.Count(output, "installed hook shim") != 5 {
		t.Fatalf("Claude Code setup should install five hook shims: %s", output)
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
		`"PostToolUse"`,
		`"PostToolUseFailure"`,
		`"Stop"`,
		`claude-session_start`,
		`claude-user_input`,
		`claude-tool_use`,
		`claude-tool_failure`,
		`claude-turn_end`,
		`"timeout": 60`,
	} {
		if !strings.Contains(settings, expected) {
			t.Fatalf("Claude Code settings missing %q: %s", expected, settings)
		}
	}
	for _, event := range []string{"session_start", "user_input", "tool_use", "tool_failure", "turn_end"} {
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

func TestClaudePluginOwnershipRemovesOnlyLegacyPaxmHooks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	settingsPath := filepath.Join(dir, "claude", "settings.json")
	t.Setenv("PAXM_CLAUDE_SETTINGS", settingsPath)
	legacy := filepath.Join(dir, "hooks", "claude-turn_end")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":%q},{"type":"command","command":"/tmp/keep-me"}]}]}}`, legacy)
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig(configPath)
	claude := cfg.Agents["claude"]
	claude.Enabled = true
	claude.Integration.Owner = config.IntegrationOwnerClaudePlugin
	cfg.Agents["claude"] = claude
	var stdout bytes.Buffer
	r := runner{stdout: &stdout, stderr: io.Discard, configPath: configPath}
	if err := r.installSelectedHookIntegrations(configPath, cfg, map[string]bool{"claude": true}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), legacy) || !strings.Contains(string(content), "/tmp/keep-me") {
		t.Fatalf("unexpected settings: %s", content)
	}
	if !strings.Contains(stdout.String(), "paxm-claude plugin") {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestClaudePluginYesSetupEnablesOnlyClaudeTarget(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CLAUDE_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--config", configPath, "setup", "--integration", "claude-plugin", "--yes"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("setup code=%d: %s", code, stderr.String())
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agents["claude"].Enabled || cfg.Agents["claude"].Integration.Owner != config.IntegrationOwnerClaudePlugin {
		t.Fatalf("claude not plugin-owned: %#v", cfg.Agents["claude"])
	}
	if cfg.Agents["pi"].Enabled {
		t.Fatalf("plugin setup unexpectedly enabled pi")
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
		filepath.Join(hooksDir, "codex-session_start"),
		filepath.Join(hooksDir, "claude-session_start"),
		filepath.Join(hooksDir, "claude-turn_end"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("enabled hook shim missing: %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(hooksDir, "codex-turn_end"),
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
		if _, configured := cfg.Agents["codex"].Hooks[event.ConfigEvent]; configured {
			codexShimPath := filepath.Join(filepath.Dir(configPath), "hooks", "codex-"+event.ConfigEvent)
			if _, err := os.Stat(codexShimPath); err != nil {
				t.Fatalf("Codex shim should remain: %s: %v", codexShimPath, err)
			}
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
	integrationTestPaths(t, t.TempDir())

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
	for _, name := range []string{"Codex", "Claude Code", "Pi", "Cursor", "TRAE", "TRAE CN", "Kimi Code", "ZCode", "Kiro", "Cline"} {
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

func TestInternalCodexUserInputHookEmitsNativeContext(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{
		Type:    "sqlite",
		Enabled: true,
		Path:    filepath.Join(t.TempDir(), "memory.sqlite"),
	}
	codex := cfg.Agents["codex"]
	codex.Enabled = true
	codex.Integration.Owner = config.IntegrationOwnerCodexPlugin
	userInput := codex.Hooks["user_input"]
	userInput.Recall.Enabled = true
	userInput.Recall.Profile = "default"
	userInput.Recall.MaxResults = 3
	userInput.Recall.Insertion = config.HookInsertionConfig{MaxItems: 3}
	userInput.Recall.Initial = &config.HookInitialRecall{Enabled: false}
	userInput.Write.Enabled = false
	codex.Hooks["user_input"] = userInput
	cfg.Agents["codex"] = codex
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAXM_INTEGRATION_OWNER", config.IntegrationOwnerCodexPlugin)
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !hookSourceAllowed(loaded, capture.Event{Target: "codex", Event: "user_input"}) {
		t.Fatalf("plugin-owned Codex hook source was unexpectedly rejected: owner=%q env=%q", loaded.Agents["codex"].Integration.Owner, os.Getenv("PAXM_INTEGRATION_OWNER"))
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main([]string{"--config", configPath, "remember", "--profile", "stm", "--text", "codex native hook contract"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remember failed with code %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Main([]string{"--config", configPath, "recall", "--query", "codex native hook contract", "--json"}, nil, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "codex native hook contract") {
		t.Fatalf("acceptance fixture was not recallable, code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	eventJSON := `{"prompt":"codex native hook contract","workspace":"/tmp/project"}`
	code = Main([]string{"--config", configPath, "recall", "--hook-event", "--json"}, strings.NewReader(eventJSON), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "codex native hook contract") {
		t.Fatalf("hook fixture was not recallable, code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	event := strings.NewReader(eventJSON)
	code = Main([]string{"--config", configPath, "__hook", "--target", "codex", "--event", "user_input", "--json"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook failed with code %d: %s", code, stderr.String())
	}

	var output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		Target string `json:"target"`
		Event  string `json:"event"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("hook did not emit valid JSON: %v\n%s", err, stdout.String())
	}
	if output.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Fatalf("unexpected hook event name: %#v", output)
	}
	if !strings.Contains(output.HookSpecificOutput.AdditionalContext, "codex native hook contract") {
		t.Fatalf("native context omitted recalled memory: %#v", output)
	}
	for _, marker := range []string{`<paxm-recall version="1" mode="passive">`, `</paxm-recall>`} {
		if !strings.Contains(output.HookSpecificOutput.AdditionalContext, marker) {
			t.Fatalf("native context omitted recall envelope %q: %#v", marker, output)
		}
	}
	if cleaned := facade.StripRecallContext(output.HookSpecificOutput.AdditionalContext); cleaned != "" {
		t.Fatalf("native context left text outside the recall envelope: %q", cleaned)
	}
	if output.Target != "" || output.Event != "" {
		t.Fatalf("internal paxm hook fields leaked into Codex output: %#v", output)
	}
}

func TestInternalSessionStartInjectsConfiguredIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Identity.UserID = "todd"
	codex := cfg.Agents["codex"]
	codex.Enabled = true
	codex.AgentID = "codex-todd"
	sessionStart := codex.Hooks["session_start"]
	sessionStart.Write.Enabled = false
	sessionStart.Recall.Enabled = false
	codex.Hooks["session_start"] = sessionStart
	cfg.Agents["codex"] = codex
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	event := strings.NewReader(`{"session_id":"session-7","cwd":"/tmp/project"}`)
	code := Main([]string{"--config", configPath, "__hook", "--target", "codex", "--event", "session_start", "--json"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook failed with code %d: %s", code, stderr.String())
	}
	var output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("session bootstrap is not JSON: %v\n%s", err, stdout.String())
	}
	if output.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hook event = %#v", output)
	}
	for _, value := range []string{`<paxm-session-identity version="1">`, `"user_id":"todd"`, `"agent_id":"codex-todd"`, `"session_id":"session-7"`} {
		if !strings.Contains(output.HookSpecificOutput.AdditionalContext, value) {
			t.Fatalf("identity context omitted %q: %#v", value, output)
		}
	}
	for _, value := range []string{`<paxm-local-time version="1">`, `"local_time":`, `"time_zone":`} {
		if !strings.Contains(output.HookSpecificOutput.AdditionalContext, value) {
			t.Fatalf("local time context omitted %q: %#v", value, output)
		}
	}
}

func TestSessionIdentityFallbackAndPlainBootstrap(t *testing.T) {
	cfg := config.Config{Agents: map[string]config.AgentConfig{"claude": {}}}
	identity := sessionIdentity(cfg, capture.Event{Target: "claude"})
	if identity.AgentID != "unknown" || identity.SessionID != "unknown" {
		t.Fatalf("fallback identity = %#v", identity)
	}
	var output bytes.Buffer
	if err := writeSessionIdentityBootstrap(&output, "claude", identity, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `<paxm-session-identity version="1">`) || !strings.Contains(output.String(), `"session_id":"unknown"`) {
		t.Fatalf("plain bootstrap = %q", output.String())
	}
}

func TestCodexUserInputRefreshesLocalTimeAfterTwelveHourTurnGap(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{Type: "sqlite", Enabled: true, Path: filepath.Join(t.TempDir(), "memory.sqlite")}
	codex := cfg.Agents["codex"]
	codex.Enabled = true
	codex.Integration.Owner = config.IntegrationOwnerCodexPlugin
	for _, event := range []string{"session_start", "user_input", "turn_end"} {
		hook := codex.Hooks[event]
		hook.Recall.Enabled = false
		hook.Write.Enabled = false
		codex.Hooks[event] = hook
	}
	cfg.Agents["codex"] = codex
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAXM_INTEGRATION_OWNER", config.IntegrationOwnerCodexPlugin)

	zone := time.FixedZone("PDT", -7*60*60)
	started := time.Date(2026, time.July, 16, 9, 0, 0, 0, zone)
	run := func(now time.Time, eventName, sessionID string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		input := strings.NewReader(fmt.Sprintf(`{"session_id":%q,"prompt":"continue"}`, sessionID))
		code := MainWithDependencies(
			[]string{"--config", configPath, "__hook", "--target", "codex", "--event", eventName, "--json"},
			input, &stdout, &stderr, Dependencies{Now: func() time.Time { return now }},
		)
		if code != 0 {
			t.Fatalf("%s hook failed with code %d: %s", eventName, code, stderr.String())
		}
		if stdout.Len() == 0 {
			return ""
		}
		var output codexUserPromptHookOutput
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatalf("%s hook output is not JSON: %v\n%s", eventName, err, stdout.String())
		}
		return output.HookSpecificOutput.AdditionalContext
	}

	if output := run(started, "session_start", "session-within"); !strings.Contains(output, `"local_time":"2026-07-16T09:00:00-07:00"`) {
		t.Fatalf("session start local time = %s", output)
	}
	if output := run(started.Add(12*time.Hour), "user_input", "session-within"); output != "" {
		t.Fatalf("exactly twelve hours should not refresh local time: %s", output)
	}

	run(started, "session_start", "session-stale")
	output := run(started.Add(12*time.Hour+time.Second), "user_input", "session-stale")
	for _, value := range []string{`<paxm-local-time version="1">`, `"local_time":"2026-07-16T21:00:01-07:00"`, `"time_zone":"PDT"`} {
		if !strings.Contains(output, value) {
			t.Fatalf("stale user input omitted %q: %s", value, output)
		}
	}

	run(started, "session_start", "session-recent-turn")
	run(started.Add(11*time.Hour), "turn_end", "session-recent-turn")
	if output := run(started.Add(13*time.Hour), "user_input", "session-recent-turn"); output != "" {
		t.Fatalf("recent turn end should suppress local time refresh: %s", output)
	}

	run(started, "session_start", "session-eight-days")
	if output := run(started.Add(8*24*time.Hour), "user_input", "session-eight-days"); !strings.Contains(output, `<paxm-local-time version="1">`) {
		t.Fatalf("eight-day turn gap should refresh local time: %s", output)
	}
}

func TestCodexUserInputRefreshesLocalTimeWhenPassiveRecallFails(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{Type: "sqlite", Enabled: true, Path: t.TempDir()}
	codex := cfg.Agents["codex"]
	codex.Enabled = true
	codex.Integration.Owner = config.IntegrationOwnerCodexPlugin
	for _, eventName := range []string{"session_start", "user_input"} {
		hook := codex.Hooks[eventName]
		hook.Write.Enabled = false
		hook.Recall.Enabled = eventName == "user_input"
		codex.Hooks[eventName] = hook
	}
	cfg.Agents["codex"] = codex
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAXM_INTEGRATION_OWNER", config.IntegrationOwnerCodexPlugin)
	zone := time.FixedZone("PDT", -7*60*60)
	started := time.Date(2026, time.July, 16, 9, 0, 0, 0, zone)
	run := func(now time.Time, eventName string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := MainWithDependencies(
			[]string{"--config", configPath, "__hook", "--target", "codex", "--event", eventName, "--json"},
			strings.NewReader(`{"session_id":"recall-failure","prompt":"continue"}`),
			&stdout, &stderr, Dependencies{Now: func() time.Time { return now }},
		)
		return code, stdout.String(), stderr.String()
	}
	if code, _, stderr := run(started, "session_start"); code != 0 {
		t.Fatalf("session start failed with code %d: %s", code, stderr)
	}
	code, stdout, stderr := run(started.Add(12*time.Hour+time.Second), "user_input")
	if code != 0 {
		t.Fatalf("failed recall should remain fail-open, code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "paxm-local-time") {
		t.Fatalf("failed recall swallowed local time context: %s", stdout)
	}
	if !strings.Contains(stderr, "paxm hook recall skipped:") {
		t.Fatalf("failed recall was not diagnosed: %s", stderr)
	}
}

func TestWriteHookResultFormatsJSONMarkdownAndEmptyResults(t *testing.T) {
	result := capture.Result{Target: "claude", Event: "user_input", Recall: &tools.RecallResult{
		Query: "memory", Hits: []memory.MemoryHit{{Provider: "sqlite", ID: "one", Text: "remember this", Score: 0.9}},
	}}
	for name, jsonOut := range map[string]bool{"json": true, "markdown": false} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			r := runner{stdout: &output}
			if err := r.writeHookResult(result, jsonOut, false, ""); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "remember this") {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
	var output bytes.Buffer
	if err := (runner{stdout: &output}).writeHookResult(capture.Result{Skipped: true}, false, false, ""); err != nil || output.Len() != 0 {
		t.Fatalf("empty result output = %q err=%v", output.String(), err)
	}
	context := `<paxm-local-time version="1">local</paxm-local-time>`
	if err := (runner{stdout: &output}).writeHookResult(capture.Result{Target: "pi", Event: "user_input", Skipped: true}, true, false, context); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"additional_context"`) || !strings.Contains(output.String(), "paxm-local-time") {
		t.Fatalf("generic JSON hook omitted supplemental context: %s", output.String())
	}
}

func TestCodexUserInputCombinesLocalTimeRefreshWithRecalledMemory(t *testing.T) {
	result := capture.Result{Target: "codex", Event: "user_input", Recall: &tools.RecallResult{
		Query: "memory", Hits: []memory.MemoryHit{{Provider: "sqlite", ID: "one", Text: "remember this", Score: 0.9}},
	}}
	var output bytes.Buffer
	context := localTimeContext(time.Date(2026, time.July, 16, 21, 0, 1, 0, time.FixedZone("PDT", -7*60*60)))
	if err := (runner{stdout: &output}).writeHookResult(result, true, true, context); err != nil {
		t.Fatal(err)
	}
	var decoded codexUserPromptHookOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`<paxm-local-time version="1">`, `"local_time":"2026-07-16T21:00:01-07:00"`, `<paxm-recall version="1" mode="passive">`, "remember this"} {
		if !strings.Contains(decoded.HookSpecificOutput.AdditionalContext, value) {
			t.Fatalf("combined context omitted %q: %#v", value, decoded)
		}
	}
}

func TestInternalCodexUserInputHookIsSilentWithoutHits(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{
		Type:    "sqlite",
		Enabled: true,
		Path:    filepath.Join(t.TempDir(), "memory.sqlite"),
	}
	codex := cfg.Agents["codex"]
	codex.Enabled = true
	codex.Integration.Owner = config.IntegrationOwnerCodexPlugin
	userInput := codex.Hooks["user_input"]
	userInput.Recall.Enabled = true
	userInput.Recall.Profile = "default"
	userInput.Recall.MaxResults = 3
	userInput.Recall.Insertion = config.HookInsertionConfig{MaxItems: 3}
	userInput.Recall.Initial = &config.HookInitialRecall{Enabled: false}
	userInput.Write.Enabled = false
	codex.Hooks["user_input"] = userInput
	cfg.Agents["codex"] = codex
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAXM_INTEGRATION_OWNER", config.IntegrationOwnerCodexPlugin)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	event := strings.NewReader(`{"prompt":"memory that does not exist","workspace":"/tmp/project"}`)
	code := Main([]string{"--config", configPath, "__hook", "--target", "codex", "--event", "user_input", "--json"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook failed with code %d: %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("no-hit Codex hook should be silent, got: %s", stdout.String())
	}
}

func TestOpenCodeHookRecallsFromTopLevelScopeMem0Server(t *testing.T) {
	t.Parallel()

	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/search" {
			http.NotFound(w, request)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode search request: %v", err)
		}
		requestBodies = append(requestBodies, body)
		if body["user_id"] == nil && body["agent_id"] == nil && body["run_id"] == nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"detail": "At least one of 'user_id', 'agent_id', or 'run_id' must be provided.",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{
			"id": "mem-1", "memory": "regulatory scope remains unresolved", "score": 0.91,
		}}})
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers["sqlite"] = config.ProviderConfig{Type: "sqlite", Enabled: false, Path: filepath.Join(t.TempDir(), "memory.sqlite")}
	cfg.Providers["memory"] = config.ProviderConfig{
		Type: "mem0", Enabled: true, BaseURL: server.URL,
		UserID: "eval-user", AgentID: "shared-consumer", RunID: "eval-run",
		ScoreSemantics: "similarity",
	}
	cfg.RecallProfiles["passive"] = config.RecallProfileConfig{
		Providers:  []config.ProviderRouteConfig{{Name: "memory", Required: true, Weight: 1}},
		MaxResults: 2,
		Thresholds: config.RecallThresholdConfig{MinRelevance: 0.1, MinScore: 0.1},
	}
	opencode := cfg.Agents["opencode"]
	opencode.Enabled = true
	userInput := opencode.Hooks["user_input"]
	userInput.Recall.Profile = "passive"
	userInput.Recall.Initial = &config.HookInitialRecall{Enabled: false}
	userInput.Recall.Insertion = config.HookInsertionConfig{MinScore: 0.1, MaxItems: 2}
	userInput.Write.Enabled = false
	opencode.Hooks["user_input"] = userInput
	cfg.Agents["opencode"] = opencode
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	event := strings.NewReader(`{"prompt":"regulatory scope","workspace":"/tmp/team-memory","session_id":"session-7"}`)
	code := Main([]string{"--config", configPath, "__hook", "--target", "opencode", "--event", "user_input"}, event, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("OpenCode hook failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "regulatory scope remains unresolved") {
		t.Fatalf("OpenCode hook omitted recalled Mem0 memory: %s", stdout.String())
	}
	if len(requestBodies) != 2 {
		t.Fatalf("search requests = %#v, want nested attempt followed by top-level fallback", requestBodies)
	}
	if requestBodies[1]["user_id"] != "eval-user" || requestBodies[1]["agent_id"] != "shared-consumer" || requestBodies[1]["run_id"] != "eval-run" {
		t.Fatalf("fallback request lost Mem0 scope: %#v", requestBodies[1])
	}
}

func TestDecodeHookEventExtractsSafeWriteFields(t *testing.T) {
	event, err := decodeHookEvent([]byte(`{
		"session_id": "volatile-session",
		"last_assistant_message": "Final answer only.",
		"messages": [
			{"role": "user", "text": "visible prompt"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "private chain"},
				{"type": "text", "text": "visible answer"},
				{"type": "tool_use", "name": "Read", "input": {"file_path": "README.md"}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "content": "README contents"}]}
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
	if len(event.Messages) != 4 || event.Messages[0].Text != "visible prompt" || event.Messages[1].Text != "visible answer" || event.Messages[2].Role != "tool_call" || !strings.Contains(event.Messages[2].Text, "README.md") || event.Messages[3].Role != "tool_result" {
		t.Fatalf("messages not decoded: %#v", event.Messages)
	}
	for _, message := range event.Messages {
		if strings.Contains(message.Text, "private chain") {
			t.Fatalf("thinking leaked into messages: %#v", event.Messages)
		}
	}
	if event.Metadata["session_id"] != "volatile-session" {
		t.Fatalf("metadata not preserved: %#v", event.Metadata)
	}
	if strings.TrimSpace(string(event.Raw)) == "" {
		t.Fatal("raw event should remain available for explicit custom templates")
	}
}

func TestDecodeCodexPostToolUseCapturesToolContentAndDropsNestedReasoning(t *testing.T) {
	event, err := decodeHookEvent([]byte(`{
		"turn_id":"turn-1",
		"tool_name":"Bash",
		"tool_use_id":"call-1",
		"tool_input":{"command":"go test ./...","reasoning":"do not store this","thinking_content":"also private"},
		"tool_response":{"exit_code":0,"content":[{"type":"text","text":"all tests passed"},{"type":"reasoning","text":"hidden chain"}]}
	}`), "codex", "tool_use")
	if err != nil {
		t.Fatal(err)
	}
	if len(event.Messages) != 2 {
		t.Fatalf("messages = %#v", event.Messages)
	}
	if event.Messages[0].Role != "tool_call" || !strings.Contains(event.Messages[0].Text, "go test ./...") {
		t.Fatalf("tool call = %#v", event.Messages[0])
	}
	if event.Messages[1].Role != "tool_result" || !strings.Contains(event.Messages[1].Text, "all tests passed") {
		t.Fatalf("tool result = %#v", event.Messages[1])
	}
	for _, message := range event.Messages {
		if strings.Contains(message.Text, "do not store") || strings.Contains(message.Text, "also private") || strings.Contains(message.Text, "hidden chain") {
			t.Fatalf("reasoning leaked: %#v", event.Messages)
		}
	}
}

func TestDecodeClaudePostToolUseFailureCapturesInputAndError(t *testing.T) {
	event, err := decodeHookEvent([]byte(`{
		"tool_name":"Read",
		"tool_use_id":"toolu_1",
		"tool_input":{"file_path":"README.md"},
		"error":"permission denied",
		"duration_ms":12
	}`), "claude", "tool_failure")
	if err != nil {
		t.Fatal(err)
	}
	if len(event.Messages) != 2 || event.Messages[0].Role != "tool_call" || !strings.Contains(event.Messages[0].Text, "README.md") || event.Messages[1].Role != "tool_result" || event.Messages[1].Text != "Error: permission denied" {
		t.Fatalf("messages = %#v", event.Messages)
	}
}

func TestDedupeHookMessagesRemovesWrappedToolDuplicates(t *testing.T) {
	messages := dedupeHookMessages([]capture.Message{{Role: "tool_call", Text: "Read README.md"}, {Role: "tool_call", Text: "Read README.md"}, {Role: "tool_result", Text: "contents"}})
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestDecodeHookMessagesSupportsCodexItemsAndClaudeEnvelope(t *testing.T) {
	object := []any{
		map[string]any{"type": "function_call", "name": "lookup", "arguments": `{"query":"paxm"}`},
		map[string]any{"type": "function_call_output", "output": "found"},
		map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "secret"}, map[string]any{"type": "text", "text": "visible"}}}},
	}
	messages := hookMessagesFromRaw(object)
	if len(messages) != 3 || messages[0].Role != "tool_call" || messages[1].Role != "tool_result" || messages[2].Text != "visible" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestCodexTranscriptToolMessagesReadsCurrentTurnAndExcludesReasoning(t *testing.T) {
	messages := codexTranscriptToolMessages(filepath.Join("testdata", "codex-turn-tools.jsonl"))
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	joined := ""
	for _, message := range messages {
		joined += "\n" + message.Text
	}
	for _, expected := range []string{"view_file", "README.md", "Pax Agent neXus", "apply_patch", "update docs", "done"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
	for _, forbidden := range []string{"old_tool", "old.txt", "must not be stored", "reasoning_content", "thinking_content", "private"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("leaked %q in %q", forbidden, joined)
		}
	}
}

func TestInitialUserInputRecallStateOnlyMarksFirstSessionInput(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	state := capture.NewSessionState(hookSessionStatePath(configPath))
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	mark := func(event capture.Event) capture.Event {
		t.Helper()
		marked, err := state.MarkInitial(event, now)
		if err != nil {
			t.Fatal(err)
		}
		return marked
	}

	first := mark(capture.Event{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-a",
		},
	})
	if first.Metadata[capture.RecallPhaseMetadataKey] != capture.RecallPhaseInitial {
		t.Fatalf("first user_input should use initial recall: %#v", first.Metadata)
	}

	second := mark(capture.Event{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-a",
		},
	})
	if second.Metadata[capture.RecallPhaseMetadataKey] != "" {
		t.Fatalf("second user_input should stay strict: %#v", second.Metadata)
	}

	nextSession := mark(capture.Event{
		Target: "codex",
		Event:  "user_input",
		Metadata: map[string]string{
			"session_id": "session-b",
		},
	})
	if nextSession.Metadata[capture.RecallPhaseMetadataKey] != capture.RecallPhaseInitial {
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

	setupInput := strings.NewReader("7\n1\n/opt/paxm/plugins/corp-memory\n--config /etc/corp-memory.yaml\n15s\n1\n2\nnone\n")
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

func TestCLISetupInteractiveMem0CloudProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("PAXM_CODEX_CONFIG", filepath.Join(t.TempDir(), "codex.toml"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	setupInput := strings.NewReader("4\n\ncloud-key\n1\ntoddzheng\n1\n2\nnone\n")
	if code := Main([]string{"--config", configPath, "setup"}, setupInput, &stdout, &stderr); code != 0 {
		t.Fatalf("setup failed with code %d: %s", code, stderr.String())
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cloud := cfg.Providers["mem0_cloud"]
	if !cloud.Enabled || cloud.Type != "mem0-cloud" || cloud.APIKey != "cloud-key" || cloud.BaseURL != config.DefaultMem0CloudBaseURL() || cloud.UserID != "toddzheng" {
		t.Fatalf("unexpected mem0 cloud config: %#v", cloud)
	}
	if cloud.Infer == nil || *cloud.Infer {
		t.Fatalf("mem0 cloud infer = %#v, want false", cloud.Infer)
	}
	for _, profileName := range []string{"passive", "passive_initial"} {
		profile := cfg.RecallProfiles[profileName]
		for _, route := range profile.Providers {
			if route.Name == "mem0_cloud" && route.Timeout != "800ms" {
				t.Fatalf("%s mem0 cloud timeout = %q, want 800ms", profileName, route.Timeout)
			}
			if route.Name == "mem0_cloud" && (route.Thresholds == nil || route.Thresholds.MinRelevance != 0.20 || route.Thresholds.MinScore != 0.20) {
				t.Fatalf("%s mem0 cloud thresholds = %#v", profileName, route.Thresholds)
			}
		}
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
				Provenance: memory.Provenance{
					UserID: "todd", AgentID: "codex-todd", ScopeType: "team", ScopeID: "pax",
				},
			},
		},
	})
	output := stdout.String()
	for _, expected := range []string{
		"Score: 0.8765",
		"Relevance: 0.7654",
		"Raw score: 0.4200 (keyword_ratio)",
		"Source: cli",
		"Scope: team:pax",
		"User: todd",
		"Agent: codex-todd",
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
	toolUse := claude.Hooks["tool_use"]
	toolUse.Write.Enabled = false
	claude.Hooks["tool_use"] = toolUse
	toolFailure := claude.Hooks["tool_failure"]
	toolFailure.Write.Enabled = false
	claude.Hooks["tool_failure"] = toolFailure
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
				"cloud":  {Type: "mem0-cloud"},
				"mos":    {Type: "memos"},
				"mosc":   {Type: "memos-cloud"},
				"ov":     {Type: "openviking"},
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
		if got, want := providerOptionIDs(cfg), []string{"sqlite", "zed", "mem", "cloud", "mos", "mosc", "rpc", "ov", "custom"}; !reflect.DeepEqual(got, want) {
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
			{name: "mem0 cloud default", providerName: "mem0_cloud", provider: config.ProviderConfig{Type: "mem0-cloud"}, wantLabel: "Mem0 Cloud", wantPriority: 3},
			{name: "memos default", providerName: "memos", provider: config.ProviderConfig{Type: "memos"}, wantLabel: "MemOS", wantPriority: 4},
			{name: "memos cloud default", providerName: "memos_cloud", provider: config.ProviderConfig{Type: "memos-cloud"}, wantLabel: "MemOS Cloud", wantPriority: 5},
			{name: "jsonrpc default", providerName: "jsonrpc", provider: config.ProviderConfig{Type: "jsonrpc"}, wantLabel: "JSON-RPC", wantPriority: 6},
			{name: "openviking default", providerName: "openviking", provider: config.ProviderConfig{Type: "openviking"}, wantLabel: "OpenViking", wantPriority: 7},
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
				Providers:  []telemetry.NamedCounter{{Name: "sqlite", Counter: telemetry.Counter{Writes: 2, Refs: 1, Recalls: 2, Hits: 5, ProviderErrors: 1, ProviderWriteSamples: 2, ProviderWriteDurationMS: 24, PassiveWriteSamples: 3, PassiveWriteLatencyTotalMS: 360}}},
				Storage: telemetry.StorageInfo{
					EventBytes: 10,
					TotalBytes: 20,
					MaxBytes:   30,
					MaxFiles:   2,
				},
			},
			want: []string{"status: attention", "overview", "recall funnel", "20.0%", "write pipeline", "50.0%", "by day", "by profile", "by agent", "by hook", "by provider", "avg_write", "avg_passive_latency", "12.0ms", "120.0ms"},
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
