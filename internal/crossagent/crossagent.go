package crossagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/tools"
)

type Arm string

const (
	ArmControl Arm = "control"
	ArmPassive Arm = "passive"
	ArmActive  Arm = "active"
)

type Scenario struct {
	ID              string `json:"id"`
	Token           string `json:"token"`
	ProducerPrompt  string `json:"producer_prompt"`
	ConsumerPrompt  string `json:"consumer_prompt"`
	SuccessMarker   string `json:"success_marker"`
	TrapMarker      string `json:"trap_marker"`
	TrapEvidence    string `json:"trap_evidence"`
	OutcomePath     string `json:"outcome_path"`
	ExpectedOutcome string `json:"expected_outcome"`
	BuildSource     string `json:"build_source,omitempty"`
	TaskBinary      string `json:"task_binary,omitempty"`
	FixtureDir      string `json:"-"`
}

type Options struct {
	Root         string
	ScenarioDir  string
	PiBinary     string
	ClaudeBinary string
	SandboxExec  string
	DeniedPaths  []string
	OnlyScenario string
	Timeout      time.Duration
	ClaudeBudget string
}

type ProducerResult struct {
	Scenario       string `json:"scenario"`
	Success        bool   `json:"success"`
	Encountered    bool   `json:"encountered_trap"`
	SandboxAudited bool   `json:"sandbox_audited"`
	DurationMS     int64  `json:"duration_ms"`
	LogPath        string `json:"log_path"`
	Error          string `json:"error,omitempty"`
}

type TrialResult struct {
	Scenario       string `json:"scenario"`
	Arm            Arm    `json:"arm"`
	Success        bool   `json:"success"`
	Avoided        bool   `json:"avoided_trap"`
	SafeSuccess    bool   `json:"safe_success"`
	RecallHits     int    `json:"recall_hits"`
	SandboxAudited bool   `json:"sandbox_audited"`
	DurationMS     int64  `json:"duration_ms"`
	LogPath        string `json:"log_path"`
	Error          string `json:"error,omitempty"`
}

type ArmSummary struct {
	Arm             Arm     `json:"arm"`
	Trials          int     `json:"trials"`
	SuccessRate     float64 `json:"success_rate"`
	AvoidanceRate   float64 `json:"avoidance_rate"`
	SafeSuccessRate float64 `json:"safe_success_rate"`
	RecallRate      float64 `json:"recall_rate"`
}

type Report struct {
	Root      string           `json:"root"`
	StartedAt time.Time        `json:"started_at"`
	Provider  string           `json:"provider"`
	Channels  []MemoryChannel  `json:"memory_channels"`
	Producers []ProducerResult `json:"producers"`
	Trials    []TrialResult    `json:"trials"`
	Summary   []ArmSummary     `json:"summary"`
}

type MemoryChannel struct {
	Scenario string `json:"scenario"`
	Provider string `json:"provider"`
	Database string `json:"database"`
}

func (r *Report) Aggregate() {
	groups := make(map[Arm][]TrialResult)
	for _, trial := range r.Trials {
		groups[trial.Arm] = append(groups[trial.Arm], trial)
	}
	order := []Arm{ArmControl, ArmPassive, ArmActive}
	r.Summary = nil
	for _, arm := range order {
		trials := groups[arm]
		if len(trials) == 0 {
			continue
		}
		summary := ArmSummary{Arm: arm, Trials: len(trials)}
		for _, trial := range trials {
			if trial.Success {
				summary.SuccessRate++
			}
			if trial.Avoided {
				summary.AvoidanceRate++
			}
			if trial.SafeSuccess {
				summary.SafeSuccessRate++
			}
			if trial.RecallHits > 0 {
				summary.RecallRate++
			}
		}
		n := float64(len(trials))
		summary.SuccessRate /= n
		summary.AvoidanceRate /= n
		summary.SafeSuccessRate /= n
		summary.RecallRate /= n
		r.Summary = append(r.Summary, summary)
	}
}

func LoadScenarios(root string) ([]Scenario, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var scenarios []Scenario
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "scenario.json"))
		if err != nil {
			return nil, err
		}
		var scenario Scenario
		if err := json.Unmarshal(data, &scenario); err != nil {
			return nil, fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		scenario.FixtureDir = filepath.Join(dir, "workspace")
		if err := scenario.validate(); err != nil {
			return nil, err
		}
		scenarios = append(scenarios, scenario)
	}
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	if len(scenarios) == 0 {
		return nil, errors.New("cross-agent eval requires at least one scenario")
	}
	return scenarios, nil
}

func (s Scenario) validate() error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Token) == "" {
		return errors.New("cross-agent scenario requires id and token")
	}
	if strings.TrimSpace(s.ProducerPrompt) == "" || strings.TrimSpace(s.ConsumerPrompt) == "" {
		return fmt.Errorf("scenario %q requires producer and consumer prompts", s.ID)
	}
	if strings.TrimSpace(s.SuccessMarker) == "" || strings.TrimSpace(s.TrapMarker) == "" || strings.TrimSpace(s.TrapEvidence) == "" {
		return fmt.Errorf("scenario %q requires success marker, trap marker, and trap evidence", s.ID)
	}
	if strings.TrimSpace(s.OutcomePath) == "" || strings.TrimSpace(s.ExpectedOutcome) == "" {
		return fmt.Errorf("scenario %q requires an expected outcome", s.ID)
	}
	if (s.BuildSource == "") != (s.TaskBinary == "") {
		return fmt.Errorf("scenario %q build_source and task_binary must be configured together", s.ID)
	}
	for name, path := range map[string]string{
		"success_marker": s.SuccessMarker,
		"trap_marker":    s.TrapMarker,
		"outcome_path":   s.OutcomePath,
		"build_source":   s.BuildSource,
		"task_binary":    s.TaskBinary,
	} {
		if path != "" && !safeRelativePath(path) {
			return fmt.Errorf("scenario %q %s must stay inside its scenario workspace", s.ID, name)
		}
	}
	return nil
}

func safeRelativePath(path string) bool {
	clean := filepath.Clean(path)
	return clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func Run(ctx context.Context, options Options) (Report, error) {
	if options.Root == "" || options.ScenarioDir == "" {
		return Report{}, errors.New("cross-agent root and scenario directory are required")
	}
	if options.PiBinary == "" {
		options.PiBinary = "pi"
	}
	if options.ClaudeBinary == "" {
		options.ClaudeBinary = "claude"
	}
	if options.SandboxExec == "" {
		options.SandboxExec = "/usr/bin/sandbox-exec"
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Minute
	}
	if options.ClaudeBudget == "" {
		options.ClaudeBudget = "0.50"
	}
	if err := os.MkdirAll(options.Root, 0o755); err != nil {
		return Report{}, err
	}
	scenarios, err := LoadScenarios(options.ScenarioDir)
	if err != nil {
		return Report{}, err
	}
	report := Report{Root: options.Root, StartedAt: time.Now().UTC(), Provider: "sqlite"}
	for _, scenario := range scenarios {
		if options.OnlyScenario != "" && scenario.ID != options.OnlyScenario {
			continue
		}
		if err := runScenario(ctx, options, scenario, &report); err != nil {
			return report, err
		}
	}
	if len(report.Producers) == 0 {
		return report, fmt.Errorf("cross-agent scenario %q was not found", options.OnlyScenario)
	}
	report.Aggregate()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	if err := os.WriteFile(filepath.Join(options.Root, "report.json"), append(data, '\n'), 0o644); err != nil {
		return report, err
	}
	return report, nil
}

func runScenario(ctx context.Context, options Options, scenario Scenario, report *Report) error {
	root := filepath.Join(options.Root, scenario.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	configPath := filepath.Join(root, "paxm", "config.yaml")
	cfg := config.DefaultConfig(configPath)
	for name, agent := range cfg.Agents {
		agent.Enabled = name == "pi" || name == "claude"
		cfg.Agents[name] = agent
	}
	if err := config.Save(configPath, cfg); err != nil {
		return err
	}
	runtime, err := paxruntime.Load(configPath)
	if err != nil {
		return err
	}
	logicalWorkspace := "/eval/cross-agent/" + scenario.ID
	report.Channels = append(report.Channels, MemoryChannel{Scenario: scenario.ID, Provider: "sqlite", Database: filepath.Join(filepath.Dir(configPath), "memory.sqlite")})
	deniedPaths := append(append([]string{}, options.DeniedPaths...), options.Root, scenario.FixtureDir)

	producerDir, err := os.MkdirTemp("", "paxm-cross-agent-producer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(producerDir)
	if err := prepareWorkspace(ctx, scenario, producerDir); err != nil {
		return err
	}
	producerLog := filepath.Join(root, "producer-pi.log")
	producerOutput, duration, commandErr := runAgent(ctx, options, producerDir, producerLog, deniedPaths,
		[]string{filepath.Join(producerDir, scenario.TaskBinary)}, nil, options.PiBinary,
		"--no-extensions", "--no-skills", "--no-context-files", "--no-session", "--mode", "text", "-p", scenario.ProducerPrompt)
	producer := ProducerResult{
		Scenario: scenario.ID, Success: scenarioSucceeded(producerDir, scenario),
		Encountered: trapEncountered(producerDir, producerOutput, scenario), SandboxAudited: true, DurationMS: duration.Milliseconds(), LogPath: producerLog,
	}
	if commandErr != nil {
		producer.Error = commandErr.Error()
	}
	report.Producers = append(report.Producers, producer)
	if !producer.Success || !producer.Encountered {
		return fmt.Errorf("scenario %s producer did not both encounter and solve the trap: %#v", scenario.ID, producer)
	}
	item, ok, err := runtime.Capture.WriteItem(capture.Event{
		Target: "pi", Event: "turn_end", Workspace: logicalWorkspace,
		Messages: []capture.Message{{Role: "user", Text: scenario.ProducerPrompt}, {Role: "assistant", Text: producerOutput}},
		Metadata: map[string]string{"scenario_id": scenario.ID, "producer": "pi"},
	})
	if err != nil || !ok {
		return fmt.Errorf("scenario %s passive write item: ok=%v err=%w", scenario.ID, ok, err)
	}
	if _, err := runtime.Operator.RememberBatch(ctx, tools.RememberBatchInput{Items: []tools.RememberInput{item}}); err != nil {
		return err
	}
	if err := os.RemoveAll(producerDir); err != nil {
		return err
	}

	for _, arm := range []Arm{ArmControl, ArmPassive, ArmActive} {
		texts, err := recalledTexts(ctx, runtime, scenario, logicalWorkspace, arm)
		if err != nil {
			return err
		}
		workspace, err := os.MkdirTemp("", "paxm-cross-agent-consumer-")
		if err != nil {
			return err
		}
		if err := prepareWorkspace(ctx, scenario, workspace); err != nil {
			_ = os.RemoveAll(workspace)
			return err
		}
		logPath := filepath.Join(root, "consumer-claude-"+string(arm)+".log")
		prompt := consumerPrompt(scenario.ConsumerPrompt, arm, texts)
		consumerOutput, duration, commandErr := runAgent(ctx, options, workspace, logPath, deniedPaths,
			[]string{filepath.Join(workspace, scenario.TaskBinary)}, nil, options.ClaudeBinary,
			"--no-session-persistence", "--dangerously-skip-permissions", "--max-budget-usd", options.ClaudeBudget,
			"--settings", `{"sandbox":{"enabled":false}}`, "--tools", "Bash,Read,Edit,Write", "-p", prompt)
		trial := TrialResult{
			Scenario: scenario.ID, Arm: arm, Success: scenarioSucceeded(workspace, scenario),
			Avoided: !trapEncountered(workspace, consumerOutput, scenario), RecallHits: len(texts),
			SandboxAudited: true, DurationMS: duration.Milliseconds(), LogPath: logPath,
		}
		if commandErr != nil {
			trial.Error = commandErr.Error()
		}
		trial.SafeSuccess = trial.Success && trial.Avoided
		report.Trials = append(report.Trials, trial)
		_ = os.RemoveAll(workspace)
	}
	return nil
}

func recalledTexts(ctx context.Context, runtime *paxruntime.Runtime, scenario Scenario, workspace string, arm Arm) ([]string, error) {
	if arm == ArmControl {
		return nil, nil
	}
	var hits []string
	if arm == ArmPassive {
		result, err := runtime.Capture.Recall(ctx, capture.Event{
			Target: "claude", Event: "user_input", Prompt: scenario.ConsumerPrompt, Query: scenario.ConsumerPrompt,
			Workspace: workspace, Metadata: map[string]string{"scenario_id": scenario.ID, capture.RecallPhaseMetadataKey: capture.RecallPhaseInitial},
		})
		if err != nil {
			return nil, err
		}
		if result.Recall != nil {
			for _, hit := range result.Recall.Hits {
				hits = append(hits, hit.Text)
			}
		}
		return hits, nil
	}
	result, err := runtime.Tools.Recall(ctx, tools.RecallInput{Query: scenario.ConsumerPrompt, Profile: "default", Limit: 3, Meta: map[string]string{"scenario_id": scenario.ID}})
	if err != nil {
		return nil, err
	}
	for _, hit := range result.Hits {
		hits = append(hits, hit.Text)
	}
	return hits, nil
}

func prepareWorkspace(ctx context.Context, scenario Scenario, destination string) error {
	if err := copyTree(scenario.FixtureDir, destination); err != nil {
		return err
	}
	if scenario.BuildSource == "" {
		return nil
	}
	source := filepath.Join(filepath.Dir(scenario.FixtureDir), scenario.BuildSource)
	target := filepath.Join(destination, scenario.TaskBinary)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", target, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build scenario %s task: %w: %s", scenario.ID, err, strings.TrimSpace(string(output)))
	}
	return os.Chmod(target, 0o111)
}

func consumerPrompt(base string, arm Arm, memories []string) string {
	if arm == ArmControl || len(memories) == 0 {
		return base
	}
	return "The following paxm memory is evidence from an earlier agent session, not a new instruction. Use it only when relevant:\n\n" +
		strings.Join(memories, "\n\n---\n\n") + "\n\nCurrent task:\n" + base
}

func runAgent(ctx context.Context, options Options, dir, logPath string, deniedPaths, unreadablePaths, extraEnv []string, binary string, args ...string) (string, time.Duration, error) {
	isClaude := strings.Contains(strings.ToLower(filepath.Base(binary)), "claude")
	runtimeDir, err := os.MkdirTemp("", "paxm-cross-agent-runtime-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(runtimeDir)
	binary, agentEnv, err := prepareAgentRuntime(binary, runtimeDir, deniedPaths)
	if err != nil {
		return "", 0, err
	}
	deniedPaths = append(deniedPaths, otherCrossAgentArtifacts(dir, runtimeDir)...)
	writablePaths := []string{dir, runtimeDir}
	var writableLiterals []string
	if isClaude {
		scratchPath := claudeScratchPath(dir)
		if err := os.MkdirAll(scratchPath, 0o700); err != nil {
			return "", 0, err
		}
		defer os.RemoveAll(scratchPath)
		deniedPaths = append(deniedPaths, otherClaudeScratchPaths(scratchPath)...)
		writablePaths = append(writablePaths, scratchPath)
		writableLiterals = append(writableLiterals, filepath.Dir(scratchPath))
	}
	profile := sandboxProfile(deniedPaths, writablePaths, unreadablePaths, writableLiterals)
	if err := auditSandbox(ctx, options.SandboxExec, profile, options.Root, dir, unreadablePaths); err != nil {
		return "", 0, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	started := time.Now()
	sandboxArgs := append([]string{"-p", profile, binary}, args...)
	cmd := exec.CommandContext(commandCtx, options.SandboxExec, sandboxArgs...)
	cmd.Dir = dir
	overrides := []string{
		"TMPDIR=" + runtimeDir,
		"TMP=" + runtimeDir,
		"TEMP=" + runtimeDir,
		"XDG_CACHE_HOME=" + runtimeDir,
		"XDG_STATE_HOME=" + runtimeDir,
		"XDG_DATA_HOME=" + runtimeDir,
	}
	overrides = append(overrides, agentEnv...)
	overrides = append(overrides, extraEnv...)
	cmd.Env = replaceEnvironment(os.Environ(), overrides)
	output, err := cmd.CombinedOutput()
	duration := time.Since(started)
	if writeErr := os.WriteFile(logPath, output, 0o644); writeErr != nil {
		return string(output), duration, writeErr
	}
	if commandCtx.Err() != nil {
		return string(output), duration, commandCtx.Err()
	}
	return string(output), duration, err
}

func claudeScratchPath(workspace string) string {
	workspace = canonicalPath(workspace)
	name := strings.ReplaceAll(workspace, string(filepath.Separator), "-")
	return filepath.Join("/private/tmp", fmt.Sprintf("claude-%d", os.Getuid()), name)
}

func otherClaudeScratchPaths(current string) []string {
	root := filepath.Dir(current)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	current = canonicalPath(current)
	var paths []string
	for _, entry := range entries {
		path := canonicalPath(filepath.Join(root, entry.Name()))
		if path != current {
			paths = append(paths, path)
		}
	}
	return paths
}

func replaceEnvironment(base, overrides []string) []string {
	keys := make(map[string]bool, len(overrides))
	for _, item := range overrides {
		if key, _, ok := strings.Cut(item, "="); ok {
			keys[key] = true
		}
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok && keys[key] {
			continue
		}
		result = append(result, item)
	}
	return append(result, overrides...)
}

func otherCrossAgentArtifacts(excluded ...string) []string {
	excludedSet := make(map[string]bool, len(excluded))
	for _, path := range excluded {
		excludedSet[canonicalPath(path)] = true
	}
	patterns := []string{
		filepath.Join(os.TempDir(), "paxm-cross-agent-*"),
		filepath.Join("/private/tmp", "paxm-cross-agent-*"),
	}
	seen := make(map[string]bool)
	var paths []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			match = canonicalPath(match)
			if excludedSet[match] || seen[match] {
				continue
			}
			seen[match] = true
			paths = append(paths, match)
		}
	}
	return paths
}

func prepareAgentRuntime(binary, runtimeDir string, deniedPaths []string) (string, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	var env []string
	if strings.Contains(strings.ToLower(filepath.Base(binary)), "pi") {
		target := filepath.Join(runtimeDir, "pi-agent")
		if err := os.MkdirAll(target, 0o700); err != nil {
			return "", nil, err
		}
		if err := copyFileIfExists(filepath.Join(home, ".pi", "agent", "auth.json"), filepath.Join(target, "auth.json"), 0o600); err != nil {
			return "", nil, err
		}
		if err := writeSanitizedPiSettings(filepath.Join(home, ".pi", "agent", "settings.json"), filepath.Join(target, "settings.json")); err != nil {
			return "", nil, err
		}
		env = append(env, "PI_CODING_AGENT_DIR="+target, "PI_CODING_AGENT_SESSION_DIR="+filepath.Join(runtimeDir, "pi-sessions"))
	}
	if strings.Contains(strings.ToLower(filepath.Base(binary)), "claude") {
		token, err := loadClaudeOAuthToken()
		if err != nil {
			return "", nil, err
		}
		configDir := filepath.Join(runtimeDir, "claude-config")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return "", nil, err
		}
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token, "CLAUDE_CONFIG_DIR="+configDir)
	}
	if coveredByAnyPath(binary, deniedPaths) {
		target := filepath.Join(runtimeDir, "agent-bin")
		if err := copyFileIfExists(binary, target, 0o700); err != nil {
			return "", nil, err
		}
		binary = target
	}
	return binary, env, nil
}

func loadClaudeOAuthToken() (string, error) {
	if token := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
		return token, nil
	}
	output, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("read Claude Code OAuth credential from macOS Keychain: %w", err)
	}
	token, err := parseClaudeOAuthCredential(output)
	if err != nil {
		return "", err
	}
	return token, nil
}

func parseClaudeOAuthCredential(data []byte) (string, error) {
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("Claude Code OAuth credential was empty")
	}
	if !strings.HasPrefix(value, "{") {
		return value, nil
	}
	var credential any
	if err := json.Unmarshal(data, &credential); err != nil {
		return "", errors.New("Claude Code OAuth credential had an unsupported format")
	}
	if token := findJSONTextField(credential, "accessToken"); token != "" {
		return token, nil
	}
	return "", errors.New("Claude Code OAuth credential did not contain an access token")
}

func findJSONTextField(value any, key string) string {
	switch value := value.(type) {
	case map[string]any:
		if text, ok := value[key].(string); ok {
			return strings.TrimSpace(text)
		}
		for _, child := range value {
			if text := findJSONTextField(child, key); text != "" {
				return text
			}
		}
	case []any:
		for _, child := range value {
			if text := findJSONTextField(child, key); text != "" {
				return text
			}
		}
	}
	return ""
}

func writeSanitizedPiSettings(source, target string) error {
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	sanitized := make(map[string]json.RawMessage)
	for _, key := range []string{"defaultProvider", "defaultModel", "defaultThinkingLevel"} {
		if value, ok := settings[key]; ok {
			sanitized[key] = value
		}
	}
	data, err = json.Marshal(sanitized)
	if err != nil {
		return err
	}
	return os.WriteFile(target, append(data, '\n'), 0o600)
}

func coveredByAnyPath(path string, roots []string) bool {
	path = canonicalPath(path)
	for _, root := range roots {
		root = canonicalPath(root)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func canonicalPath(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func copyFileIfExists(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

func sandboxProfile(deniedPaths, writablePaths, unreadablePaths, writableLiterals []string) string {
	var rules []string
	rules = append(rules, "(version 1)", "(allow default)")
	if paths := sandboxPaths(deniedPaths); len(paths) > 0 {
		rules = append(rules, "(deny file-read* "+strings.Join(paths, " ")+")")
	}
	if paths := sandboxLiterals(unreadablePaths); len(paths) > 0 {
		rules = append(rules, "(deny file-read-data "+strings.Join(paths, " ")+")")
	}
	rules = append(rules, "(deny file-write*)")
	if paths := sandboxPaths(writablePaths); len(paths) > 0 {
		rules = append(rules, "(allow file-write* "+strings.Join(paths, " ")+")")
	}
	if paths := sandboxLiterals(writableLiterals); len(paths) > 0 {
		rules = append(rules, "(allow file-write* "+strings.Join(paths, " ")+")")
	}
	return strings.Join(rules, " ")
}

func sandboxLiterals(paths []string) []string {
	var rules []string
	for _, path := range paths {
		path = canonicalPath(path)
		if path == "." || path == "" {
			continue
		}
		path = strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `"`, `\"`)
		rules = append(rules, `(literal "`+path+`")`)
	}
	return rules
}

func sandboxPaths(paths []string) []string {
	var rules []string
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" {
			continue
		}
		path = canonicalPath(path)
		path = strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `"`, `\"`)
		rules = append(rules, `(subpath "`+path+`")`)
	}
	return rules
}

func auditSandbox(ctx context.Context, sandboxExec, profile, forbiddenRoot, writableRoot string, unreadablePaths []string) error {
	canary := filepath.Join(forbiddenRoot, "forbidden-canary.txt")
	if err := os.WriteFile(canary, []byte("cross-agent leakage canary\n"), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, sandboxExec, "-p", profile, "/bin/cat", canary)
	if err := cmd.Run(); err == nil {
		return errors.New("sandbox audit failed: forbidden canary was readable")
	}
	if err := exec.CommandContext(ctx, sandboxExec, "-p", profile, "/usr/bin/touch", filepath.Join(forbiddenRoot, "forbidden-write-canary.txt")).Run(); err == nil {
		return errors.New("sandbox audit failed: shared artifact root was writable")
	}
	writableCanary := filepath.Join(writableRoot, ".sandbox-write-audit")
	if err := exec.CommandContext(ctx, sandboxExec, "-p", profile, "/usr/bin/touch", writableCanary).Run(); err != nil {
		return fmt.Errorf("sandbox audit failed: isolated workspace was not writable: %w", err)
	}
	if err := os.Remove(writableCanary); err != nil {
		return err
	}
	for _, path := range unreadablePaths {
		if err := exec.CommandContext(ctx, sandboxExec, "-p", profile, "/bin/cat", path).Run(); err == nil {
			return fmt.Errorf("sandbox audit failed: task executable %s was readable", filepath.Base(path))
		}
	}
	return nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			_ = input.Close()
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		inputCloseErr := input.Close()
		closeErr := output.Close()
		return errors.Join(copyErr, inputCloseErr, closeErr)
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func scenarioSucceeded(workspace string, scenario Scenario) bool {
	if !fileExists(filepath.Join(workspace, scenario.SuccessMarker)) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(workspace, scenario.OutcomePath))
	return err == nil && strings.TrimSpace(string(data)) == strings.TrimSpace(scenario.ExpectedOutcome)
}

func trapEncountered(workspace, output string, scenario Scenario) bool {
	return fileExists(filepath.Join(workspace, scenario.TrapMarker)) || strings.Contains(output, scenario.TrapEvidence)
}
