package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/config"
	paxeval "github.com/pax-beehive/paxm/internal/eval"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/mcp"
	"github.com/pax-beehive/paxm/internal/memory"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/telemetry"
)

const (
	defaultVersion                = "dev"
	legacyDefaultRecallMaxResults = 8
)

type ensureZepUserFunc func(context.Context, config.ProviderConfig) (zepadapter.EnsureUserResult, error)
type shutdownHookDaemonFunc func(string) error

type Dependencies struct {
	Version            string
	EnsureZepUser      ensureZepUserFunc
	ShutdownHookDaemon shutdownHookDaemonFunc
}

type runner struct {
	stdin              io.Reader
	stdout             io.Writer
	stderr             io.Writer
	configPath         string
	version            string
	ensureZepUser      ensureZepUserFunc
	shutdownHookDaemon shutdownHookDaemonFunc
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithDependencies(args, stdin, stdout, stderr, Dependencies{})
}

func MainWithDependencies(args []string, stdin io.Reader, stdout, stderr io.Writer, deps Dependencies) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	args, configPath, err := extractConfigFlag(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	deps = deps.withDefaults()
	r := runner{
		stdin:              stdin,
		stdout:             stdout,
		stderr:             stderr,
		configPath:         configPath,
		version:            deps.Version,
		ensureZepUser:      deps.EnsureZepUser,
		shutdownHookDaemon: deps.ShutdownHookDaemon,
	}
	if len(args) == 0 {
		r.printHelp()
		return 0
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		r.printHelp()
		return 0
	}
	if args[0] == "--version" || args[0] == "-v" {
		fmt.Fprintln(stdout, r.versionString())
		return 0
	}
	if err := r.run(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func (deps Dependencies) withDefaults() Dependencies {
	if strings.TrimSpace(deps.Version) == "" {
		deps.Version = defaultVersion
	}
	if deps.EnsureZepUser == nil {
		deps.EnsureZepUser = zepadapter.EnsureUser
	}
	if deps.ShutdownHookDaemon == nil {
		deps.ShutdownHookDaemon = func(configPath string) error {
			return flushExistingHookBuffer(configPath, true)
		}
	}
	return deps
}

func (r runner) versionString() string {
	if strings.TrimSpace(r.version) != "" {
		return r.version
	}
	return defaultVersion
}

func (r runner) ensureZepUserFunc() ensureZepUserFunc {
	if r.ensureZepUser != nil {
		return r.ensureZepUser
	}
	return zepadapter.EnsureUser
}

func (r runner) shutdownHookDaemonFunc() shutdownHookDaemonFunc {
	if r.shutdownHookDaemon != nil {
		return r.shutdownHookDaemon
	}
	return func(configPath string) error {
		return flushExistingHookBuffer(configPath, true)
	}
}

func (r runner) run(args []string) error {
	switch args[0] {
	case "setup":
		return r.runSetup(args[1:])
	case "uninstall":
		return r.runUninstall(args[1:])
	case "recall":
		return r.runRecall(args[1:])
	case "remember":
		return r.runRemember(args[1:])
	case "history":
		return r.runHistory(args[1:])
	case "logs":
		return r.runLogs(args[1:])
	case "backfill":
		return r.runBackfill(args[1:])
	case "eval":
		return r.runEval(args[1:])
	case "mcp":
		return r.runMCP(args[1:])
	case "update":
		return r.runUpdate(args[1:])
	case "version":
		fmt.Fprintln(r.stdout, r.versionString())
		return nil
	case "config":
		return r.runConfig(args[1:])
	case "__hook":
		return r.runInternalHook(args[1:])
	case "__hook-daemon":
		return r.runHookDaemon(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (r runner) runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	force := fs.Bool("force", false, "overwrite an existing config")
	yes := fs.Bool("yes", false, "accept default setup answers")
	integration := fs.String("integration", config.IntegrationOwnerPaxm, "hook owner: paxm, codex-plugin, or claude-plugin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *integration != config.IntegrationOwnerPaxm && *integration != config.IntegrationOwnerCodexPlugin && *integration != config.IntegrationOwnerClaudePlugin {
		return fmt.Errorf("unsupported setup integration %q", *integration)
	}

	path := r.configFile()
	prompter := newSetupPrompter(r.stdin, r.stdout)
	configExists, proceed, err := r.confirmSetupOverwrite(path, prompter, *force, *yes)
	if err != nil || !proceed {
		return err
	}
	cfg, err := setupBaseConfig(path, configExists)
	if err != nil {
		return err
	}
	selectedProviders := defaultSelections(providerOptions(cfg), cfgProviderEnabled(cfg))
	selectedHooks := defaultSelections(hookOptions(cfg), cfgHookEnabled(cfg))
	pluginTarget := ""
	if *integration == config.IntegrationOwnerCodexPlugin {
		pluginTarget = "codex"
	}
	if *integration == config.IntegrationOwnerClaudePlugin {
		pluginTarget = "claude"
	}
	previousEnabled := make(map[string]bool, len(cfg.Agents))
	for name, agent := range cfg.Agents {
		previousEnabled[name] = agent.Enabled
	}
	if pluginTarget != "" {
		for name := range selectedHooks {
			selectedHooks[name] = name == pluginTarget
		}
	}
	if !*yes {
		selectedProviders, selectedHooks, proceed, err = r.promptSetupSelections(prompter, &cfg, selectedProviders, selectedHooks)
		if err != nil || !proceed {
			return err
		}
	}
	if pluginTarget != "" {
		for name := range selectedHooks {
			selectedHooks[name] = name == pluginTarget
		}
	}
	if !anySelected(selectedProviders) {
		return errors.New("setup requires at least one memory provider")
	}
	applySetupSelections(&cfg, selectedProviders, selectedHooks, *yes)
	if pluginTarget != "" {
		for name, enabled := range previousEnabled {
			if name == pluginTarget {
				continue
			}
			agent := cfg.Agents[name]
			agent.Enabled = enabled
			cfg.Agents[name] = agent
		}
	}
	if agent, ok := cfg.Agents["codex"]; ok {
		if *integration == config.IntegrationOwnerPaxm || *integration == config.IntegrationOwnerCodexPlugin {
			agent.Integration.Owner = *integration
		}
		cfg.Agents["codex"] = agent
	}
	if agent, ok := cfg.Agents["claude"]; ok && *integration == config.IntegrationOwnerClaudePlugin {
		agent.Integration.Owner = *integration
		cfg.Agents["claude"] = agent
	}
	if !*yes {
		proceed, err = r.confirmSetupSummary(prompter, cfg, selectedProviders, selectedHooks)
		if err != nil || !proceed {
			return err
		}
	}
	zepUserResult, err := r.maybeEnsureZepUser(context.Background(), cfg)
	if err != nil {
		return err
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	if err := flushExistingHookBuffer(path, true); err != nil {
		fmt.Fprintf(r.stderr, "warning: config saved but existing hook daemon could not be stopped: %v\n", err)
	}
	fmt.Fprintf(r.stdout, "saved config: %s\n", path)
	if zepUserResult != nil {
		status := "exists"
		if zepUserResult.Created {
			status = "created"
		}
		fmt.Fprintf(r.stdout, "ensured Zep user: %s (%s)\n", zepUserResult.UserID, status)
	}
	return r.installSelectedHookIntegrations(path, cfg, selectedHooks)
}

func (r runner) confirmSetupOverwrite(path string, prompter *setupPrompter, force, yes bool) (bool, bool, error) {
	configExists := config.Exists(path)
	if !configExists || force {
		return configExists, true, nil
	}
	if yes {
		return configExists, false, fmt.Errorf("config already exists at %s; use --force to overwrite", path)
	}
	overwrite, err := prompter.confirm(fmt.Sprintf("Update existing config at %s?", path), false)
	if err != nil {
		return configExists, false, r.finishSetupPrompt(err)
	}
	if !overwrite {
		fmt.Fprintln(r.stdout, "setup cancelled")
		return configExists, false, nil
	}
	return configExists, true, nil
}

func (r runner) promptSetupSelections(prompter *setupPrompter, cfg *config.Config, selectedProviders, selectedHooks map[string]bool) (map[string]bool, map[string]bool, bool, error) {
	var err error
	selectedProviders, err = prompter.multiSelect("Select memory providers to enable", providerOptions(*cfg), selectedProviders)
	if err != nil {
		return nil, nil, false, r.finishSetupPrompt(err)
	}
	for _, providerName := range providerOptionIDs(*cfg) {
		if !selectedProviders[providerName] {
			continue
		}
		if err := promptProviderInstance(prompter.reader, prompter.output, cfg, providerName); err != nil {
			return nil, nil, false, r.finishSetupPrompt(err)
		}
	}
	selectedHooks, err = prompter.multiSelect("Select agents for passive memory", hookOptions(*cfg), selectedHooks)
	if err != nil {
		return nil, nil, false, r.finishSetupPrompt(err)
	}
	if err := configureSelectedAgents(prompter, cfg, selectedHooks); err != nil {
		return nil, nil, false, r.finishSetupPrompt(err)
	}
	return selectedProviders, selectedHooks, true, nil
}

func applySetupSelections(cfg *config.Config, selectedProviders, selectedHooks map[string]bool, yes bool) {
	for name, provider := range cfg.Providers {
		provider.Enabled = selectedProviders[name]
		cfg.Providers[name] = provider
		if !provider.Enabled {
			removeProviderFromDefaultProfiles(cfg, name)
		}
	}
	for name, selected := range selectedHooks {
		if selected {
			enablePassiveWriteStart(cfg, name)
		}
	}
	if yes {
		for name, agent := range cfg.Agents {
			agent.Enabled = selectedHooks[name]
			cfg.Agents[name] = agent
		}
	}
}

func enablePassiveWriteStart(cfg *config.Config, name string) {
	agent := cfg.Agents[name]
	if agentPassiveWriteEnabled(agent) && strings.TrimSpace(agent.PassiveWriteStartedAt) == "" {
		agent.PassiveWriteStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
		cfg.Agents[name] = agent
	}
}

func (r runner) confirmSetupSummary(prompter *setupPrompter, cfg config.Config, selectedProviders, selectedHooks map[string]bool) (bool, error) {
	writeSetupSummary(r.stdout, cfg, selectedProviders, selectedHooks)
	apply, err := prompter.confirm("Apply this setup?", true)
	if err != nil {
		return false, r.finishSetupPrompt(err)
	}
	if !apply {
		fmt.Fprintln(r.stdout, "setup cancelled")
		return false, nil
	}
	return true, nil
}

func (r runner) installSelectedHookIntegrations(path string, cfg config.Config, selectedHooks map[string]bool) error {
	for _, name := range sortedSelected(selectedHooks) {
		if !selectedHooks[name] {
			continue
		}
		if name == "codex" && strings.EqualFold(cfg.Agents[name].Integration.Owner, config.IntegrationOwnerCodexPlugin) {
			// Remove legacy paxm-managed Codex registrations before handing
			// ownership to the plugin. Plugin hooks are discovered and trusted by
			// Codex itself; paxm must not register a second copy.
			marker := filepath.Join(filepath.Dir(config.ExpandPath(path)), "hooks", "codex-")
			if err := removeCodexGlobalHooks(codexConfigPath(), marker); err != nil {
				return err
			}
			if err := removeAgentHookShims(path, name); err != nil {
				return err
			}
			fmt.Fprintln(r.stdout, "Codex hooks are owned by the paxm-memory plugin")
			continue
		}
		if name == "claude" && strings.EqualFold(cfg.Agents[name].Integration.Owner, config.IntegrationOwnerClaudePlugin) {
			marker := filepath.Join(filepath.Dir(config.ExpandPath(path)), "hooks", "claude-")
			if err := removeClaudeGlobalHooks(claudeSettingsPath(), marker); err != nil {
				return err
			}
			if err := removeAgentHookShims(path, name); err != nil {
				return err
			}
			fmt.Fprintln(r.stdout, "Claude hooks are owned by the paxm-claude plugin")
			continue
		}
		if err := removeLegacyHookShim(path, name); err != nil {
			return err
		}
		if err := uninstallAgentIntegration(path, name); err != nil {
			return fmt.Errorf("reset %s integration: %w", name, err)
		}
		installedScripts := make(map[string]string)
		for _, event := range hookInstallEventsForAgent(cfg.Agents[name]) {
			scriptPath, err := installHookShim(path, name, event.ConfigEvent)
			if err != nil {
				return err
			}
			installedScripts[event.ConfigEvent] = scriptPath
			fmt.Fprintf(r.stdout, "installed hook shim: %s\n", scriptPath)
		}
		if name == "codex" {
			fmt.Fprintf(r.stdout, "registered Codex global hook: %s\n", codexConfigPath())
		}
		if name == "claude" {
			if err := installClaudeGlobalHooks(claudeSettingsPath(), installedScripts); err != nil {
				return err
			}
			fmt.Fprintf(r.stdout, "registered Claude Code global hook: %s\n", claudeSettingsPath())
		}
		if name == "pi" {
			if err := installPiGlobalHook(piExtensionPath(), installedScripts); err != nil {
				return err
			}
			fmt.Fprintf(r.stdout, "registered Pi agent extension: %s\n", piExtensionPath())
		}
		if name == "opencode" {
			if err := installOpenCodeGlobalHook(openCodePluginPath(), installedScripts); err != nil {
				return err
			}
			fmt.Fprintf(r.stdout, "registered OpenCode global plugin: %s\n", openCodePluginPath())
		}
	}
	return nil
}

func (r runner) finishSetupPrompt(err error) error {
	if errors.Is(err, errPromptCancelled) {
		fmt.Fprintln(r.stdout, "setup cancelled")
		return nil
	}
	return err
}

func (r runner) maybeEnsureZepUser(ctx context.Context, cfg config.Config) (*zepadapter.EnsureUserResult, error) {
	provider, ok := cfg.Providers["zep"]
	if !ok || !provider.Enabled || provider.Type != "zep" || strings.TrimSpace(provider.UserID) == "" {
		return nil, nil
	}
	result, err := r.ensureZepUserFunc()(ctx, provider)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func setupBaseConfig(path string, useExisting bool) (config.Config, error) {
	defaultCfg := config.DefaultConfig(path)
	if !useExisting {
		return defaultCfg, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	cfg = config.Normalize(cfg)
	for name, provider := range defaultCfg.Providers {
		if _, ok := cfg.Providers[name]; !ok {
			cfg.Providers[name] = provider
		}
	}
	for name, profile := range defaultCfg.RecallProfiles {
		if existing, ok := cfg.RecallProfiles[name]; ok {
			cfg.RecallProfiles[name] = mergeRecallProfileDefaults(name, existing, profile)
		} else {
			if name == "passive" {
				cfg.RecallProfiles[name] = config.PassiveRecallProfileFrom(cfg.RecallProfiles["default"])
			} else if name == "passive_initial" {
				cfg.RecallProfiles[name] = config.PassiveInitialRecallProfileFrom(cfg.RecallProfiles["default"])
			} else {
				cfg.RecallProfiles[name] = profile
			}
		}
	}
	for name, profile := range defaultCfg.WriteProfiles {
		if _, ok := cfg.WriteProfiles[name]; !ok {
			cfg.WriteProfiles[name] = profile
		}
	}
	cfg.Telemetry = mergeTelemetryDefaults(cfg.Telemetry, defaultCfg.Telemetry)
	for name, agent := range defaultCfg.Agents {
		existing, ok := cfg.Agents[name]
		if !ok {
			cfg.Agents[name] = agent
			continue
		}
		if existing.Hooks == nil {
			existing.Hooks = make(map[string]config.AgentHookConfig)
		}
		for eventName, eventCfg := range agent.Hooks {
			existingHook, ok := existing.Hooks[eventName]
			if !ok {
				existing.Hooks[eventName] = eventCfg
				continue
			}
			existing.Hooks[eventName] = mergeHookDefaults(existingHook, eventCfg)
		}
		cfg.Agents[name] = existing
	}
	return cfg, nil
}

func mergeRecallProfileDefaults(name string, current, defaults config.RecallProfileConfig) config.RecallProfileConfig {
	if name == "default" && current.MaxResults == legacyDefaultRecallMaxResults && config.IsDefaultRecallProfile(defaults) && config.IsDefaultRecallThresholds(current.Thresholds) {
		current.MaxResults = defaults.MaxResults
	}
	return current
}

func mergeTelemetryDefaults(current, defaults config.TelemetryConfig) config.TelemetryConfig {
	if current.Enabled == nil {
		current.Enabled = defaults.Enabled
	}
	if current.Dir == "" {
		current.Dir = defaults.Dir
	}
	if current.EventsFile == "" {
		current.EventsFile = defaults.EventsFile
	}
	if current.MetricsFile == "" {
		current.MetricsFile = defaults.MetricsFile
	}
	if current.MaxEventFileBytes == 0 {
		current.MaxEventFileBytes = defaults.MaxEventFileBytes
	}
	if current.MaxEventFiles == 0 {
		current.MaxEventFiles = defaults.MaxEventFiles
	}
	if current.RetentionDays == 0 {
		current.RetentionDays = defaults.RetentionDays
	}
	if current.QueryPreviewChars == 0 {
		current.QueryPreviewChars = defaults.QueryPreviewChars
	}
	return current
}

func mergeHookDefaults(current, defaults config.AgentHookConfig) config.AgentHookConfig {
	if current.Recall.Profile == "default" && defaults.Recall.Profile == "passive" && current.Recall.QueryTemplate == defaults.Recall.QueryTemplate {
		current.Recall.Profile = defaults.Recall.Profile
		if defaults.Recall.MaxResults != 0 {
			current.Recall.MaxResults = defaults.Recall.MaxResults
		}
	}
	if current.Recall.Insertion == (config.HookInsertionConfig{}) {
		current.Recall.Insertion = defaults.Recall.Insertion
	}
	if current.Recall.Initial == nil && defaults.Recall.Initial != nil {
		initial := *defaults.Recall.Initial
		current.Recall.Initial = &initial
	}
	if current.Write.Profile == "" {
		current.Write.Profile = defaults.Write.Profile
	}
	if current.Write.Template == "" {
		current.Write.Template = defaults.Write.Template
	}
	if current.Write.Mode == "" {
		current.Write.Mode = defaults.Write.Mode
	}
	if !current.Write.Enabled && current.Write.Template == "{{ .prompt }}" && current.Write.Mode == "prompt" && defaults.Write.Enabled {
		current.Write = defaults.Write
		return current
	}
	if current.Write.Buffer == (config.HookBufferConfig{}) {
		current.Write.Buffer = defaults.Write.Buffer
	}
	return current
}

func (r runner) runRecall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	query := fs.String("query", "", "recall query")
	queryShort := fs.String("q", "", "recall query")
	profile := fs.String("profile", "", "recall profile")
	limit := fs.Int("limit", 0, "maximum memories to return")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read query from stdin")
	hookEvent := fs.Bool("hook-event", false, "read a hook event from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hookEvent {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		event, err := decodeHookEvent(bytes, "codex", "user_input")
		if err != nil {
			return err
		}
		return r.executeHook(event, *jsonOut, false)
	}
	q := firstNonEmpty(*query, *queryShort)
	if *stdin {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		q = string(bytes)
	}

	cfg, service, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := service.Recall(context.Background(), facade.RecallInput{
		Query:   q,
		Profile: *profile,
		Limit:   *limit,
	})
	r.recordRecallTelemetry(cfg, "recall", "cli", "", "", paxruntime.EffectiveRecallProfile(cfg, *profile), firstNonEmpty(result.Query, q), result, false, time.Since(started), err)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeRecallJSON(r.stdout, result, "active")
	}
	writeRecallContextMarkdown(r.stdout, result, "active")
	return nil
}

type recallJSONOutput struct {
	facade.RecallResult
	PaxmContext recallJSONContext `json:"paxm_context"`
}

type recallJSONContext struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	Mode    string `json:"mode"`
}

func writeRecallJSON(w io.Writer, result facade.RecallResult, mode string) error {
	return writeJSON(w, recallJSONOutput{
		RecallResult: result,
		PaxmContext:  recallJSONContext{Version: 1, Kind: "recall", Mode: mode},
	})
}

func (r runner) runEval(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: paxm eval run --suite PATH [--gate none|adapter|quality] [--json] [--compare RESULT.json] [--budget BUDGET.json] [--output RESULT.json]")
	}
	fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	suitePath := fs.String("suite", "evals/baseline", "suite file or directory")
	jsonOut := fs.Bool("json", false, "write JSON")
	comparePath := fs.String("compare", "", "compare with a prior result JSON")
	budgetPath := fs.String("budget", "", "enforce a regression budget JSON")
	outputPath := fs.String("output", "", "write the current result JSON")
	gate := fs.String("gate", "none", "failure policy: none, adapter, or quality")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *gate != "quality" && *gate != "adapter" && *gate != "none" {
		return fmt.Errorf("unsupported eval gate %q", *gate)
	}
	if *gate == "adapter" && *budgetPath != "" {
		return errors.New("--budget measures provider quality and cannot be used with --gate adapter")
	}
	if *gate == "none" && *budgetPath != "" {
		return errors.New("--budget cannot be enforced with --gate none")
	}
	suite, err := paxeval.Load(*suitePath)
	if err != nil {
		return err
	}
	result, err := (paxeval.Runner{}).Run(context.Background(), suite)
	if err != nil {
		return err
	}
	var comparison *paxeval.Comparison
	if *comparePath != "" {
		baseline, loadErr := paxeval.LoadResult(*comparePath)
		if loadErr != nil {
			return loadErr
		}
		value, compareErr := paxeval.Compare(baseline, result)
		if compareErr != nil {
			return compareErr
		}
		comparison = &value
	}
	var budgetFailures []string
	if *budgetPath != "" {
		budget, loadErr := paxeval.LoadBudget(*budgetPath)
		if loadErr != nil {
			return loadErr
		}
		budgetFailures = paxeval.CheckBudget(result, budget)
	}
	if *outputPath != "" {
		data, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		if writeErr := os.WriteFile(*outputPath, append(data, '\n'), 0o600); writeErr != nil {
			return writeErr
		}
	}
	if *jsonOut && (comparison != nil || *budgetPath != "") {
		err = writeJSON(r.stdout, struct {
			Result         paxeval.Result      `json:"result"`
			Comparison     *paxeval.Comparison `json:"comparison,omitempty"`
			BudgetFailures []string            `json:"budget_failures,omitempty"`
		}{result, comparison, budgetFailures})
	} else if *jsonOut {
		err = writeJSON(r.stdout, result)
	} else {
		writeEvalReport(r.stdout, result)
		if comparison != nil {
			writeEvalComparison(r.stdout, *comparison)
		}
		for _, failure := range budgetFailures {
			fmt.Fprintf(r.stdout, "BUDGET FAIL: %s\n", failure)
		}
	}
	if err != nil {
		return err
	}
	if *gate == "adapter" {
		if result.AdapterContractCases == 0 {
			return errors.New("adapter gate requires a suite with conversation writes")
		}
		if result.ExecutionFailed > 0 {
			return fmt.Errorf("eval execution failed: %d cases had runtime or provider errors", result.ExecutionFailed)
		}
		if result.AdapterContractFailed > 0 {
			return fmt.Errorf("adapter contract failed: %d of %d cases failed", result.AdapterContractFailed, result.AdapterContractCases)
		}
		return nil
	}
	if *gate == "none" {
		if result.ExecutionFailed > 0 {
			return fmt.Errorf("eval execution failed: %d cases had runtime or provider errors", result.ExecutionFailed)
		}
		return nil
	}
	if result.Failed > 0 {
		return fmt.Errorf("eval failed: %d of %d cases failed", result.Failed, result.CaseCount)
	}
	if len(budgetFailures) > 0 {
		return fmt.Errorf("eval regression budget failed: %d metrics outside budget", len(budgetFailures))
	}
	return nil
}

func writeEvalComparison(w io.Writer, comparison paxeval.Comparison) {
	fmt.Fprintf(w, "comparison: %s -> %s\n", comparison.BaselineSuite, comparison.CurrentSuite)
	fmt.Fprintf(w, "  passed %+d  recall@k %+.3f  precision@k %+.3f  mrr %+.3f  false-positive rate %+.3f  duration %+dms\n", comparison.PassedDelta, comparison.RecallAtKDelta, comparison.PrecisionAtKDelta, comparison.MRRDelta, comparison.FalsePositiveRateDelta, comparison.DurationMSDelta)
	if comparison.WriteRecallDelta != 0 || comparison.WritePrecisionDelta != 0 || comparison.WriteFalsePositiveRateDelta != 0 {
		fmt.Fprintf(w, "  write recall %+.3f  write precision %+.3f  write false-positive rate %+.3f\n", comparison.WriteRecallDelta, comparison.WritePrecisionDelta, comparison.WriteFalsePositiveRateDelta)
	}
}

func writeEvalReport(w io.Writer, result paxeval.Result) {
	fmt.Fprintf(w, "paxm eval: %s (v%d)\n", result.Suite, result.Version)
	fmt.Fprintf(w, "cases: %d  passed: %d  failed: %d  duration: %dms\n", result.CaseCount, result.Passed, result.Failed, result.DurationMS)
	if result.ExecutionFailed > 0 {
		fmt.Fprintf(w, "execution failures: %d\n", result.ExecutionFailed)
	}
	fmt.Fprintf(w, "recall@k: %.3f  precision@k: %.3f  mrr: %.3f  false-positive rate: %.3f\n", result.RecallAtK, result.PrecisionAtK, result.MRR, result.FalsePositiveRate)
	if result.AdapterContractCases > 0 {
		fmt.Fprintf(w, "adapter contract: %d/%d passed  failed: %d\n", result.AdapterContractPassed, result.AdapterContractCases, result.AdapterContractFailed)
	}
	if result.WriteCaseCount > 0 {
		fmt.Fprintf(w, "writes: %d/%d  write recall: %.3f  write precision: %.3f  write false-positive rate: %.3f\n", result.Writes, result.WriteCaseCount, result.WriteRecall, result.WritePrecision, result.WriteFalsePositiveRate)
		fmt.Fprintf(w, "results: %d  returned context: %d bytes  write total: %.3fms  recall total: %.3fms\n", result.ResultCount, result.ReturnedContextBytes, float64(result.WriteDurationUS)/1000, float64(result.RecallDurationUS)/1000)
	}
	for _, group := range result.Categories {
		fmt.Fprintf(w, "  %-20s %3d/%-3d  recall@k %.3f  precision@k %.3f  mrr %.3f\n", group.Name, group.Passed, group.CaseCount, group.RecallAtK, group.PrecisionAtK, group.MRR)
		if group.WriteCaseCount > 0 {
			fmt.Fprintf(w, "  %-20s write recall %.3f  write precision %.3f  write false-positive rate %.3f\n", "", group.WriteRecall, group.WritePrecision, group.WriteFalsePositiveRate)
		}
	}
	for _, item := range result.Cases {
		if item.Passed {
			continue
		}
		fmt.Fprintf(w, "FAIL %s", item.ID)
		if item.Error != "" {
			fmt.Fprintf(w, ": %s", item.Error)
		}
		if len(item.Missing) > 0 {
			fmt.Fprintf(w, " missing=%s", strings.Join(item.Missing, ","))
		}
		if len(item.Forbidden) > 0 {
			fmt.Fprintf(w, " forbidden=%s", strings.Join(item.Forbidden, ","))
		}
		if len(item.Unexpected) > 0 {
			fmt.Fprintf(w, " unexpected=%s", strings.Join(item.Unexpected, ","))
		}
		if len(item.WriteMissing) > 0 {
			fmt.Fprintf(w, " write-missing=%s", strings.Join(item.WriteMissing, ","))
		}
		if len(item.WriteForbidden) > 0 {
			fmt.Fprintf(w, " write-forbidden=%s", strings.Join(item.WriteForbidden, ","))
		}
		if len(item.MetadataMismatches) > 0 {
			fmt.Fprintf(w, " metadata=%s", strings.Join(item.MetadataMismatches, ","))
		}
		if len(item.AdapterContractErrors) > 0 {
			fmt.Fprintf(w, " adapter=%s", strings.Join(item.AdapterContractErrors, ","))
		}
		fmt.Fprintln(w)
	}
}

func (r runner) runRemember(args []string) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	text := fs.String("text", "", "memory text")
	profile := fs.String("profile", "", "write profile")
	source := fs.String("source", "cli", "memory source")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read memory text from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value := *text
	if *stdin {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		value = string(bytes)
	}

	cfg, service, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := service.Ingest(context.Background(), facade.IngestInput{
		Text:    value,
		Profile: *profile,
		Source:  *source,
	})
	r.recordRememberTelemetry(cfg, "remember", "cli", paxruntime.EffectiveWriteProfile(*profile), 1, result, time.Since(started), err)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, result)
	}
	for _, ref := range result.Refs {
		fmt.Fprintf(r.stdout, "stored memory: %s/%s\n", ref.Provider, ref.ID)
	}
	return nil
}

func (r runner) executeHook(event facade.HookEvent, jsonOut, codexNative bool) error {
	cfg, service, err := r.loadRuntime()
	if err != nil {
		return err
	}
	started := time.Now()
	result, err := service.RunHook(context.Background(), event)
	query := event.Query
	if result.Recall != nil {
		query = result.Recall.Query
	}
	var recall facade.RecallResult
	if result.Recall != nil {
		recall = *result.Recall
	}
	r.recordRecallTelemetry(cfg, "hook_recall", "hook", result.Target, result.Event, hookRecallProfile(cfg, event), query, recall, result.Skipped, time.Since(started), err)
	if err != nil {
		return err
	}
	if codexNative {
		return writeCodexUserPromptHookOutput(r.stdout, result)
	}
	if jsonOut {
		return writeJSON(r.stdout, result)
	}
	if result.Skipped || result.Recall == nil {
		return nil
	}
	writeRecallContextMarkdown(r.stdout, *result.Recall, "passive")
	return nil
}

type codexUserPromptHookOutput struct {
	HookSpecificOutput codexUserPromptHookSpecificOutput `json:"hookSpecificOutput"`
}

type codexUserPromptHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func writeCodexUserPromptHookOutput(w io.Writer, result facade.HookResult) error {
	if result.Skipped || result.Recall == nil || len(result.Recall.Hits) == 0 {
		return nil
	}
	var context bytes.Buffer
	writeRecallMarkdown(&context, *result.Recall)
	additionalContext := facade.WrapRecallContext("passive", "Relevant memory recalled by paxm:\n\n"+strings.TrimSpace(context.String()))
	if additionalContext == "" {
		return nil
	}
	return writeJSON(w, codexUserPromptHookOutput{
		HookSpecificOutput: codexUserPromptHookSpecificOutput{
			HookEventName:     "UserPromptSubmit",
			AdditionalContext: additionalContext,
		},
	})
}

type hookBufferRequest struct {
	Action  string          `json:"action,omitempty"`
	EventID string          `json:"event_id,omitempty"`
	Target  string          `json:"target"`
	Event   string          `json:"event"`
	Raw     json.RawMessage `json:"raw"`
}

type hookBufferResponse struct {
	OK             bool                   `json:"ok"`
	Buffered       bool                   `json:"buffered,omitempty"`
	Flushed        int                    `json:"flushed,omitempty"`
	ProviderWrites map[string]int         `json:"provider_writes,omitempty"`
	ProviderRefs   map[string]int         `json:"provider_refs,omitempty"`
	ProviderErrors []memory.ProviderError `json:"provider_errors,omitempty"`
	Error          string                 `json:"error,omitempty"`
}

type hookCleanupWorker struct {
	requests chan struct{}
	done     chan struct{}
	cleanup  func(context.Context)
}

func newHookCleanupWorker(cleanup func(context.Context)) *hookCleanupWorker {
	worker := &hookCleanupWorker{
		requests: make(chan struct{}, 1),
		done:     make(chan struct{}),
		cleanup:  cleanup,
	}
	go func() {
		defer close(worker.done)
		for range worker.requests {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			worker.cleanup(ctx)
			cancel()
		}
	}()
	return worker
}

func (w *hookCleanupWorker) Schedule() {
	select {
	case w.requests <- struct{}{}:
	default:
	}
}

func (w *hookCleanupWorker) Close() {
	close(w.requests)
	<-w.done
}

func (r runner) runInternalHook(args []string) error {
	fs := flag.NewFlagSet("__hook", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	target := fs.String("target", "codex", "hook target")
	eventName := fs.String("event", "", "hook event")
	jsonOut := fs.Bool("json", false, "write JSON recall output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := io.ReadAll(r.stdin)
	if err != nil {
		return err
	}
	event, err := decodeHookEvent(raw, *target, *eventName)
	if err != nil {
		return err
	}
	if cfg, cfgErr := config.Load(r.configFile()); cfgErr == nil {
		if !hookSourceAllowed(cfg, event) {
			return nil
		}
		event = r.markInitialUserInputRecall(cfg, event)
		if hookWriteEnabled(cfg, event) {
			bufferStarted := time.Now()
			response, err := r.sendHookToBuffer(event)
			r.recordHookWriteTelemetry(cfg, event, response, time.Since(bufferStarted), err)
			if err != nil {
				fmt.Fprintf(r.stderr, "paxm hook buffer skipped: %s\n", err)
			}
		}
	}
	if event.Event == "user_input" {
		codexNative := *jsonOut && event.Target == "codex" && event.Event == "user_input"
		return r.executeHook(event, *jsonOut, codexNative)
	}
	return nil
}

func hookSourceAllowed(cfg config.Config, event facade.HookEvent) bool {
	owner := strings.ToLower(strings.TrimSpace(cfg.Agents[event.Target].Integration.Owner))
	source := strings.ToLower(strings.TrimSpace(os.Getenv("PAXM_INTEGRATION_OWNER")))
	if owner == config.IntegrationOwnerCodexPlugin || owner == config.IntegrationOwnerClaudePlugin {
		return source == owner
	}
	return source == "" || source == config.IntegrationOwnerPaxm
}

func (r runner) runHookDaemon(args []string) error {
	fs := flag.NewFlagSet("__hook-daemon", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	socket := fs.String("socket", hookSocketPath(r.configFile()), "daemon socket")
	idleTimeout := fs.Duration("idle-timeout", 30*time.Minute, "daemon idle timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, service, err := r.loadRuntime()
	if err != nil {
		return err
	}
	releaseLock, err := acquireHookDaemonLock(r.configFile())
	if err != nil {
		return err
	}
	defer releaseLock()
	queuePath := hookQueuePath(r.configFile())
	if strings.TrimSpace(cfg.CaptureQueue.Path) != "" {
		queuePath = cfg.CaptureQueue.Path
	}
	maxEpisodeAge, _ := time.ParseDuration(cfg.CaptureQueue.MaxEpisodeAge)
	retryMin, _ := time.ParseDuration(cfg.CaptureQueue.RetryMin)
	queue, err := capturequeue.Open(queuePath, capturequeue.Options{
		MaxEpisodeAge: maxEpisodeAge,
		RetryMin:      retryMin,
		MaxAttempts:   cfg.CaptureQueue.MaxAttempts,
		Providers: func(profile string) []string {
			writeProfile, ok := cfg.WriteProfiles[paxruntime.EffectiveWriteProfile(profile)]
			if !ok {
				return nil
			}
			providers := make([]string, 0, len(writeProfile.Providers))
			for _, route := range writeProfile.Providers {
				providers = append(providers, route.Name)
			}
			return providers
		},
		ProviderConcurrency: func(provider string) int {
			if concurrency := cfg.CaptureQueue.ProviderConcurrency[provider]; concurrency > 0 {
				return concurrency
			}
			return cfg.CaptureQueue.ProviderConcurrency["default"]
		},
		Deliver: func(ctx context.Context, provider string, episode capturequeue.Episode) (string, error) {
			items := episode.IngestInputs()
			result, err := service.IngestBatchToProvider(ctx, provider, facade.IngestBatchInput{Items: items})
			if len(result.Refs) == 0 {
				if err != nil {
					return "", err
				}
				return "", fmt.Errorf("provider %s returned no memory reference", provider)
			}
			return result.Refs[0].ID, err
		},
		OnDelivery: func(outcome capturequeue.DeliveryOutcome) {
			hookEvent := "delivery"
			if outcome.Dead {
				hookEvent = "delivery_dead"
			}
			event := telemetry.Event{
				Time:                       time.Now().UTC(),
				Kind:                       "hook_delivery",
				Source:                     "capture_queue",
				Command:                    "hook",
				HookEvent:                  hookEvent,
				Success:                    outcome.Err == nil,
				DurationMS:                 outcome.Duration.Milliseconds(),
				ProviderDurationMS:         outcome.Duration.Milliseconds(),
				PassiveWriteLatencyTotalMS: outcome.PassiveWriteLatencyTotal.Milliseconds(),
				PassiveWriteSamples:        outcome.PassiveWriteSamples,
				ItemCount:                  1,
				EpisodeID:                  outcome.EpisodeID,
				SessionKey:                 outcome.SessionKey,
				Provider:                   outcome.Provider,
				Error:                      paxruntime.TelemetryError(outcome.Err),
			}
			if outcome.Err != nil {
				event.ProviderErrorDetails = []telemetry.ProviderErrorDetail{{Provider: outcome.Provider, Op: "put"}}
			}
			if outcome.Ref != "" {
				event.RefCount = 1
				event.ProviderWrites = map[string]int{outcome.Provider: 1}
				event.ProviderRefs = map[string]int{outcome.Provider: 1}
			}
			r.recordTelemetry(cfg, event)
		},
	})
	if err != nil {
		return err
	}
	defer queue.Close()
	worker := newCaptureDeliveryWorker(queue)
	defer worker.Close()
	cleanupWorker := newHookCleanupWorker(func(ctx context.Context) {
		_, _ = service.CleanupExpired(ctx, 500)
	})
	defer cleanupWorker.Close()
	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(*socket)
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(*socket)
	}()

	deadline := time.NewTimer(*idleTimeout)
	defer deadline.Stop()
	for {
		type acceptResult struct {
			conn net.Conn
			err  error
		}
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, err := listener.Accept()
			accepted <- acceptResult{conn: conn, err: err}
		}()
		select {
		case <-deadline.C:
			_, _ = queue.SealAll(context.Background())
			_, _ = queue.RunOnce(context.Background())
			return nil
		case result := <-accepted:
			if result.err != nil {
				return result.err
			}
			flushed, shutdown, err := handleCaptureQueueConn(context.Background(), service, queue, result.conn, worker.Notify, cleanupWorker.Schedule)
			if err != nil {
				fmt.Fprintf(r.stderr, "paxm hook buffer error: %s\n", err)
			}
			if shutdown {
				return nil
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(*idleTimeout)
			_ = flushed
		}
	}
}

func handleCaptureQueueConn(ctx context.Context, service *facade.Service, queue *capturequeue.Queue, conn net.Conn, notifyDelivery, scheduleCleanup func()) (int, bool, error) {
	defer conn.Close()
	var request hookBufferRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, false, err
	}
	if request.Action == "flush" || request.Action == "shutdown" {
		sealed, err := queue.SealAll(ctx)
		if err == nil && request.Action == "flush" {
			_, err = queue.RunOnce(ctx)
		}
		if err != nil {
			_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
			return 0, false, err
		}
		scheduleCleanup()
		_ = writeJSON(conn, hookBufferResponse{OK: true, Flushed: sealed})
		return sealed, request.Action == "shutdown", nil
	}
	event, err := decodeHookEvent(request.Raw, request.Target, request.Event)
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, false, err
	}
	if request.EventID != "" {
		if event.Metadata == nil {
			event.Metadata = make(map[string]string)
		}
		event.Metadata["event_id"] = request.EventID
	}
	item, ok, err := service.HookWriteItem(event)
	if err != nil || !ok {
		response := hookBufferResponse{OK: err == nil}
		if err != nil {
			response.Error = err.Error()
		}
		_ = writeJSON(conn, response)
		return 0, false, err
	}
	sessionKey := captureSessionKey(event)
	bufferCfg := service.HookBufferConfig(event)
	receipt, err := queue.Append(ctx, capturequeue.Event{
		ID:         strings.TrimSpace(event.Metadata["event_id"]),
		SessionKey: sessionKey,
		Terminal:   bufferCfg.Flush,
		Sequence:   hookSequence(event.Metadata, "event_sequence", "sequence"),
		Final:      hookSequence(event.Metadata, "final_sequence"),
		Item:       item,
	})
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, false, err
	}
	notifyDelivery()
	flushed := 0
	if bufferCfg.Flush {
		flushed = 1
	}
	_ = writeJSON(conn, hookBufferResponse{
		OK:       true,
		Buffered: true,
		Flushed:  flushed,
	})
	_ = receipt
	return flushed, false, nil
}

func captureSessionKey(event facade.HookEvent) string {
	target := firstNonEmpty(strings.TrimSpace(event.Target), "codex")
	workspace := firstNonEmpty(strings.TrimSpace(event.Workspace), strings.TrimSpace(event.Metadata["cwd"]), "unknown")
	if sessionID := strings.TrimSpace(event.Metadata["session_id"]); sessionID != "" {
		return target + "/workspace/" + workspace + "/session/" + sessionID
	}
	if transcript := strings.TrimSpace(event.Metadata["transcript_path"]); transcript != "" {
		return target + "/workspace/" + workspace + "/transcript/" + transcript
	}
	return target + "/workspace/" + workspace + "/event/" + firstNonEmpty(strings.TrimSpace(event.Metadata["event_id"]), newHookEventID())
}

func hookSequence(metadata map[string]string, keys ...string) *int64 {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		sequence, err := strconv.ParseInt(value, 10, 64)
		if err == nil && sequence > 0 {
			return &sequence
		}
	}
	return nil
}

func (r runner) sendHookToBuffer(event facade.HookEvent) (hookBufferResponse, error) {
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	if strings.TrimSpace(event.Metadata["event_id"]) == "" {
		event.Metadata["event_id"] = newHookEventID()
	}
	socket := hookSocketPath(r.configFile())
	response, err := sendHookBufferRequest(socket, event)
	if err != nil {
		if startErr := r.startHookDaemon(socket); startErr != nil {
			return hookBufferResponse{}, startErr
		}
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			response, err = sendHookBufferRequest(socket, event)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return hookBufferResponse{}, err
	}
	if !response.OK && response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (r runner) startHookDaemon(socket string) error {
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	cmd := exec.Command(binaryPath, "--config", r.configFile(), "__hook-daemon", "--socket", socket)
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer devNull.Close()
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	detachCommand(cmd)
	return cmd.Start()
}

func sendHookBufferRequest(socket string, event facade.HookEvent) (hookBufferResponse, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return hookBufferResponse{}, err
	}
	defer conn.Close()
	raw := event.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	request := hookBufferRequest{
		EventID: event.Metadata["event_id"],
		Target:  event.Target,
		Event:   event.Event,
		Raw:     raw,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return hookBufferResponse{}, err
	}
	var response hookBufferResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return hookBufferResponse{}, err
	}
	return response, nil
}

func newHookEventID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return "evt_" + hex.EncodeToString(bytes)
	}
	return "evt_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func flushExistingHookBuffer(configPath string, shutdown bool) error {
	socketPath := hookSocketPath(configPath)
	if _, err := os.Stat(socketPath); errors.Is(err, os.ErrNotExist) {
		lockPath := hookDaemonLockPath(configPath)
		if pathDoesNotExist(lockPath) {
			return nil
		}
		deadline := time.Now().Add(time.Second)
		for pathDoesNotExist(socketPath) {
			if pathDoesNotExist(lockPath) {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("hook daemon lock exists but socket did not become ready")
			}
			time.Sleep(25 * time.Millisecond)
		}
	} else if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(35 * time.Second)); err != nil {
		return err
	}
	action := "flush"
	if shutdown {
		action = "shutdown"
	}
	if err := json.NewEncoder(conn).Encode(hookBufferRequest{Action: action}); err != nil {
		return err
	}
	var response hookBufferResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(firstNonEmpty(response.Error, "hook buffer flush failed"))
	}
	if shutdown {
		return waitForHookDaemonStop(configPath, 5*time.Second)
	}
	return nil
}

func waitForHookDaemonStop(configPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		socketGone := pathDoesNotExist(hookSocketPath(configPath))
		lockGone := pathDoesNotExist(hookDaemonLockPath(configPath))
		if socketGone && lockGone {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("hook daemon did not stop before timeout")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func pathDoesNotExist(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func decodeHookEvent(raw []byte, target, eventName string) (facade.HookEvent, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var event facade.HookEvent
	typedRaw := raw
	var rawObject map[string]any
	if json.Unmarshal(raw, &rawObject) == nil {
		delete(rawObject, "messages")
		if encoded, err := json.Marshal(rawObject); err == nil {
			typedRaw = encoded
		}
	}
	if err := json.Unmarshal(typedRaw, &event); err != nil {
		return facade.HookEvent{}, fmt.Errorf("decode hook event JSON: %w", err)
	}
	if event.Target == "" {
		event.Target = target
	}
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = eventName
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if event.Prompt == "" {
		event.Prompt = promptFromRawHook(raw)
	}
	enrichHookEventFromRaw(&event, raw)
	event.Raw = append(json.RawMessage(nil), raw...)
	return event, nil
}

func promptFromRawHook(raw []byte) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"prompt", "user_prompt", "input", "message"} {
		value, ok := object[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func enrichHookEventFromRaw(event *facade.HookEvent, raw []byte) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return
	}
	if event.Workspace == "" {
		for _, key := range []string{"workspace", "cwd", "current_dir"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				event.Workspace = value
				break
			}
		}
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	for _, key := range []string{"session_id", "transcript_path", "cwd", "current_dir", "model", "source"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			event.Metadata[key] = value
		}
	}
	if event.Assistant == "" {
		for _, key := range []string{"last_assistant_message", "assistant", "assistant_message", "response", "output"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				event.Assistant = value
				break
			}
		}
	}
	if len(event.Messages) == 0 {
		event.Messages = hookMessagesFromRaw(object["messages"])
	}
	if event.Event == "tool_use" || event.Event == "tool_failure" {
		event.Messages = append(event.Messages, hookMessagesFromToolEvent(object)...)
	}
	if event.Target == "codex" && event.Event == "turn_end" {
		if path := strings.TrimSpace(stringField(object, "transcript_path")); path != "" {
			event.Messages = append(event.Messages, codexTranscriptToolMessages(path)...)
		}
	}
	event.Messages = dedupeHookMessages(event.Messages)
}

func codexTranscriptToolMessages(path string) []facade.HookMessage {
	file, err := os.Open(config.ExpandPath(path))
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var messages []facade.HookMessage
	for scanner.Scan() {
		var record struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		kind := strings.ToLower(stringField(record.Payload, "type"))
		if record.Type == "event_msg" && kind == "task_started" {
			messages = nil
			continue
		}
		if record.Type != "response_item" {
			continue
		}
		switch kind {
		case "function_call", "custom_tool_call":
			name := firstNonEmpty(stringField(record.Payload, "name"), stringField(record.Payload, "namespace"))
			input := hookValueText(firstNonNil(record.Payload["arguments"], record.Payload["input"]))
			if text := strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " ")); text != "" {
				messages = append(messages, facade.HookMessage{Role: "tool_call", Text: text, Source: "codex_transcript"})
			}
		case "function_call_output", "custom_tool_call_output":
			if text := hookValueText(record.Payload["output"]); text != "" {
				messages = append(messages, facade.HookMessage{Role: "tool_result", Text: text, Source: "codex_transcript"})
			}
		}
	}
	return dedupeHookMessages(messages)
}

func dedupeHookMessages(messages []facade.HookMessage) []facade.HookMessage {
	seen := make(map[string]struct{}, len(messages))
	result := make([]facade.HookMessage, 0, len(messages))
	for _, message := range messages {
		key := strings.ToLower(strings.TrimSpace(message.Role)) + "\x00" + strings.TrimSpace(firstNonEmpty(message.Text, message.Content))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, message)
	}
	return result
}

func hookMessagesFromToolEvent(object map[string]any) []facade.HookMessage {
	name := strings.TrimSpace(stringField(object, "tool_name"))
	input := hookValueText(object["tool_input"])
	response := hookValueText(firstNonNil(object["tool_response"], object["tool_result"], object["output"]))
	if response == "" {
		if failure := strings.TrimSpace(stringField(object, "error")); failure != "" {
			response = "Error: " + failure
		}
	}
	var messages []facade.HookMessage
	if call := strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " ")); call != "" {
		messages = append(messages, facade.HookMessage{Role: "tool_call", Text: call})
	}
	if response != "" {
		messages = append(messages, facade.HookMessage{Role: "tool_result", Text: response})
	}
	return messages
}

func hookMessagesFromRaw(value any) []facade.HookMessage {
	rawMessages, ok := value.([]any)
	if !ok {
		return nil
	}
	messages := make([]facade.HookMessage, 0, len(rawMessages))
	for _, rawMessage := range rawMessages {
		object, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		if nested, ok := object["message"].(map[string]any); ok {
			messages = append(messages, hookMessagesFromRaw([]any{nested})...)
			continue
		}
		role := stringField(object, "role")
		source := stringField(object, "source")
		kind := strings.ToLower(stringField(object, "type"))
		if role == "" {
			switch kind {
			case "tool_use", "tool_call", "function_call":
				if text := formatHookToolCall(object); text != "" {
					messages = append(messages, facade.HookMessage{Role: "tool_call", Text: text, Source: source})
				}
				continue
			case "tool_result", "tool_response", "function_call_output", "function_result":
				if text := hookValueText(firstNonNil(object["content"], object["output"], object["result"])); text != "" {
					messages = append(messages, facade.HookMessage{Role: "tool_result", Text: text, Source: source})
				}
				continue
			case "thinking", "reasoning", "analysis", "redacted_thinking":
				continue
			}
		}
		if text := strings.TrimSpace(firstNonEmpty(stringField(object, "text"), stringField(object, "content"))); role != "" && text != "" {
			messages = append(messages, facade.HookMessage{Role: role, Text: text, Source: source})
		}
		messages = append(messages, hookContentMessages(role, source, object["content"])...)
		messages = append(messages, hookToolCallMessages(source, object["tool_calls"])...)
	}
	return messages
}

func hookContentMessages(defaultRole, source string, value any) []facade.HookMessage {
	blocks, ok := value.([]any)
	if !ok {
		return nil
	}
	var messages []facade.HookMessage
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			continue
		}
		kind := strings.ToLower(firstNonEmpty(stringField(block, "type"), defaultRole))
		switch kind {
		case "thinking", "reasoning", "analysis", "redacted_thinking":
			continue
		case "tool_use", "tool_call", "function_call":
			if text := formatHookToolCall(block); text != "" {
				messages = append(messages, facade.HookMessage{Role: "tool_call", Text: text, Source: source})
			}
		case "tool_result", "tool_response", "function_call_output", "function_result":
			if text := hookValueText(firstNonNil(block["content"], block["output"], block["result"])); text != "" {
				messages = append(messages, facade.HookMessage{Role: "tool_result", Text: text, Source: source})
			}
		default:
			if text := strings.TrimSpace(firstNonEmpty(stringField(block, "text"), stringField(block, "content"))); text != "" {
				messages = append(messages, facade.HookMessage{Role: defaultRole, Text: text, Source: source})
			}
		}
	}
	return messages
}

func hookToolCallMessages(source string, value any) []facade.HookMessage {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	var messages []facade.HookMessage
	for _, value := range values {
		call, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if function, ok := call["function"].(map[string]any); ok {
			call = function
		}
		if text := formatHookToolCall(call); text != "" {
			messages = append(messages, facade.HookMessage{Role: "tool_call", Text: text, Source: source})
		}
	}
	return messages
}

func formatHookToolCall(call map[string]any) string {
	name := strings.TrimSpace(firstNonEmpty(stringField(call, "name"), stringField(call, "tool")))
	input := hookValueText(firstNonNil(call["input"], call["arguments"], call["args"]))
	return strings.TrimSpace(strings.Join(nonEmptyStrings(name, input), " "))
}

func hookValueText(value any) string {
	value = sanitizeHookValue(value)
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		var structured any
		if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Unmarshal([]byte(text), &structured) == nil {
			value = sanitizeHookValue(structured)
		} else {
			return text
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
}

func sanitizeHookValue(value any) any {
	switch typed := value.(type) {
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			if clean := sanitizeHookValue(item); clean != nil {
				result = append(result, clean)
			}
		}
		return result
	case map[string]any:
		kind := strings.ToLower(stringField(typed, "type"))
		if kind == "thinking" || kind == "reasoning" || kind == "analysis" || kind == "redacted_thinking" {
			return nil
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if isReasoningField(key) {
				continue
			}
			if clean := sanitizeHookValue(item); clean != nil {
				result[key] = clean
			}
		}
		return result
	default:
		return value
	}
}

func isReasoningField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "thinking", "thinking_content", "reasoning", "reasoning_content", "analysis", "chain_of_thought", "thought", "thoughts", "redacted_thinking":
		return true
	default:
		return false
	}
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
func nonEmptyStrings(values ...string) []string {
	var result []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func stringField(object map[string]any, key string) string {
	if value, ok := object[key].(string); ok {
		return value
	}
	return ""
}

func bytesTrimSpace(bytes []byte) []byte {
	return []byte(strings.TrimSpace(string(bytes)))
}

func hookSocketPath(configPath string) string {
	return filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", "paxm-hook.sock")
}

func (r runner) runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config command requires a subcommand: path, show, doctor")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(r.stdout, r.configFile())
		return nil
	case "show":
		cfg, err := config.Load(r.configFile())
		if err != nil {
			return err
		}
		return writeJSON(r.stdout, cfg)
	case "doctor":
		return r.runConfigDoctor(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func (r runner) runConfigDoctor(args []string) error {
	fs := flag.NewFlagSet("config doctor", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rt, err := paxruntime.Load(r.configFile())
	if err != nil {
		return err
	}
	statuses, err := rt.Health(context.Background())
	if *jsonOut {
		if writeErr := writeJSON(r.stdout, statuses); writeErr != nil {
			return writeErr
		}
		return err
	}
	for _, status := range statuses {
		if status.OK {
			fmt.Fprintf(r.stdout, "ok: %s\n", status.Provider)
			continue
		}
		fmt.Fprintf(r.stdout, "error: %s: %s\n", status.Provider, status.Error)
	}
	return err
}

func (r runner) runMCP(args []string) error {
	if len(args) == 0 {
		return errors.New("mcp command requires a subcommand: serve")
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
		fs.SetOutput(r.stderr)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return mcp.Serve(mcp.Options{
			ConfigPath: r.configFile(),
			Version:    r.versionString(),
			Stdin:      r.stdin,
			Stdout:     r.stdout,
			Stderr:     r.stderr,
		})
	default:
		return fmt.Errorf("unknown mcp subcommand %q", args[0])
	}
}

func (r runner) runHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	days := fs.Int("days", 7, "number of days to summarize")
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	summary, err := recorder.History(*days)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, summary)
	}
	writeHistorySummary(r.stdout, summary)
	return nil
}

func (r runner) runLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	tail := fs.Int("tail", 50, "number of recent events")
	follow := fs.Bool("follow", false, "follow new events")
	jsonOut := fs.Bool("json", false, "write JSONL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tail < 0 {
		return errors.New("logs tail must be non-negative")
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	emit := func(event telemetry.Event) error {
		if *jsonOut {
			return json.NewEncoder(r.stdout).Encode(event)
		}
		writeLogEvent(r.stdout, event)
		return nil
	}
	if *follow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return recorder.FollowEvents(ctx, *tail, 250*time.Millisecond, emit)
	}
	events, err := recorder.TailEvents(*tail)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func (r runner) loadRuntime() (config.Config, *facade.Service, error) {
	rt, err := paxruntime.Load(r.configFile())
	if err != nil {
		return config.Config{}, nil, err
	}
	return rt.Config, rt.Service, nil
}

func (r runner) loadService() (*facade.Service, error) {
	_, service, err := r.loadRuntime()
	return service, err
}

func (r runner) configFile() string {
	return paxruntime.ConfigFile(r.configPath)
}

func (r runner) printHelp() {
	fmt.Fprintln(r.stdout, "paxm - memory adapter CLI")
	fmt.Fprintln(r.stdout)
	fmt.Fprintln(r.stdout, "Usage:")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] setup [--integration paxm|codex-plugin|claude-plugin]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] uninstall [--agent AGENT] [--yes]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] recall --query TEXT [--limit N] [--json]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] remember --profile stm|ltm --text TEXT")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] history [--days N] [--json]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] logs [--tail N] [--follow] [--json]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill scan --agent AGENT [--before TIME]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill run --agent AGENT --provider NAME [--background]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill status --agent AGENT --provider NAME")
	fmt.Fprintln(r.stdout, "  paxm eval run [--suite PATH] [--json]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] mcp serve")
	fmt.Fprintln(r.stdout, "  paxm update [--check] [--version VERSION]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] config doctor")
	fmt.Fprintln(r.stdout, "  paxm version")
}

func (r runner) recordRecallTelemetry(cfg config.Config, kind, source, target, hookEvent, profile, query string, result facade.RecallResult, skipped bool, duration time.Duration, opErr error) {
	event := paxruntime.RecallTelemetryEvent(cfg, paxruntime.RecallTelemetryInput{
		Kind:      kind,
		Source:    source,
		Target:    target,
		HookEvent: hookEvent,
		Profile:   profile,
		Result:    result,
		Skipped:   skipped,
		Duration:  duration,
		Err:       opErr,
	})
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	recorder.PrepareQueryEvent(&event, query)
	r.recordTelemetry(cfg, event)
}

func (r runner) recordRememberTelemetry(cfg config.Config, kind, source, profile string, itemCount int, result facade.IngestResult, duration time.Duration, opErr error) {
	event := paxruntime.RememberTelemetryEvent(cfg, paxruntime.RememberTelemetryInput{
		Kind:      kind,
		Source:    source,
		Profile:   profile,
		ItemCount: itemCount,
		Result:    result,
		Duration:  duration,
		Err:       opErr,
	})
	r.recordTelemetry(cfg, event)
}

func (r runner) recordHookWriteTelemetry(cfg config.Config, event facade.HookEvent, response hookBufferResponse, duration time.Duration, opErr error) {
	telemetryEvent := telemetry.Event{
		Time:                 time.Now().UTC(),
		Kind:                 "hook_write",
		Source:               "hook",
		Command:              "hook",
		Target:               event.Target,
		HookEvent:            event.Event,
		Profile:              hookWriteProfile(cfg, event),
		Success:              opErr == nil,
		Skipped:              opErr != nil || !response.Buffered,
		DurationMS:           duration.Milliseconds(),
		ItemCount:            boolInt(response.Buffered),
		Flushed:              response.Flushed,
		ProviderWrites:       response.ProviderWrites,
		ProviderRefs:         response.ProviderRefs,
		ProviderErrorDetails: telemetry.ProviderErrors(response.ProviderErrors),
		Error:                paxruntime.TelemetryError(opErr),
	}
	r.recordTelemetry(cfg, telemetryEvent)
}

func (r runner) recordTelemetry(cfg config.Config, event telemetry.Event) {
	recorder := telemetry.NewRecorder(cfg.Telemetry, r.configFile())
	if err := recorder.Record(event); err != nil {
		fmt.Fprintf(r.stderr, "paxm telemetry skipped: %s\n", err)
	}
}

func hookRecallProfile(cfg config.Config, event facade.HookEvent) string {
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if agent, ok := cfg.Agents[event.Target]; ok {
		if hook, ok := agent.Hooks[event.Event]; ok {
			if event.Metadata != nil && event.Metadata[facade.HookRecallPhaseMetadataKey] == facade.HookRecallPhaseInitial && hook.Recall.Initial != nil && hook.Recall.Initial.Enabled {
				return paxruntime.EffectiveRecallProfile(cfg, hook.Recall.Initial.Profile)
			}
			return paxruntime.EffectiveRecallProfile(cfg, hook.Recall.Profile)
		}
	}
	return "default"
}

func hookWriteProfile(cfg config.Config, event facade.HookEvent) string {
	if event.Target == "" {
		event.Target = "codex"
	}
	if agent, ok := cfg.Agents[event.Target]; ok {
		if hook, ok := agent.Hooks[event.Event]; ok {
			return paxruntime.EffectiveWriteProfile(hook.Write.Profile)
		}
	}
	return "default"
}

func hookWriteEnabled(cfg config.Config, event facade.HookEvent) bool {
	if event.Target == "" {
		event.Target = "codex"
	}
	agent, ok := cfg.Agents[event.Target]
	if !ok || !agent.Enabled {
		return false
	}
	hook, ok := agent.Hooks[event.Event]
	return ok && hook.Write.Enabled
}

func (r runner) markInitialUserInputRecall(cfg config.Config, event facade.HookEvent) facade.HookEvent {
	if !hookInitialRecallEnabled(cfg, event) {
		return event
	}
	key := hookSessionStateKey(event)
	if key == "" {
		return event
	}
	first, err := markHookSessionSeen(hookSessionStatePath(r.configFile()), key, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(r.stderr, "paxm hook state skipped: %s\n", err)
		return event
	}
	if !first {
		return event
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	event.Metadata[facade.HookRecallPhaseMetadataKey] = facade.HookRecallPhaseInitial
	return event
}

func hookInitialRecallEnabled(cfg config.Config, event facade.HookEvent) bool {
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if event.Event != "user_input" {
		return false
	}
	agent, ok := cfg.Agents[event.Target]
	if !ok || !agent.Enabled {
		return false
	}
	hook, ok := agent.Hooks[event.Event]
	return ok && hook.Recall.Enabled && hook.Recall.Initial != nil && hook.Recall.Initial.Enabled
}

func hookSessionStateKey(event facade.HookEvent) string {
	target := event.Target
	if target == "" {
		target = "codex"
	}
	if value := strings.TrimSpace(event.Metadata["session_id"]); value != "" {
		return target + "/session/" + value
	}
	if value := strings.TrimSpace(event.Metadata["transcript_path"]); value != "" {
		return target + "/transcript/" + value
	}
	if value := strings.TrimSpace(event.Workspace); value != "" {
		return target + "/workspace/" + value
	}
	if value := strings.TrimSpace(event.Metadata["cwd"]); value != "" {
		return target + "/workspace/" + value
	}
	return ""
}

func hookSessionStatePath(configPath string) string {
	return filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", "session_state.json")
}

const (
	hookSessionStateVersion    = 1
	hookSessionStateMaxEntries = 1000
	hookSessionStateTTL        = 7 * 24 * time.Hour
)

type hookSessionState struct {
	Version int                  `json:"version"`
	Seen    map[string]time.Time `json:"seen"`
}

func markHookSessionSeen(path, key string, now time.Time) (bool, error) {
	state, err := loadHookSessionState(path)
	if err != nil {
		return false, err
	}
	if state.Seen == nil {
		state.Seen = make(map[string]time.Time)
	}
	pruneHookSessionState(&state, now)
	_, exists := state.Seen[key]
	state.Seen[key] = now.UTC()
	if err := saveHookSessionState(path, state); err != nil {
		return false, err
	}
	return !exists, nil
}

func loadHookSessionState(path string) (hookSessionState, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hookSessionState{Version: hookSessionStateVersion, Seen: make(map[string]time.Time)}, nil
		}
		return hookSessionState{}, err
	}
	var state hookSessionState
	if err := json.Unmarshal(bytes, &state); err != nil {
		return hookSessionState{Version: hookSessionStateVersion, Seen: make(map[string]time.Time)}, nil
	}
	if state.Version == 0 {
		state.Version = hookSessionStateVersion
	}
	if state.Seen == nil {
		state.Seen = make(map[string]time.Time)
	}
	return state, nil
}

func saveHookSessionState(path string, state hookSessionState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state.Version = hookSessionStateVersion
	bytes, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pruneHookSessionState(state *hookSessionState, now time.Time) {
	cutoff := now.Add(-hookSessionStateTTL)
	for key, seenAt := range state.Seen {
		if seenAt.Before(cutoff) {
			delete(state.Seen, key)
		}
	}
	if len(state.Seen) <= hookSessionStateMaxEntries {
		return
	}
	type seenEntry struct {
		Key    string
		SeenAt time.Time
	}
	entries := make([]seenEntry, 0, len(state.Seen))
	for key, seenAt := range state.Seen {
		entries = append(entries, seenEntry{Key: key, SeenAt: seenAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].SeenAt.Before(entries[j].SeenAt)
	})
	for len(entries) > hookSessionStateMaxEntries {
		delete(state.Seen, entries[0].Key)
		entries = entries[1:]
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type setupOption struct {
	ID    string
	Label string
}

func providerOptions(cfg config.Config) []setupOption {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		leftPriority := providerOptionPriority(cfg.Providers[names[i]].Type)
		rightPriority := providerOptionPriority(cfg.Providers[names[j]].Type)
		if leftPriority == rightPriority {
			return names[i] < names[j]
		}
		return leftPriority < rightPriority
	})
	options := make([]setupOption, 0, len(names))
	for _, name := range names {
		provider := cfg.Providers[name]
		label := name
		if provider.Type != "" && provider.Type != name {
			label = fmt.Sprintf("%s (%s)", name, provider.Type)
		}
		options = append(options, setupOption{ID: name, Label: label})
	}
	return options
}

func providerOptionIDs(cfg config.Config) []string {
	options := providerOptions(cfg)
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return ids
}

func providerOptionPriority(providerType string) int {
	switch providerType {
	case "sqlite":
		return 0
	case "zep":
		return 1
	case "mem0":
		return 2
	case "jsonrpc":
		return 3
	default:
		return 100
	}
}

func promptProviderInstance(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	switch provider.Type {
	case "sqlite":
		return promptSQLiteProvider(reader, writer, cfg, providerName)
	case "zep":
		return promptZepProvider(reader, writer, cfg, providerName)
	case "mem0":
		return promptMem0Provider(reader, writer, cfg, providerName)
	case "jsonrpc":
		return promptJSONRPCProvider(reader, writer, cfg, providerName)
	default:
		return promptProviderRouting(reader, writer, cfg, providerName, providerPromptLabel(providerName, provider))
	}
}

func promptSQLiteProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	var err error
	provider.Path, err = promptString(reader, writer, providerPromptLabel(providerName, provider)+" memory path", provider.Path)
	if err != nil {
		return err
	}
	cfg.Providers[providerName] = provider
	return promptProviderRouting(reader, writer, cfg, providerName, providerPromptLabel(providerName, provider))
}

func promptZepProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	zep := cfg.Providers[providerName]
	var err error
	zep.APIKey, err = promptString(reader, writer, providerPromptLabel(providerName, zep)+" API key", zep.APIKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(zep.APIKey) == "" {
		return errors.New("zep setup requires an API key")
	}
	targetDefault := "user"
	if zep.GraphID != "" {
		targetDefault = "graph"
	}
	target, err := promptSingleSelect(reader, writer, providerPromptLabel(providerName, zep)+" memory target", []setupOption{
		{ID: "user", Label: "user graph"},
		{ID: "graph", Label: "named graph"},
	}, targetDefault)
	if err != nil {
		return err
	}
	if target == "user" {
		zep.UserID, err = promptString(reader, writer, providerPromptLabel(providerName, zep)+" user ID", zep.UserID)
		if err != nil {
			return err
		}
		zep.GraphID = ""
		if strings.TrimSpace(zep.UserID) == "" {
			return errors.New("zep setup requires a user ID")
		}
	} else {
		zep.GraphID, err = promptString(reader, writer, providerPromptLabel(providerName, zep)+" graph ID", zep.GraphID)
		if err != nil {
			return err
		}
		zep.UserID = ""
		if strings.TrimSpace(zep.GraphID) == "" {
			return errors.New("zep setup requires a graph ID")
		}
	}
	zep.SearchScope, err = promptSingleSelect(reader, writer, providerPromptLabel(providerName, zep)+" search scope", []setupOption{
		{ID: "episodes", Label: "episodes"},
		{ID: "edges", Label: "edges"},
		{ID: "nodes", Label: "nodes"},
		{ID: "observations", Label: "observations"},
		{ID: "thread_summaries", Label: "thread summaries"},
		{ID: "auto", Label: "auto"},
	}, firstNonEmpty(zep.SearchScope, "episodes"))
	if err != nil {
		return err
	}
	cfg.Providers[providerName] = zep
	return promptProviderRouting(reader, writer, cfg, providerName, providerPromptLabel(providerName, zep))
}

func promptMem0Provider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	mem0 := cfg.Providers[providerName]
	label := providerPromptLabel(providerName, mem0)
	var err error
	mem0.BaseURL, err = promptString(reader, writer, label+" base URL", firstNonEmpty(mem0.BaseURL, config.DefaultMem0BaseURL()))
	if err != nil {
		return err
	}
	if strings.TrimSpace(mem0.BaseURL) == "" {
		return errors.New("mem0 setup requires a base URL")
	}
	mem0.APIKey, err = promptString(reader, writer, label+" API key (blank if auth is disabled)", mem0.APIKey)
	if err != nil {
		return err
	}
	target, err := promptSingleSelect(reader, writer, label+" memory target", []setupOption{
		{ID: "user", Label: "user_id"},
		{ID: "agent", Label: "agent_id"},
		{ID: "run", Label: "run_id"},
	}, currentMem0Target(mem0))
	if err != nil {
		return err
	}
	switch target {
	case "agent":
		mem0.AgentID, err = promptString(reader, writer, label+" agent ID", mem0.AgentID)
		if err != nil {
			return err
		}
		mem0.UserID = ""
		mem0.RunID = ""
		if strings.TrimSpace(mem0.AgentID) == "" {
			return errors.New("mem0 setup requires an agent ID")
		}
	case "run":
		mem0.RunID, err = promptString(reader, writer, label+" run ID", mem0.RunID)
		if err != nil {
			return err
		}
		mem0.UserID = ""
		mem0.AgentID = ""
		if strings.TrimSpace(mem0.RunID) == "" {
			return errors.New("mem0 setup requires a run ID")
		}
	default:
		mem0.UserID, err = promptString(reader, writer, label+" user ID", mem0.UserID)
		if err != nil {
			return err
		}
		mem0.AgentID = ""
		mem0.RunID = ""
		if strings.TrimSpace(mem0.UserID) == "" {
			return errors.New("mem0 setup requires a user ID")
		}
	}
	cfg.Providers[providerName] = mem0
	return promptProviderRouting(reader, writer, cfg, providerName, label)
}

func promptJSONRPCProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	label := providerPromptLabel(providerName, provider)
	var err error
	provider.Transport, err = promptSingleSelect(reader, writer, label+" transport", []setupOption{
		{ID: "stdio", Label: "stdio"},
	}, firstNonEmpty(provider.Transport, "stdio"))
	if err != nil {
		return err
	}
	provider.Command, err = promptString(reader, writer, label+" command", provider.Command)
	if err != nil {
		return err
	}
	if strings.TrimSpace(provider.Command) == "" {
		return errors.New("jsonrpc setup requires a command")
	}
	argsText, err := promptString(reader, writer, label+" args (space-separated)", strings.Join(provider.Args, " "))
	if err != nil {
		return err
	}
	provider.Args = strings.Fields(argsText)
	provider.Timeout, err = promptString(reader, writer, label+" timeout", firstNonEmpty(provider.Timeout, "30s"))
	if err != nil {
		return err
	}
	if _, err := time.ParseDuration(provider.Timeout); err != nil {
		return fmt.Errorf("jsonrpc setup timeout: %w", err)
	}
	cfg.Providers[providerName] = provider
	return promptProviderRouting(reader, writer, cfg, providerName, label)
}

func providerPromptLabel(providerName string, provider config.ProviderConfig) string {
	switch provider.Type {
	case "sqlite":
		if providerName == "sqlite" {
			return "SQLite"
		}
		return providerName + " (SQLite)"
	case "zep":
		if providerName == "zep" {
			return "Zep"
		}
		return providerName + " (Zep)"
	case "mem0":
		if providerName == "mem0" {
			return "Mem0"
		}
		return providerName + " (Mem0)"
	case "jsonrpc":
		if providerName == "jsonrpc" {
			return "JSON-RPC"
		}
		return providerName + " (JSON-RPC)"
	default:
		return providerName
	}
}

func currentMem0Target(provider config.ProviderConfig) string {
	switch {
	case strings.TrimSpace(provider.AgentID) != "":
		return "agent"
	case strings.TrimSpace(provider.RunID) != "":
		return "run"
	default:
		return "user"
	}
}

func hookOptions(cfg config.Config) []setupOption {
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		leftPriority := agentOptionPriority(names[i])
		rightPriority := agentOptionPriority(names[j])
		if leftPriority == rightPriority {
			return names[i] < names[j]
		}
		return leftPriority < rightPriority
	})
	options := make([]setupOption, 0, len(names))
	for _, name := range names {
		options = append(options, setupOption{ID: name, Label: agentDisplayName(name)})
	}
	return options
}

func agentOptionPriority(name string) int {
	switch name {
	case "codex":
		return 0
	case "claude":
		return 1
	case "pi":
		return 2
	case "opencode":
		return 3
	default:
		return 100
	}
}

func agentDisplayName(name string) string {
	switch name {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	case "pi":
		return "Pi"
	case "opencode":
		return "OpenCode"
	default:
		return name
	}
}

func cfgProviderEnabled(cfg config.Config) map[string]bool {
	selected := make(map[string]bool)
	for name, provider := range cfg.Providers {
		selected[name] = provider.Enabled
	}
	return selected
}

func cfgHookEnabled(cfg config.Config) map[string]bool {
	selected := make(map[string]bool)
	for name, agent := range cfg.Agents {
		if !agent.Enabled {
			selected[name] = false
			continue
		}
		for _, hook := range agent.Hooks {
			if hook.Recall.Enabled || hook.Write.Enabled {
				selected[name] = true
				break
			}
		}
	}
	return selected
}

func promptProviderRouting(reader *bufio.Reader, writer io.Writer, cfg *config.Config, provider, label string) error {
	mode, err := promptSingleSelect(reader, writer, label+" provider mode", []setupOption{
		{ID: "read_write", Label: "read and write"},
		{ID: "read_only", Label: "read only"},
		{ID: "write_only", Label: "write only"},
	}, currentProviderMode(*cfg, provider))
	if err != nil {
		return err
	}
	policy, err := promptSingleSelect(reader, writer, label+" provider failure policy", []setupOption{
		{ID: "required", Label: "required"},
		{ID: "best_effort", Label: "best effort"},
	}, currentProviderPolicy(*cfg, provider))
	if err != nil {
		return err
	}
	setDefaultProviderMode(cfg, provider, mode, policy == "required")
	return nil
}

func currentProviderMode(cfg config.Config, provider string) string {
	canRead := recallProfileHasProvider(cfg.RecallProfiles["default"], provider)
	canWrite := writeProfileHasProvider(cfg.WriteProfiles["default"], provider)
	switch {
	case canRead && canWrite:
		return "read_write"
	case canRead:
		return "read_only"
	case canWrite:
		return "write_only"
	default:
		return "read_write"
	}
}

func currentProviderPolicy(cfg config.Config, provider string) string {
	required, ok := config.ProviderRouteRequired(cfg.RecallProfiles["default"].Providers, provider)
	if !ok {
		required, ok = config.ProviderRouteRequired(cfg.WriteProfiles["default"].Providers, provider)
	}
	if ok && !required {
		return "best_effort"
	}
	return "required"
}

func setDefaultProviderMode(cfg *config.Config, provider, mode string, required bool) {
	switch mode {
	case "read_only":
		upsertRecallRoute(cfg, provider, required)
		removeWriteRoute(cfg, provider)
	case "write_only":
		removeRecallRoute(cfg, provider)
		upsertWriteRoute(cfg, provider, required)
	default:
		upsertRecallRoute(cfg, provider, required)
		upsertWriteRoute(cfg, provider, required)
	}
}

func removeProviderFromDefaultProfiles(cfg *config.Config, provider string) {
	removeRecallRoute(cfg, provider)
	removeWriteRoute(cfg, provider)
}

func recallProfileHasProvider(profile config.RecallProfileConfig, provider string) bool {
	_, ok := config.ProviderRouteRequired(profile.Providers, provider)
	return ok
}

func writeProfileHasProvider(profile config.WriteProfileConfig, provider string) bool {
	_, ok := config.ProviderRouteRequired(profile.Providers, provider)
	return ok
}

func upsertRecallRoute(cfg *config.Config, provider string, required bool) {
	upsertRecallRouteInProfile(cfg, "default", provider, required)
	if _, ok := cfg.RecallProfiles["passive"]; !ok {
		cfg.RecallProfiles["passive"] = config.PassiveRecallProfileFrom(cfg.RecallProfiles["default"])
	}
	upsertRecallRouteInProfile(cfg, "passive", provider, required)
	if _, ok := cfg.RecallProfiles["passive_initial"]; !ok {
		cfg.RecallProfiles["passive_initial"] = config.PassiveInitialRecallProfileFrom(cfg.RecallProfiles["default"])
	}
	upsertRecallRouteInProfile(cfg, "passive_initial", provider, required)
}

func upsertRecallRouteInProfile(cfg *config.Config, profileName, provider string, required bool) {
	profile := cfg.RecallProfiles[profileName]
	profile.Providers = config.UpsertProviderRoute(profile.Providers, provider, required)
	cfg.RecallProfiles[profileName] = profile
}

func removeRecallRoute(cfg *config.Config, provider string) {
	for _, profileName := range []string{"default", "passive", "passive_initial"} {
		profile := cfg.RecallProfiles[profileName]
		profile.Providers = config.RemoveProviderRoute(profile.Providers, provider)
		cfg.RecallProfiles[profileName] = profile
	}
}

func upsertWriteRoute(cfg *config.Config, provider string, required bool) {
	for _, profileName := range []string{"default", "stm", "ltm"} {
		profile := cfg.WriteProfiles[profileName]
		profile.Providers = config.UpsertProviderRoute(profile.Providers, provider, required)
		cfg.WriteProfiles[profileName] = profile
	}
}

func removeWriteRoute(cfg *config.Config, provider string) {
	for _, profileName := range []string{"default", "stm", "ltm"} {
		profile := cfg.WriteProfiles[profileName]
		profile.Providers = config.RemoveProviderRoute(profile.Providers, provider)
		cfg.WriteProfiles[profileName] = profile
	}
}

func defaultSelections(options []setupOption, selected map[string]bool) map[string]bool {
	normalized := make(map[string]bool)
	for _, option := range options {
		normalized[option.ID] = selected[option.ID]
	}
	return normalized
}

func anySelected(selected map[string]bool) bool {
	for _, enabled := range selected {
		if enabled {
			return true
		}
	}
	return false
}

func sortedSelected(selected map[string]bool) []string {
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func promptBool(reader *bufio.Reader, writer io.Writer, question string, defaultValue bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultValue {
		suffix = " [Y/n]: "
	}
	for {
		fmt.Fprint(writer, question+suffix)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultValue, nil
		}
		switch value {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(writer, "Please answer yes or no.")
		}
		if errors.Is(err, io.EOF) {
			return defaultValue, nil
		}
	}
}

func promptSingleSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaultID string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", question)
	}
	defaultIndex := optionIndex(options, defaultID)
	if defaultIndex == -1 && len(options) > 0 {
		defaultIndex = 0
		defaultID = options[0].ID
	}
	for {
		fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if i == defaultIndex {
				marker = "[x]"
			}
			fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		fmt.Fprintf(writer, "Choose one [%d]: ", defaultIndex+1)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultID, nil
		}
		index, parseErr := strconv.Atoi(value)
		if parseErr == nil && index >= 1 && index <= len(options) {
			return options[index-1].ID, nil
		}
		for _, option := range options {
			if strings.EqualFold(value, option.ID) {
				return option.ID, nil
			}
		}
		fmt.Fprintln(writer, "Please choose one of the listed options.")
		if errors.Is(err, io.EOF) {
			return defaultID, nil
		}
	}
}

func promptMultiSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaults map[string]bool) (map[string]bool, error) {
	if len(options) == 0 {
		return map[string]bool{}, nil
	}
	defaultText := defaultSelectionText(options, defaults)
	for {
		fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if defaults[option.ID] {
				marker = "[x]"
			}
			fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		fmt.Fprintf(writer, "Choose numbers, comma-separated, or all/none [%s]: ", defaultText)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultSelections(options, defaults), nil
		}
		selected, parseErr := parseMultiSelect(value, options)
		if parseErr == nil {
			return selected, nil
		}
		fmt.Fprintf(writer, "%s\n", parseErr)
		if errors.Is(err, io.EOF) {
			return defaultSelections(options, defaults), nil
		}
	}
}

func parseMultiSelect(value string, options []setupOption) (map[string]bool, error) {
	selected := make(map[string]bool)
	for _, option := range options {
		selected[option.ID] = false
	}
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "all":
		for _, option := range options {
			selected[option.ID] = true
		}
		return selected, nil
	case "none":
		return selected, nil
	}
	parts := strings.Split(value, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 1 || index > len(options) {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		selected[options[index-1].ID] = true
	}
	return selected, nil
}

func defaultSelectionText(options []setupOption, defaults map[string]bool) string {
	var indexes []string
	for i, option := range options {
		if defaults[option.ID] {
			indexes = append(indexes, strconv.Itoa(i+1))
		}
	}
	if len(indexes) == 0 {
		return "none"
	}
	return strings.Join(indexes, ",")
}

func optionIndex(options []setupOption, id string) int {
	for i, option := range options {
		if option.ID == id {
			return i
		}
	}
	return -1
}

func promptString(reader *bufio.Reader, writer io.Writer, question, defaultValue string) (string, error) {
	fmt.Fprintf(writer, "%s [%s]: ", question, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func removeLegacyHookShim(configPath, target string) error {
	legacyPath := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", target+"-user_prompt")
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type hookInstallEvent struct {
	ConfigEvent string
	NativeEvent string
	Matcher     string
	Status      string
}

func installedHookEvents() []hookInstallEvent {
	return []hookInstallEvent{
		{
			ConfigEvent: "session_start",
			NativeEvent: "SessionStart",
			Matcher:     "startup|resume|clear|compact",
			Status:      "Buffering paxm session memory",
		},
		{
			ConfigEvent: "user_input",
			NativeEvent: "UserPromptSubmit",
			Status:      "Recalling paxm memory",
		},
		{
			ConfigEvent: "tool_use",
			NativeEvent: "PostToolUse",
			Status:      "Buffering paxm tool memory",
		},
		{
			ConfigEvent: "tool_failure",
			NativeEvent: "PostToolUseFailure",
			Status:      "Buffering failed paxm tool memory",
		},
		{
			ConfigEvent: "turn_end",
			NativeEvent: "Stop",
			Status:      "Buffering paxm turn memory",
		},
	}
}

func hookInstallEventsForAgent(agent config.AgentConfig) []hookInstallEvent {
	events := make([]hookInstallEvent, 0, len(agent.Hooks))
	for _, installEvent := range installedHookEvents() {
		hook, ok := agent.Hooks[installEvent.ConfigEvent]
		if ok && (hook.Recall.Enabled || hook.Write.Enabled) {
			events = append(events, installEvent)
		}
	}
	return events
}

func hookInstallEventByConfig(configEvent string) (hookInstallEvent, bool) {
	for _, event := range installedHookEvents() {
		if event.ConfigEvent == configEvent {
			return event, true
		}
	}
	return hookInstallEvent{}, false
}

func installHookShim(configPath, target, event string) (string, error) {
	hooksDir := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", err
	}
	installEvent, ok := hookInstallEventByConfig(event)
	if !ok {
		return "", fmt.Errorf("unsupported hook event %q", event)
	}
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	scriptPath := filepath.Join(hooksDir, target+"-"+event)
	outputFlag := " --json"
	if target == "claude" {
		outputFlag = ""
	}
	script := "#!/bin/sh\nexec " + shellQuote(binaryPath) + " --config " + shellQuote(config.ExpandPath(configPath)) + " __hook --target " + shellQuote(target) + " --event " + shellQuote(event) + outputFlag + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return "", err
	}
	if target == "codex" {
		if err := installCodexGlobalHook(codexConfigPath(), scriptPath, installEvent.ConfigEvent); err != nil {
			return "", err
		}
	}
	return scriptPath, nil
}

func codexConfigPath() string {
	if path := os.Getenv("PAXM_CODEX_CONFIG"); path != "" {
		return config.ExpandPath(path)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(".codex", "config.toml")
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(config.ExpandPath(codexHome), "config.toml")
}

func claudeSettingsPath() string {
	if path := os.Getenv("PAXM_CLAUDE_SETTINGS"); path != "" {
		return config.ExpandPath(path)
	}
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(".claude", "settings.json")
		}
		claudeConfigDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(config.ExpandPath(claudeConfigDir), "settings.json")
}

func piAgentDir() string {
	if path := os.Getenv("PAXM_PI_AGENT_DIR"); path != "" {
		return config.ExpandPath(path)
	}
	if path := os.Getenv("PI_CODING_AGENT_DIR"); path != "" {
		return config.ExpandPath(path)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".pi", "agent")
	}
	return filepath.Join(home, ".pi", "agent")
}

func piExtensionPath() string {
	return filepath.Join(piAgentDir(), "extensions", "paxm-hook", "index.ts")
}

func installPiGlobalHook(path string, scriptPaths map[string]string) error {
	userInputScriptPath := strings.TrimSpace(scriptPaths["user_input"])
	turnEndScriptPath := strings.TrimSpace(scriptPaths["turn_end"])
	if userInputScriptPath == "" && turnEndScriptPath == "" {
		return errors.New("pi hook requires at least one hook shim")
	}
	path = config.ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(piHookExtensionSource(userInputScriptPath, turnEndScriptPath)), 0o644)
}

func piHookExtensionSource(userInputScriptPath, turnEndScriptPath string) string {
	userInputScriptLiteral := jsonStringLiteral(config.ExpandPath(userInputScriptPath))
	turnEndScriptLiteral := jsonStringLiteral(config.ExpandPath(turnEndScriptPath))
	return `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawnSync } from "node:child_process";

const paxmUserInputHookCommand = ` + userInputScriptLiteral + `;
const paxmTurnEndHookCommand = ` + turnEndScriptLiteral + `;
const maxBufferedMessages = 20;

type BufferedMessage = {
  role: string;
  text: string;
  source: string;
};

let activeContext: any;
let lastPrompt = "";
let turnMessages: BufferedMessage[] = [];
const pendingToolArgs = new Map<string, any>();

function currentSessionId(ctx: any): string {
  const sessionFile = ctx.sessionManager?.getSessionFile?.();
  if (typeof sessionFile !== "string") return "";
  const fileName = sessionFile.split(/[\\/]/).pop() ?? "";
  const timestamped = fileName.match(/^\d{4}-\d{2}-\d{2}T[^_]+_(.+)\.jsonl$/i);
  if (timestamped?.[1]) return timestamped[1];
  return fileName.replace(/\.jsonl$/i, "");
}

function currentWorkspace(ctx: any): string {
  if (typeof ctx?.cwd === "string") return ctx.cwd;
  return "";
}

function activeCtx(ctx: any): any {
  if (ctx) activeContext = ctx;
  return ctx ?? activeContext ?? {};
}

function sanitizeValue(value: any): any {
  if (Array.isArray(value)) return value.map(sanitizeValue).filter((item) => item !== undefined);
  if (value && typeof value === "object") {
    const kind = String(value.type ?? "").toLowerCase();
    if (["thinking", "reasoning", "analysis", "redacted_thinking"].includes(kind)) return undefined;
    const result: Record<string, any> = {};
    for (const [key, item] of Object.entries(value)) {
      if (["thinking", "thinking_content", "reasoning", "reasoning_content", "analysis", "chain_of_thought", "thought", "thoughts", "redacted_thinking"].includes(key.toLowerCase())) continue;
      const clean = sanitizeValue(item);
      if (clean !== undefined) result[key] = clean;
    }
    return result;
  }
  return value;
}

function valueText(value: any): string {
  value = sanitizeValue(value);
  if (typeof value === "string") return value.trim();
  if (value === undefined || value === null) return "";
  try { return JSON.stringify(value); } catch { return ""; }
}

function contentMessages(role: string, content: any, source: string): BufferedMessage[] {
  if (role.toLowerCase() === "toolresult") return [];
  if (typeof content === "string") return [{ role, text: content, source }];
  if (!Array.isArray(content)) return [];
  const messages: BufferedMessage[] = [];
  for (const part of content) {
    if (typeof part === "string") {
      messages.push({ role, text: part, source });
      continue;
    }
    const kind = String(part?.type ?? "").toLowerCase();
    if (["thinking", "reasoning", "analysis", "redacted_thinking"].includes(kind)) continue;
    if (["toolcall", "tool_use", "tool_call", "function_call", "toolresult", "tool_result", "tool_response", "function_call_output", "function_result"].includes(kind)) continue;
    const text = valueText(part?.text ?? part?.content);
    if (text !== "") messages.push({ role, text, source });
  }
  return messages;
}

function appendBufferedMessage(role: string, text: string, source: string): void {
  const trimmed = text.trim();
  if (trimmed === "") return;
  const last = turnMessages[turnMessages.length - 1];
  if (last?.role === role && last.text === trimmed) return;
  turnMessages.push({ role, text: trimmed, source });
  if (turnMessages.length > maxBufferedMessages) {
    turnMessages = turnMessages.slice(-maxBufferedMessages);
  }
}

function appendPiMessage(message: any, source: string): void {
  const role = typeof message?.role === "string" ? message.role : "unknown";
  if (role.toLowerCase() === "toolresult") return;
  const messages = contentMessages(role, message?.content, source);
  if (messages.length === 0 && typeof message?.text === "string") {
    appendBufferedMessage(role, message.text, source);
    return;
  }
  for (const item of messages) appendBufferedMessage(item.role, item.text, item.source);
}

function runPaxmHook(command: string, payload: unknown, ctx: any, notifyOnFailure: boolean): { ok: boolean; stdout: string } {
  const result = spawnSync(command, [], {
    input: JSON.stringify(payload) + "\n",
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
  });

  if (result.error) {
    if (notifyOnFailure) ctx?.ui?.notify?.(` + "`" + `paxm hook failed: ${result.error.message}` + "`" + `, "warning");
    return { ok: false, stdout: "" };
  }
  if (result.status !== 0) {
    if (notifyOnFailure) {
      const detail = (result.stderr || result.stdout || "Unknown paxm hook failure.").trim();
      ctx?.ui?.notify?.(` + "`" + `paxm hook failed: ${detail}` + "`" + `, "warning");
    }
    return { ok: false, stdout: result.stdout ?? "" };
  }

  return { ok: true, stdout: result.stdout ?? "" };
}

function flushTurn(triggerEvent: string, event: any, ctx: any): void {
  if (paxmTurnEndHookCommand === "") return;
  const resolvedCtx = activeCtx(ctx);
  const messages = turnMessages;
  if (messages.length === 0 && lastPrompt.trim() === "") return;
  turnMessages = [];

  const payload = {
    schema_version: "paxm.pi.turn_end.v1",
    target: "pi",
    event: "turn_end",
    agent: "pi",
    session_id: currentSessionId(resolvedCtx),
    cwd: currentWorkspace(resolvedCtx),
    workspace: currentWorkspace(resolvedCtx),
    prompt: lastPrompt,
    source: "pi",
    trigger_event: triggerEvent,
    messages,
    metadata: {
      pi_event: triggerEvent,
      message_count: String(messages.length),
    },
  };

  runPaxmHook(paxmTurnEndHookCommand, payload, resolvedCtx, false);
  lastPrompt = "";
}

function formatPaxmRecall(raw: string): string {
  if (raw.trim() === "") return "";
  try {
    const result = JSON.parse(raw);
    if (result?.skipped || !result?.recall?.hits?.length) return "";
    const lines = ["paxm memory recall:"];
    for (const hit of result.recall.hits) {
      const score = typeof hit.score === "number" ? hit.score.toFixed(4) : "n/a";
      const provider = hit.provider ? String(hit.provider) : "unknown";
      const text = hit.text ? escapePaxmRecallText(String(hit.text).trim()) : "";
      if (text === "") continue;
      lines.push("- [" + provider + " score=" + score + "] " + text);
    }
    return lines.length > 1
      ? '<paxm-recall version="1" mode="passive">\n' + lines.join("\n") + "\n</paxm-recall>"
      : "";
  } catch {
    const text = escapePaxmRecallText(raw.trim());
    if (text === "" || text.includes("<paxm-recall")) return text;
    return '<paxm-recall version="1" mode="passive">\n' + text + "\n</paxm-recall>";
  }
}

function escapePaxmRecallText(text: string): string {
  return text
    .split("</paxm-recall>").join("&lt;/paxm-recall&gt;")
    .split("<paxm-recall").join("&lt;paxm-recall");
}

export default function (pi: ExtensionAPI) {
  const onRuntimeEvent = pi.on as unknown as (event: string, handler: (event: any, ctx: any) => unknown) => void;

  onRuntimeEvent("session_start", (_event, ctx) => {
    activeContext = ctx;
    lastPrompt = "";
    turnMessages = [];
    pendingToolArgs.clear();
  });

  onRuntimeEvent("message_end", (event, ctx) => {
    if (paxmTurnEndHookCommand === "") return;
    activeCtx(ctx);
    appendPiMessage(event?.message, "message_end");
  });

  onRuntimeEvent("tool_execution_start", (event, ctx) => {
    if (paxmTurnEndHookCommand === "") return;
    activeCtx(ctx);
    const toolCallId = valueText(event?.toolCallId);
    if (toolCallId !== "") pendingToolArgs.set(toolCallId, event?.args);
  });

  onRuntimeEvent("tool_execution_end", (event, ctx) => {
    if (paxmTurnEndHookCommand === "") return;
    activeCtx(ctx);
    const name = valueText(event?.toolName);
    const toolCallId = valueText(event?.toolCallId);
    const args = valueText(pendingToolArgs.get(toolCallId));
    if (toolCallId !== "") pendingToolArgs.delete(toolCallId);
    appendBufferedMessage("tool_call", [name, args].filter(Boolean).join(" "), "tool_execution_end");
    const result = valueText(event?.result);
    if (result !== "") appendBufferedMessage("tool_result", event?.isError ? "Error: " + result : result, "tool_execution_end");
  });

  onRuntimeEvent("agent_end", (event, ctx) => {
    if (paxmTurnEndHookCommand === "") return;
    flushTurn("agent_end", event, ctx);
    pendingToolArgs.clear();
  });

  onRuntimeEvent("session_shutdown", (event, ctx) => {
    if (paxmTurnEndHookCommand === "") return;
    flushTurn("session_shutdown", event, ctx);
    lastPrompt = "";
    turnMessages = [];
    pendingToolArgs.clear();
  });

  pi.on("before_agent_start", async (event, ctx) => {
    const resolvedCtx = activeCtx(ctx);
    lastPrompt = typeof event.prompt === "string" ? event.prompt : "";
    if (paxmTurnEndHookCommand !== "") {
      appendBufferedMessage("user", lastPrompt, "before_agent_start");
    }
    if (paxmUserInputHookCommand === "") return;

    const payload = {
      schema_version: "paxm.pi.user_input.v1",
      target: "pi",
      event: "user_input",
      agent: "pi",
      session_id: currentSessionId(resolvedCtx),
      cwd: currentWorkspace(resolvedCtx),
      workspace: currentWorkspace(resolvedCtx),
      prompt: event.prompt,
      source: "pi",
    };

    const result = runPaxmHook(paxmUserInputHookCommand, payload, resolvedCtx, true);
    if (!result.ok) return;

    const content = formatPaxmRecall(result.stdout);
    if (content === "") return;

    return {
      message: {
        customType: "paxm-memory-recall",
        content,
        display: true,
        details: {
          source: "paxm",
          event: "user_input",
        },
      },
    };
  });
}
`
}

func jsonStringLiteral(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func installCodexGlobalHook(path, scriptPath, configEvent string) error {
	path = config.ExpandPath(path)
	installEvent, ok := hookInstallEventByConfig(configEvent)
	if !ok {
		return fmt.Errorf("unsupported Codex hook event %q", configEvent)
	}
	command := shellQuote(scriptPath)
	commandHook := `{ type = "command", command = "` + escapeTomlString(command) + `", async = false, statusMessage = "` + escapeTomlString(installEvent.Status) + `" }`
	entry := `{ hooks = [` + commandHook + `] }`
	if installEvent.Matcher != "" {
		entry = `{ matcher = "` + escapeTomlString(installEvent.Matcher) + `", hooks = [` + commandHook + `] }`
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content, prunedLegacy := pruneLegacyCodexUserPromptHook(string(contentBytes))
	if strings.Contains(content, scriptPath) || strings.Contains(content, command) {
		if prunedLegacy {
			return writeCodexConfig(path, contentBytes, content)
		}
		return nil
	}

	updated := upsertCodexHook(content, installEvent.NativeEvent, entry)
	return writeCodexConfig(path, contentBytes, updated)
}

type claudeHookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type claudeHookGroup struct {
	Matcher string              `json:"matcher,omitempty"`
	Hooks   []claudeHookHandler `json:"hooks"`
}

func installClaudeGlobalHooks(path string, scriptPaths map[string]string) error {
	path = config.ExpandPath(path)
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	settings := make(map[string]json.RawMessage)
	if len(bytesTrimSpace(original)) > 0 {
		if err := json.Unmarshal(original, &settings); err != nil {
			return fmt.Errorf("decode Claude Code settings %s: %w", path, err)
		}
	}
	hooks := make(map[string][]json.RawMessage)
	if rawHooks := settings["hooks"]; len(bytesTrimSpace(rawHooks)) > 0 && string(bytesTrimSpace(rawHooks)) != "null" {
		if err := json.Unmarshal(rawHooks, &hooks); err != nil {
			return fmt.Errorf("decode Claude Code hooks %s: %w", path, err)
		}
	}
	changed := false
	hasHook := false
	for _, installEvent := range installedHookEvents() {
		scriptPath := strings.TrimSpace(scriptPaths[installEvent.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		hasHook = true
		command := shellQuote(scriptPath)
		alreadyInstalled := false
		for _, rawGroup := range hooks[installEvent.NativeEvent] {
			if claudeHookGroupHasCommand(rawGroup, command, scriptPath) {
				alreadyInstalled = true
				break
			}
		}
		if alreadyInstalled {
			continue
		}
		group := claudeHookGroup{
			Matcher: installEvent.Matcher,
			Hooks: []claudeHookHandler{{
				Type:    "command",
				Command: command,
				Timeout: 60,
			}},
		}
		groupBytes, err := json.Marshal(group)
		if err != nil {
			return err
		}
		hooks[installEvent.NativeEvent] = append(hooks[installEvent.NativeEvent], groupBytes)
		changed = true
	}
	if !hasHook {
		return errors.New("Claude Code hook requires at least one hook shim")
	}
	if !changed {
		return nil
	}
	hooksBytes, err := json.Marshal(hooks)
	if err != nil {
		return err
	}
	settings["hooks"] = hooksBytes
	return writeClaudeSettings(path, original, settings)
}

func claudeHookGroupHasCommand(rawGroup json.RawMessage, command, scriptPath string) bool {
	var group struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(rawGroup, &group); err != nil {
		return false
	}
	for _, hook := range group.Hooks {
		if hook.Command == command || hook.Command == scriptPath || strings.Contains(hook.Command, scriptPath) {
			return true
		}
	}
	return false
}

func writeClaudeSettings(path string, original []byte, settings map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(original) > 0 {
		backupPath := path + ".paxm.bak"
		if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backupPath, original, 0o600); err != nil {
				return err
			}
		}
	}
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	return os.WriteFile(path, updated, 0o600)
}

func writeCodexConfig(path string, original []byte, updated string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(original) > 0 {
		backupPath := path + ".paxm.bak"
		if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backupPath, original, 0o600); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func pruneLegacyCodexUserPromptHook(content string) (string, bool) {
	if !strings.Contains(content, "codex-user_prompt") {
		return content, false
	}
	lines := strings.SplitAfter(content, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "UserPromptSubmit = ") {
			next := removeInlineTomlArrayEntries(line, "codex-user_prompt")
			if next != line {
				lines[i] = next
				changed = true
			}
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, ""), true
}

func upsertCodexHook(content, eventName, entry string) string {
	if content == "" {
		return "[hooks]\n" + eventName + " = [" + entry + "]\n"
	}
	lines := strings.SplitAfter(content, "\n")
	hooksStart := -1
	hooksEnd := len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[hooks]" {
			hooksStart = i
			continue
		}
		if hooksStart != -1 && i > hooksStart && strings.HasPrefix(trimmed, "[") {
			hooksEnd = i
			break
		}
	}
	if hooksStart == -1 {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + "\n[hooks]\n" + eventName + " = [" + entry + "]\n"
	}
	for i := hooksStart + 1; i < hooksEnd; i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), eventName+" = ") {
			lines[i] = appendInlineTomlArray(line, entry)
			return strings.Join(lines, "")
		}
	}
	newLine := eventName + " = [" + entry + "]\n"
	updated := append([]string{}, lines[:hooksStart+1]...)
	updated = append(updated, newLine)
	updated = append(updated, lines[hooksStart+1:]...)
	return strings.Join(updated, "")
}

func removeInlineTomlArrayEntries(line, marker string) string {
	newline := ""
	if strings.HasSuffix(line, "\n") {
		newline = "\n"
		line = strings.TrimSuffix(line, "\n")
	}
	start := strings.Index(line, "[")
	end := strings.LastIndex(line, "]")
	if start == -1 || end <= start {
		return line + newline
	}
	prefix := line[:start+1]
	body := line[start+1 : end]
	suffix := line[end:]
	entries := splitTopLevelInlineEntries(body)
	filtered := entries[:0]
	changed := false
	for _, entry := range entries {
		if strings.Contains(entry, marker) {
			changed = true
			continue
		}
		filtered = append(filtered, entry)
	}
	if !changed {
		return line + newline
	}
	return prefix + strings.Join(filtered, ", ") + suffix + newline
}

func splitTopLevelInlineEntries(body string) []string {
	var entries []string
	start := 0
	depth := 0
	inString := false
	escaped := false
	for i, char := range body {
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				entries = append(entries, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(body[start:]) != "" {
		entries = append(entries, strings.TrimSpace(body[start:]))
	}
	return entries
}

func appendInlineTomlArray(line, entry string) string {
	newline := ""
	if strings.HasSuffix(line, "\n") {
		newline = "\n"
		line = strings.TrimSuffix(line, "\n")
	}
	index := strings.LastIndex(line, "]")
	if index == -1 {
		return line + newline
	}
	prefix := strings.TrimRight(line[:index], " ")
	suffix := line[index:]
	if strings.HasSuffix(prefix, "[") {
		return prefix + entry + suffix + newline
	}
	return prefix + ", " + entry + suffix + newline
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func escapeTomlString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(value)
}

func extractConfigFlag(args []string) ([]string, string, error) {
	var filtered []string
	var configPath string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-c":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s requires a path", arg)
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(arg, "--config="):
			configPath = strings.TrimPrefix(arg, "--config=")
		default:
			filtered = append(filtered, arg)
		}
	}
	return filtered, configPath, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeRecallMarkdown(w io.Writer, result facade.RecallResult) {
	if len(result.Hits) == 0 {
		fmt.Fprintln(w, "No memories found.")
		return
	}
	for i, hit := range result.Hits {
		fmt.Fprintf(w, "### Memory %d (%s)\n", i+1, hit.Provider)
		fmt.Fprintf(w, "Score: %.4f\n", hit.Score)
		fmt.Fprintf(w, "Relevance: %.4f\n", hit.Relevance)
		if hit.RawScore != nil {
			if hit.RawScoreKind != "" {
				fmt.Fprintf(w, "Raw score: %.4f (%s)\n", *hit.RawScore, hit.RawScoreKind)
			} else {
				fmt.Fprintf(w, "Raw score: %.4f\n", *hit.RawScore)
			}
		}
		if hit.Source != "" {
			fmt.Fprintf(w, "Source: %s\n\n", hit.Source)
		} else {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, strings.TrimSpace(hit.Text))
		fmt.Fprintln(w)
	}
}

func writeRecallContextMarkdown(w io.Writer, result facade.RecallResult, mode string) {
	if len(result.Hits) == 0 {
		writeRecallMarkdown(w, result)
		return
	}
	var context bytes.Buffer
	writeRecallMarkdown(&context, result)
	fmt.Fprintln(w, facade.WrapRecallContext(mode, context.String()))
}

func writeHistorySummary(w io.Writer, summary telemetry.HistorySummary) {
	fmt.Fprintln(w, "== paxm history ==")
	fmt.Fprintf(w, "window: last %d days\n", summary.Days)
	if summary.Totals.Events == 0 {
		fmt.Fprintln(w, "status: quiet")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "no telemetry events recorded yet")
		writeHistoryTable(w, "storage", []string{"file", "path"}, [][]string{
			{"events", summary.Storage.EventsFile},
			{"metrics", summary.Storage.MetricsFile},
		})
		return
	}
	fmt.Fprintf(w, "status: %s\n", historyStatus(summary.Totals))
	writeHistoryTable(w, "overview", []string{"metric", "value"}, [][]string{
		{"events", formatInt(summary.Totals.Events)},
		{"successes", formatInt(summary.Totals.Successes)},
		{"errors", formatInt(summary.Totals.Errors)},
		{"skipped", formatInt(summary.Totals.Skipped)},
		{"provider_errors", formatInt(summary.Totals.ProviderErrors)},
		{"recall_timeouts", formatInt(summary.Totals.RecallTimeouts)},
	})
	writeHistoryTable(w, "recall funnel", []string{"recalls", "hits", "inserted", "insert_rate"}, [][]string{{
		formatInt(summary.Totals.Recalls),
		formatInt(summary.Totals.Hits),
		formatInt(summary.Totals.Inserted),
		formatPercent(summary.Totals.Inserted, summary.Totals.Hits),
	}})
	providerWrites := sumNamedCounters(summary.Providers, func(counter telemetry.Counter) int { return counter.Writes })
	providerRefs := sumNamedCounters(summary.Providers, func(counter telemetry.Counter) int { return counter.Refs })
	writeHistoryTable(w, "write pipeline", []string{"write_events", "items", "provider_writes", "provider_refs", "flushes", "provider_ref_rate"}, [][]string{{
		formatInt(summary.Totals.Writes),
		formatInt(summary.Totals.Items),
		formatInt(providerWrites),
		formatInt(providerRefs),
		formatInt(summary.Totals.Flushes),
		formatPercent(providerRefs, providerWrites),
	}})
	writeHistoryTable(w, "storage", []string{"events_bytes", "total_bytes", "max_bytes", "files"}, [][]string{{
		formatInt64(summary.Storage.EventBytes),
		formatInt64(summary.Storage.TotalBytes),
		formatInt64(summary.Storage.MaxBytes),
		formatInt(summary.Storage.MaxFiles),
	}})
	if len(summary.Daily) > 0 {
		rows := make([][]string, 0, len(summary.Daily))
		for _, day := range summary.Daily {
			rows = append(rows, []string{
				day.Date,
				formatInt(day.Counter.Recalls),
				formatInt(day.Counter.Hits),
				formatInt(day.Counter.Inserted),
				formatInt(day.Counter.Writes),
				formatInt(day.Counter.Errors),
			})
		}
		writeHistoryTable(w, "by day", []string{"date", "recalls", "hits", "inserted", "writes", "errors"}, rows)
	}
	writeNamedCounters(w, "by profile", []string{"profile", "recalls", "hits", "inserted", "writes", "errors"}, summary.Profiles, func(counter telemetry.Counter) []string {
		return []string{formatInt(counter.Recalls), formatInt(counter.Hits), formatInt(counter.Inserted), formatInt(counter.Writes), formatInt(counter.Errors)}
	})
	writeNamedCounters(w, "by agent", []string{"agent", "passive_recalls", "passive_writes", "inserted", "flushes", "errors"}, summary.Agents, func(counter telemetry.Counter) []string {
		return []string{formatInt(counter.Recalls), formatInt(counter.Writes), formatInt(counter.Inserted), formatInt(counter.Flushes), formatInt(counter.Errors)}
	})
	writeNamedCounters(w, "by hook", []string{"hook", "recalls", "inserted", "writes", "flushes", "errors"}, summary.HookEvents, func(counter telemetry.Counter) []string {
		return []string{formatInt(counter.Recalls), formatInt(counter.Inserted), formatInt(counter.Writes), formatInt(counter.Flushes), formatInt(counter.Errors)}
	})
	writeNamedCounters(w, "by provider", []string{"provider", "recalls", "hits", "avg_recall", "p95_recall", "recall_timeouts", "bulkhead_skips", "writes", "refs", "avg_write", "avg_passive_latency", "provider_errors"}, summary.Providers, func(counter telemetry.Counter) []string {
		return []string{formatInt(counter.Recalls), formatInt(counter.Hits), formatAverageMS(counter.ProviderRecallDurationMS, counter.ProviderRecallSamples), formatDurationMS(telemetry.ProviderRecallP95MS(counter)), formatInt(counter.ProviderRecallTimeouts), formatInt(counter.ProviderRecallBulkheadSkips), formatInt(counter.Writes), formatInt(counter.Refs), formatAverageMS(counter.ProviderWriteDurationMS, counter.ProviderWriteSamples), formatAverageMS(counter.PassiveWriteLatencyTotalMS, counter.PassiveWriteSamples), formatInt(counter.ProviderErrors)}
	})
}

func writeLogEvent(w io.Writer, event telemetry.Event) {
	status := "OK"
	if event.Skipped {
		status = "SKIP"
	} else if !event.Success {
		status = "ERROR"
	}
	kind := firstNonEmpty(event.Kind, "event")
	parts := []string{event.Time.UTC().Format(time.RFC3339), status, kind}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "command", value: event.Command},
		{name: "source", value: event.Source},
		{name: "target", value: event.Target},
		{name: "hook_event", value: event.HookEvent},
		{name: "profile", value: event.Profile},
		{name: "episode_id", value: event.EpisodeID},
		{name: "session_key", value: event.SessionKey},
		{name: "provider", value: event.Provider},
	} {
		if strings.TrimSpace(field.value) != "" {
			parts = append(parts, field.name+"="+formatLogValue(field.value))
		}
	}
	for _, field := range []struct {
		name  string
		value int
	}{
		{name: "hits", value: event.HitCount},
		{name: "inserted", value: event.InsertedCount},
		{name: "items", value: event.ItemCount},
		{name: "refs", value: event.RefCount},
		{name: "flushed", value: event.Flushed},
		{name: "provider_errors", value: len(event.ProviderErrorDetails)},
	} {
		if field.value > 0 {
			parts = append(parts, field.name+"="+strconv.Itoa(field.value))
		}
	}
	if event.DurationMS > 0 {
		parts = append(parts, "duration_ms="+strconv.FormatInt(event.DurationMS, 10))
	}
	if event.ProviderDurationMS > 0 {
		parts = append(parts, "provider_duration_ms="+strconv.FormatInt(event.ProviderDurationMS, 10))
	}
	if event.PassiveWriteLatencyTotalMS > 0 {
		parts = append(parts, "passive_write_latency_total_ms="+strconv.FormatInt(event.PassiveWriteLatencyTotalMS, 10))
	}
	if event.Error != "" {
		parts = append(parts, "error="+strconv.Quote(event.Error))
	}
	fmt.Fprintln(w, strings.Join(parts, " "))
}

func formatLogValue(value string) string {
	if strings.ContainsAny(value, " \t\r\n\"") {
		return strconv.Quote(value)
	}
	return value
}

func writeNamedCounters(w io.Writer, title string, headers []string, counters []telemetry.NamedCounter, values func(telemetry.Counter) []string) {
	if len(counters) == 0 {
		return
	}
	rows := make([][]string, 0, len(counters))
	for _, counter := range counters {
		rows = append(rows, append([]string{counter.Name}, values(counter.Counter)...))
	}
	writeHistoryTable(w, title, headers, rows)
}

func writeHistoryTable(w io.Writer, title string, headers []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, title)
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = len(header)
	}
	for _, row := range rows {
		for i := range headers {
			if i < len(row) && len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}
	writeHistoryRow(w, headers, widths)
	separator := make([]string, len(headers))
	for i, width := range widths {
		separator[i] = strings.Repeat("-", width)
	}
	writeHistoryRow(w, separator, widths)
	for _, row := range rows {
		writeHistoryRow(w, row, widths)
	}
}

func writeHistoryRow(w io.Writer, row []string, widths []int) {
	for i, width := range widths {
		value := ""
		if i < len(row) {
			value = row[i]
		}
		if i == 0 {
			fmt.Fprintf(w, "  %-*s", width, value)
			continue
		}
		fmt.Fprintf(w, "  %*s", width, value)
	}
	fmt.Fprintln(w)
}

func historyStatus(counter telemetry.Counter) string {
	if counter.Errors > 0 || counter.ProviderErrors > 0 {
		return "attention"
	}
	if counter.Skipped > 0 {
		return "partial"
	}
	return "ok"
}

func sumNamedCounters(counters []telemetry.NamedCounter, value func(telemetry.Counter) int) int {
	total := 0
	for _, counter := range counters {
		total += value(counter.Counter)
	}
	return total
}

func formatPercent(numerator, denominator int) string {
	if denominator == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", float64(numerator)*100/float64(denominator))
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func formatAverageMS(total int64, samples int) string {
	if samples == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1fms", float64(total)/float64(samples))
}

func formatDurationMS(value int64) string {
	if value == 0 {
		return "n/a"
	}
	return strconv.FormatInt(value, 10) + "ms"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
