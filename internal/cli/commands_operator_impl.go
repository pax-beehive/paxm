package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters"
	jsonrpcadapter "github.com/pax-beehive/paxm/internal/adapters/jsonrpc"
	jsonrpcconformance "github.com/pax-beehive/paxm/internal/adapters/jsonrpc/conformance"
	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/config"
	paxeval "github.com/pax-beehive/paxm/internal/eval"
	"github.com/pax-beehive/paxm/internal/memory"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

func (r runner) runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	force := fs.Bool("force", false, "overwrite an existing config")
	yes := fs.Bool("yes", false, "accept default setup answers")
	userID := fs.String("user-id", "", "stable user identity")
	teamIDs := fs.String("team-id", "", "comma-separated team scope IDs")
	integration := fs.String("integration", config.IntegrationOwnerPaxm, "hook owner: paxm, codex-plugin, or claude-plugin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *integration != config.IntegrationOwnerPaxm && *integration != config.IntegrationOwnerCodexPlugin && *integration != config.IntegrationOwnerClaudePlugin {
		return fmt.Errorf("unsupported setup integration %q", *integration)
	}

	path := r.configFile()
	prompter := newSetupPrompter(r.stdin, r.stdout)
	selection, proceed, err := r.prepareSetup(path, prompter, *force, *yes, *integration, *userID, *teamIDs)
	if err != nil || !proceed {
		return err
	}
	cfg, selectedHooks := selection.cfg, selection.selectedHooks
	if err := preflightSelectedHookIntegrations(path, cfg, selectedHooks); err != nil {
		return err
	}
	zepUserResult, err := r.maybeEnsureZepUser(context.Background(), cfg)
	if err != nil {
		return err
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	if err := flushExistingHookBuffer(path, true); err != nil {
		_, _ = fmt.Fprintf(r.stderr, "warning: config saved but existing hook daemon could not be stopped: %v\n", err)
	}
	_, _ = fmt.Fprintf(r.stdout, "saved config: %s\n", path)
	if zepUserResult != nil {
		status := "exists"
		if zepUserResult.Created {
			status = "created"
		}
		_, _ = fmt.Fprintf(r.stdout, "ensured Zep user: %s (%s)\n", zepUserResult.UserID, status)
	}
	return r.installSelectedHookIntegrations(path, cfg, selectedHooks)
}

func preflightSelectedHookIntegrations(path string, cfg config.Config, selectedHooks map[string]bool) error {
	for _, name := range sortedSelected(selectedHooks) {
		if !selectedHooks[name] || !isRequestedAgent(name) {
			continue
		}
		if err := preflightRequestedNativeHooks(name, hookInstallEventsForNamedAgent(name, cfg.Agents[name])); err != nil {
			return fmt.Errorf("preflight %s integration: %w", name, err)
		}
		if err := preflightAgentMCP(name); err != nil {
			return fmt.Errorf("preflight %s MCP integration: %w", name, err)
		}
	}
	return nil
}

type setupSelection struct {
	cfg           config.Config
	selectedHooks map[string]bool
}

func (r runner) prepareSetup(path string, prompter *setupPrompter, force, yes bool, integration, userID, teamIDs string) (setupSelection, bool, error) {
	configExists, proceed, err := r.confirmSetupOverwrite(path, prompter, force, yes)
	if err != nil || !proceed {
		return setupSelection{}, false, err
	}
	cfg, err := setupBaseConfig(path, configExists)
	if err != nil {
		return setupSelection{}, false, err
	}
	selectedProviders := defaultSelections(providerOptions(cfg), cfgProviderEnabled(cfg))
	selectedHooks := defaultSelections(hookOptions(cfg), cfgHookEnabled(cfg))
	pluginTarget := setupPluginTarget(integration)
	previousEnabled := enabledAgents(cfg)
	if strings.TrimSpace(userID) != "" {
		cfg.Identity.UserID = config.SlugID(userID)
		if cfg.Identity.UserID == "" {
			return setupSelection{}, false, errors.New("setup user ID must contain letters or numbers")
		}
	}
	ensureSetupIdentity(&cfg)
	if strings.TrimSpace(teamIDs) != "" {
		if err := configureTeamWriteProfiles(&cfg, teamIDs); err != nil {
			return setupSelection{}, false, err
		}
	}
	constrainPluginHooks(selectedHooks, pluginTarget)
	if !yes {
		selectedProviders, selectedHooks, proceed, err = r.promptSetupSelections(prompter, &cfg, selectedProviders, selectedHooks)
		if err != nil || !proceed {
			return setupSelection{}, false, err
		}
	}
	constrainPluginHooks(selectedHooks, pluginTarget)
	if !anySelected(selectedProviders) {
		return setupSelection{}, false, errors.New("setup requires at least one memory provider")
	}
	applySetupSelections(&cfg, selectedProviders, selectedHooks, yes)
	applySetupIntegration(&cfg, integration, pluginTarget, previousEnabled)
	if !yes {
		proceed, err = r.confirmSetupSummary(prompter, cfg, selectedProviders, selectedHooks)
		if err != nil || !proceed {
			return setupSelection{}, false, err
		}
	}
	return setupSelection{cfg: cfg, selectedHooks: selectedHooks}, true, nil
}

func setupPluginTarget(integration string) string {
	switch integration {
	case config.IntegrationOwnerCodexPlugin:
		return "codex"
	case config.IntegrationOwnerClaudePlugin:
		return "claude"
	default:
		return ""
	}
}

func enabledAgents(cfg config.Config) map[string]bool {
	result := make(map[string]bool, len(cfg.Agents))
	for name, agent := range cfg.Agents {
		result[name] = agent.Enabled
	}
	return result
}

func constrainPluginHooks(selected map[string]bool, target string) {
	if target == "" {
		return
	}
	for name := range selected {
		selected[name] = name == target
	}
}

func applySetupIntegration(cfg *config.Config, integration, pluginTarget string, previousEnabled map[string]bool) {
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
		if integration == config.IntegrationOwnerPaxm || integration == config.IntegrationOwnerCodexPlugin {
			agent.Integration.Owner = integration
		}
		cfg.Agents["codex"] = agent
	}
	if agent, ok := cfg.Agents["claude"]; ok && integration == config.IntegrationOwnerClaudePlugin {
		agent.Integration.Owner = integration
		cfg.Agents["claude"] = agent
	}
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
		_, _ = fmt.Fprintln(r.stdout, "setup cancelled")
		return configExists, false, nil
	}
	return configExists, true, nil
}

func (r runner) promptSetupSelections(prompter *setupPrompter, cfg *config.Config, selectedProviders, selectedHooks map[string]bool) (map[string]bool, map[string]bool, bool, error) {
	var err error
	if prompter.interactive {
		if err := promptSetupIdentity(prompter, cfg); err != nil {
			return nil, nil, false, r.finishSetupPrompt(err)
		}
	}
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

func ensureSetupIdentity(cfg *config.Config) {
	if strings.TrimSpace(cfg.Identity.UserID) == "" {
		cfg.Identity.UserID = config.SlugID(firstNonEmpty(os.Getenv("USER"), "user"))
	}
}

func promptSetupIdentity(prompter *setupPrompter, cfg *config.Config) error {
	ensureSetupIdentity(cfg)
	userID, err := prompter.text("User ID", cfg.Identity.UserID)
	if err != nil {
		return err
	}
	userID = config.SlugID(userID)
	if userID == "" {
		return errors.New("setup requires a user ID")
	}
	cfg.Identity.UserID = userID
	teamIDs, err := prompter.text("Team IDs (comma-separated, optional)", configuredTeamIDs(*cfg))
	if err != nil {
		return err
	}
	if strings.TrimSpace(teamIDs) == "" {
		return nil
	}
	return configureTeamWriteProfiles(cfg, teamIDs)
}

func configureTeamWriteProfiles(cfg *config.Config, values string) error {
	base, ok := cfg.WriteProfiles["ltm"]
	if !ok {
		base = cfg.WriteProfiles["default"]
	}
	seen := make(map[string]bool)
	for _, value := range strings.Split(values, ",") {
		teamID := config.SlugID(value)
		if teamID == "" {
			return fmt.Errorf("team ID %q must contain letters or numbers", strings.TrimSpace(value))
		}
		if seen[teamID] {
			continue
		}
		seen[teamID] = true
		profile := base
		profile.Tier = "ltm"
		profile.ExpiresAfter = ""
		profile.Scope = config.MemoryScopeConfig{Type: "team", ID: teamID}
		cfg.WriteProfiles["team-"+teamID] = profile
	}
	return nil
}

func configuredTeamIDs(cfg config.Config) string {
	var ids []string
	seen := make(map[string]bool)
	for _, profile := range cfg.WriteProfiles {
		if profile.Scope.Type == "team" && profile.Scope.ID != "" && !seen[profile.Scope.ID] {
			ids = append(ids, profile.Scope.ID)
			seen[profile.Scope.ID] = true
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
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
		_, _ = fmt.Fprintln(r.stdout, "setup cancelled")
		return false, nil
	}
	return true, nil
}

func (r runner) installSelectedHookIntegrations(path string, cfg config.Config, selectedHooks map[string]bool) error {
	for _, name := range sortedSelected(selectedHooks) {
		if !selectedHooks[name] {
			continue
		}
		if handled, err := r.reconcilePluginOwnership(path, cfg, name); err != nil {
			return err
		} else if handled {
			continue
		}
		if err := r.installAgentHookIntegration(path, cfg.Agents[name], name); err != nil {
			return err
		}
	}
	return nil
}

func (r runner) reconcilePluginOwnership(path string, cfg config.Config, name string) (bool, error) {
	agent, ok := cfg.Agents[name]
	if !ok {
		return false, nil
	}
	owner := strings.ToLower(agent.Integration.Owner)
	switch {
	case name == "codex" && owner == config.IntegrationOwnerCodexPlugin:
		marker := filepath.Join(filepath.Dir(config.ExpandPath(path)), "hooks", "codex-")
		if err := removeCodexGlobalHooks(codexConfigPath(), marker); err != nil {
			return true, err
		}
		if err := removeAgentHookShims(path, name); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(r.stdout, "Codex hooks are owned by the paxm-memory plugin")
		return true, nil
	case name == "claude" && owner == config.IntegrationOwnerClaudePlugin:
		marker := filepath.Join(filepath.Dir(config.ExpandPath(path)), "hooks", "claude-")
		if err := removeClaudeGlobalHooks(claudeSettingsPath(), marker); err != nil {
			return true, err
		}
		if err := removeAgentHookShims(path, name); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(r.stdout, "Claude hooks are owned by the paxm-claude plugin")
		return true, nil
	default:
		return false, nil
	}
}

func (r runner) installAgentHookIntegration(path string, agent config.AgentConfig, name string) error {
	if isRequestedAgent(name) {
		if err := preflightRequestedNativeHooks(name, hookInstallEventsForNamedAgent(name, agent)); err != nil {
			return fmt.Errorf("preflight %s integration: %w", name, err)
		}
		if err := preflightAgentMCP(name); err != nil {
			return fmt.Errorf("preflight %s MCP integration: %w", name, err)
		}
	}
	if err := removeLegacyHookShim(path, name); err != nil {
		return err
	}
	if err := uninstallAgentIntegration(path, name); err != nil {
		return fmt.Errorf("reset %s integration: %w", name, err)
	}
	installedScripts := make(map[string]string)
	for _, event := range hookInstallEventsForNamedAgent(name, agent) {
		scriptPath, err := installHookShim(path, name, event.ConfigEvent)
		if err != nil {
			return err
		}
		installedScripts[event.ConfigEvent] = scriptPath
		_, _ = fmt.Fprintf(r.stdout, "installed hook shim: %s\n", scriptPath)
	}
	return r.registerAgentIntegration(path, name, installedScripts)
}

func (r runner) registerAgentIntegration(configPath, name string, installedScripts map[string]string) error {
	switch name {
	case "codex":
		_, _ = fmt.Fprintf(r.stdout, "registered Codex global hook: %s\n", codexConfigPath())
	case "claude":
		if err := installClaudeGlobalHooks(claudeSettingsPath(), installedScripts); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "registered Claude Code global hook: %s\n", claudeSettingsPath())
	case "pi":
		if err := installPiGlobalHook(piExtensionPath(), installedScripts); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "registered Pi agent extension: %s\n", piExtensionPath())
	case "opencode":
		if err := installOpenCodeGlobalHook(openCodePluginPath(), installedScripts); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "registered OpenCode global plugin: %s\n", openCodePluginPath())
	default:
		if !isRequestedAgent(name) {
			return nil
		}
		path, err := installRequestedNativeHooks(name, installedScripts)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "registered %s native hooks: %s\n", agentDisplayName(name), path)
	}
	if isRequestedAgent(name) {
		path, err := installAgentMCP(configPath, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.stdout, "registered %s MCP server: %s\n", agentDisplayName(name), path)
	}
	return nil
}

func (r runner) finishSetupPrompt(err error) error {
	if errors.Is(err, errPromptCancelled) {
		_, _ = fmt.Fprintln(r.stdout, "setup cancelled")
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
	mergeDefaultProviders(&cfg, defaultCfg)
	mergeDefaultRecallProfiles(&cfg, defaultCfg)
	mergeDefaultWriteProfiles(&cfg, defaultCfg)
	cfg.Telemetry = mergeTelemetryDefaults(cfg.Telemetry, defaultCfg.Telemetry)
	mergeDefaultAgents(&cfg, defaultCfg)
	return cfg, nil
}

func mergeDefaultProviders(cfg *config.Config, defaults config.Config) {
	for name, provider := range defaults.Providers {
		if _, ok := cfg.Providers[name]; !ok {
			cfg.Providers[name] = provider
		}
	}
}

func mergeDefaultRecallProfiles(cfg *config.Config, defaults config.Config) {
	for name, profile := range defaults.RecallProfiles {
		if existing, ok := cfg.RecallProfiles[name]; ok {
			cfg.RecallProfiles[name] = mergeRecallProfileDefaults(name, existing, profile)
			continue
		}
		switch name {
		case "passive":
			cfg.RecallProfiles[name] = config.PassiveRecallProfileFrom(cfg.RecallProfiles["default"])
		case "passive_initial":
			cfg.RecallProfiles[name] = config.PassiveInitialRecallProfileFrom(cfg.RecallProfiles["default"])
		default:
			cfg.RecallProfiles[name] = profile
		}
	}
}

func mergeDefaultWriteProfiles(cfg *config.Config, defaults config.Config) {
	for name, profile := range defaults.WriteProfiles {
		if _, ok := cfg.WriteProfiles[name]; !ok {
			cfg.WriteProfiles[name] = profile
		}
	}
}

func mergeDefaultAgents(cfg *config.Config, defaults config.Config) {
	for name, agent := range defaults.Agents {
		existing, ok := cfg.Agents[name]
		if !ok {
			cfg.Agents[name] = agent
			continue
		}
		if existing.Hooks == nil {
			existing.Hooks = make(map[string]config.AgentHookConfig)
		}
		mergeDefaultAgentHooks(&existing, agent)
		cfg.Agents[name] = existing
	}
}

func mergeDefaultAgentHooks(existing *config.AgentConfig, defaults config.AgentConfig) {
	for eventName, eventCfg := range defaults.Hooks {
		existingHook, ok := existing.Hooks[eventName]
		if !ok {
			existing.Hooks[eventName] = eventCfg
			continue
		}
		existing.Hooks[eventName] = mergeHookDefaults(existingHook, eventCfg)
	}
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

func (r runner) runEval(args []string) error {
	if len(args) > 0 && args[0] == "cleanup" {
		return r.runEvalCleanup(args[1:])
	}
	if len(args) > 1 && args[0] == "retrieval" && args[1] == "locomo" {
		return r.runLoCoMoEval(args[2:])
	}
	if len(args) > 1 && args[0] == "provider" && args[1] == "jsonrpc" {
		return r.runJSONRPCConformance(args[2:])
	}
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: paxm eval run locomo --agent NAME [options] | paxm eval retrieval locomo [options] | paxm eval provider jsonrpc --command PATH")
	}
	if len(args) > 1 && args[1] == "locomo" {
		return r.runLoCoMoAgentEval(args[2:])
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
			_, _ = fmt.Fprintf(r.stdout, "BUDGET FAIL: %s\n", failure)
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

type stringListFlag []string

func (v *stringListFlag) String() string         { return strings.Join(*v, ",") }
func (v *stringListFlag) Set(value string) error { *v = append(*v, value); return nil }

func (r runner) runJSONRPCConformance(args []string) error {
	fs := flag.NewFlagSet("eval provider jsonrpc", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	command := fs.String("command", "", "provider executable")
	timeout := fs.Duration("timeout", 10*time.Second, "timeout per RPC call")
	jsonOut := fs.Bool("json", false, "write JSON")
	var commandArgs stringListFlag
	fs.Var(&commandArgs, "arg", "provider argument; repeat as needed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*command) == "" {
		return errors.New("JSON-RPC conformance requires --command PATH")
	}
	provider, err := jsonrpcadapter.New("conformance", config.ProviderConfig{Transport: "stdio", Command: *command, Args: commandArgs, Timeout: timeout.String()})
	if err != nil {
		return err
	}
	result := jsonrpcconformance.Run(context.Background(), provider)
	if *jsonOut {
		if err := writeJSON(r.stdout, result); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(r.stdout, "paxm JSON-RPC provider conformance: passed=%t protocol=%s\n", result.Passed, result.Protocol)
		for _, check := range result.Checks {
			status := "PASS"
			if check.Skipped {
				status = "SKIP"
			} else if !check.Passed {
				status = "FAIL"
			}
			_, _ = fmt.Fprintf(r.stdout, "  %-5s %s", status, check.Name)
			if check.Error != "" {
				_, _ = fmt.Fprintf(r.stdout, ": %s", check.Error)
			}
			_, _ = fmt.Fprintln(r.stdout)
		}
	}
	if !result.Passed {
		return errors.New("JSON-RPC provider failed required conformance checks")
	}
	return nil
}

func (r runner) runEvalCleanup(args []string) error {
	fs := flag.NewFlagSet("eval cleanup", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	runID := fs.String("run", "", "run id or run id prefix to clean")
	stale := fs.Bool("stale", false, "clean all non-cleaned manifests not marked keep-memory")
	manifestDir := fs.String("manifest-dir", filepath.Join(filepath.Dir(config.DefaultDataPath()), "eval-runs"), "eval run manifest directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" && !*stale {
		return errors.New("eval cleanup requires --run ID or --stale")
	}
	entries, err := os.ReadDir(*manifestDir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(paxruntime.ConfigFile(r.configPath))
	if err != nil {
		return err
	}
	registry := adapters.DefaultRegistry()
	cleaned := 0
	var cleanupErr error
	for _, entry := range entries {
		if !entry.IsDir() || (*runID != "" && entry.Name() != *runID && !strings.HasPrefix(entry.Name(), *runID+"-")) {
			continue
		}
		manifestPath := filepath.Join(*manifestDir, entry.Name(), "manifest.json")
		scope, restoreErr := paxeval.RestoreProviderScope(cfg, manifestPath)
		if restoreErr != nil {
			cleanupErr = errors.Join(cleanupErr, restoreErr)
			continue
		}
		if *stale && *runID == "" && scope.Manifest.Status == paxeval.EvalStatusCleaned {
			continue
		}
		if *stale && *runID == "" && scope.Manifest.KeepMemory {
			continue
		}
		if *runID != "" {
			scope.Manifest.KeepMemory = false
		}
		provider, buildErr := registry.BuildProvider(scope.Manifest.Provider, scope.Config.Providers[scope.Manifest.Provider])
		if buildErr != nil {
			cleanupErr = errors.Join(cleanupErr, buildErr)
			continue
		}
		if err := paxeval.CleanupProviderScope(context.Background(), scope, provider); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cleanup %s: %w", entry.Name(), err))
			continue
		}
		cleaned++
		_, _ = fmt.Fprintf(r.stdout, "cleaned eval run: %s\n", entry.Name())
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if cleaned == 0 {
		return errors.New("no matching eval runs required cleanup")
	}
	return nil
}

func (r runner) runLoCoMoAgentEval(args []string) error {
	fs := flag.NewFlagSet("eval run locomo", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	datasetPath := fs.String("dataset", "", "path to the official locomo10.json dataset")
	agentName := fs.String("agent", "", "agent runtime to evaluate (opencode)")
	agentBinary := fs.String("agent-binary", "", "agent executable path")
	model := fs.String("model", "", "agent model override")
	providerName := fs.String("provider", "sqlite", "configured provider name")
	armsValue := fs.String("arms", "control,passive,active", "comma-separated control, passive, active arms")
	maxQuestions := fs.Int("max-questions", 0, "limit paid agent questions")
	allQuestions := fs.Bool("all", false, "run every eligible LoCoMo question")
	matchThreshold := fs.Float64("match-threshold", 0.5, "token-F1 threshold for a matched answer")
	manifestDir := fs.String("manifest-dir", filepath.Join(filepath.Dir(config.DefaultDataPath()), "eval-runs"), "eval run manifest directory")
	runID := fs.String("run-id", "", "stable eval run id")
	settle := fs.Duration("settle", 0, "wait after ingest before agent questions")
	timeout := fs.Duration("timeout", 3*time.Minute, "timeout for each agent call")
	keepMemory := fs.Bool("keep-memory", false, "intentionally retain benchmark memories")
	jsonOut := fs.Bool("json", false, "write JSON")
	outputPath := fs.String("output", "", "write result JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*datasetPath) == "" || strings.TrimSpace(*agentName) == "" {
		return errors.New("LoCoMo agent evaluation requires --dataset PATH and --agent NAME")
	}
	if strings.TrimSpace(*model) == "" {
		return errors.New("LoCoMo agent evaluation requires --model PROVIDER/MODEL so runs are reproducible")
	}
	if *maxQuestions <= 0 && !*allQuestions {
		return errors.New("LoCoMo agent evaluation makes paid model calls; choose --max-questions N or explicitly pass --all")
	}
	if *maxQuestions < 0 || *matchThreshold <= 0 || *matchThreshold > 1 || *timeout <= 0 {
		return errors.New("invalid LoCoMo agent evaluation limits")
	}
	arms, err := parseAgentArms(*armsValue)
	if err != nil {
		return err
	}
	cfg, err := config.Load(paxruntime.ConfigFile(r.configPath))
	if err != nil {
		return err
	}
	dataset, err := paxeval.LoadLoCoMo(*datasetPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		*runID = newHookEventID()
	}
	executor := r.agentExecutor
	if executor == nil {
		if *agentName != "opencode" {
			return fmt.Errorf("agent %q is not supported", *agentName)
		}
		binary, findErr := findOpenCodeBinary(*agentBinary)
		if findErr != nil {
			return findErr
		}
		paxmBinary, executableErr := os.Executable()
		if executableErr != nil {
			return executableErr
		}
		executor = paxeval.OpenCodeExecutor{Binary: binary, PaxmBinary: paxmBinary, Model: *model, Timeout: *timeout}
	}
	registry := adapters.DefaultRegistry()
	result, runErr := (paxeval.LoCoMoAgentRunner{BuildProvider: registry.BuildProvider, Agent: executor}).Run(context.Background(), dataset, paxeval.LoCoMoAgentOptions{
		Config: cfg, Provider: *providerName, RunID: *runID, ManifestDir: *manifestDir,
		AgentName: *agentName, Arms: arms, MaxQuestions: *maxQuestions, MatchThreshold: *matchThreshold,
		KeepMemory: *keepMemory, Settle: *settle,
	})
	if *outputPath != "" {
		data, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return errors.Join(runErr, marshalErr)
		}
		if writeErr := os.WriteFile(*outputPath, append(data, '\n'), 0o600); writeErr != nil {
			return errors.Join(runErr, writeErr)
		}
	}
	if *jsonOut {
		if err := writeJSON(r.stdout, result); err != nil {
			return errors.Join(runErr, err)
		}
	} else {
		writeLoCoMoAgentReport(r.stdout, result)
	}
	if runErr != nil {
		return runErr
	}
	for _, summary := range result.Summaries {
		if summary.Errors > 0 {
			return fmt.Errorf("LoCoMo agent eval %s arm had %d execution errors", summary.Arm, summary.Errors)
		}
	}
	return nil
}

func parseAgentArms(value string) ([]paxeval.AgentArm, error) {
	seen := make(map[paxeval.AgentArm]bool)
	var arms []paxeval.AgentArm
	for _, item := range strings.Split(value, ",") {
		arm := paxeval.AgentArm(strings.TrimSpace(item))
		switch arm {
		case paxeval.AgentArmControl, paxeval.AgentArmPassive, paxeval.AgentArmActive:
		default:
			return nil, fmt.Errorf("unsupported eval arm %q", item)
		}
		if !seen[arm] {
			seen[arm] = true
			arms = append(arms, arm)
		}
	}
	if len(arms) == 0 {
		return nil, errors.New("at least one eval arm is required")
	}
	return arms, nil
}

func findOpenCodeBinary(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return config.ExpandPath(explicit), nil
	}
	if path, err := exec.LookPath("opencode"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, ".opencode", "bin", "opencode")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return "", errors.New("OpenCode binary not found; pass --agent-binary PATH")
}

func writeLoCoMoAgentReport(w io.Writer, result paxeval.LoCoMoAgentResult) {
	_, _ = fmt.Fprintf(w, "paxm eval: %s  agent=%s  provider=%s  model=%s\n", result.Benchmark, result.Agent, result.Provider, result.Model)
	_, _ = fmt.Fprintf(w, "  agent write canary: %t\n", result.WriteCanary)
	_, _ = fmt.Fprintf(w, "  questions: %d  trials: %d  duration: %s\n", result.QuestionCount, result.TrialCount, time.Duration(result.DurationMS)*time.Millisecond)
	for _, summary := range result.Summaries {
		_, _ = fmt.Fprintf(w, "  %-7s accuracy %.1f%%  mean-f1 %.3f  exact %.1f%%  recall-used %d/%d  useful %.1f%%  errors %d  tokens %d/%d  cost $%.4f\n",
			summary.Arm, summary.Accuracy*100, summary.MeanF1, summary.ExactMatch*100, summary.RecallUsed, summary.Trials, summary.UsefulRecallRate*100,
			summary.Errors, summary.InputTokens, summary.OutputTokens, summary.Cost)
	}
	_, _ = fmt.Fprintf(w, "  memory lift: passive %+.1fpp  active %+.1fpp\n", result.PassiveLift*100, result.ActiveLift*100)
}

func (r runner) runLoCoMoEval(args []string) error {
	fs := flag.NewFlagSet("eval retrieval locomo", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	datasetPath := fs.String("dataset", "", "path to the official locomo10.json dataset")
	providerName := fs.String("provider", "sqlite", "configured provider name")
	manifestDir := fs.String("manifest-dir", filepath.Join(filepath.Dir(config.DefaultDataPath()), "eval-runs"), "eval run manifest directory")
	runID := fs.String("run-id", "", "stable eval run id")
	limit := fs.Int("limit", 10, "retrieval result limit")
	settle := fs.Duration("settle", 0, "wait after ingest before recall")
	keepMemory := fs.Bool("keep-memory", false, "intentionally retain benchmark memories")
	jsonOut := fs.Bool("json", false, "write JSON")
	outputPath := fs.String("output", "", "write result JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*datasetPath) == "" {
		return errors.New("LoCoMo evaluation requires --dataset PATH")
	}
	if *limit <= 0 {
		return errors.New("LoCoMo --limit must be positive")
	}
	cfg, err := config.Load(paxruntime.ConfigFile(r.configPath))
	if err != nil {
		return err
	}
	dataset, err := paxeval.LoadLoCoMo(*datasetPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		*runID = newHookEventID()
	}
	registry := adapters.DefaultRegistry()
	result, runErr := (paxeval.LoCoMoRunner{BuildProvider: registry.BuildProvider}).Run(context.Background(), dataset, paxeval.LoCoMoRunOptions{
		Config: cfg, Provider: *providerName, RunID: *runID, ManifestDir: *manifestDir,
		Limit: *limit, KeepMemory: *keepMemory, Settle: *settle,
	})
	if *outputPath != "" {
		data, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			return errors.Join(runErr, marshalErr)
		}
		if writeErr := os.WriteFile(*outputPath, append(data, '\n'), 0o600); writeErr != nil {
			return errors.Join(runErr, writeErr)
		}
	}
	if *jsonOut {
		if err := writeJSON(r.stdout, result); err != nil {
			return errors.Join(runErr, err)
		}
	} else {
		writeLoCoMoReport(r.stdout, result)
	}
	if runErr != nil {
		return runErr
	}
	if result.ExecutionFailed > 0 {
		return fmt.Errorf("LoCoMo eval execution failed for %d questions", result.ExecutionFailed)
	}
	return nil
}

func writeLoCoMoReport(w io.Writer, result paxeval.LoCoMoResult) {
	_, _ = fmt.Fprintf(w, "paxm eval: %s (%s)\n", result.Benchmark, result.Provider)
	_, _ = fmt.Fprintf(w, "  conversations: %d  questions: %d  passed: %d  failed: %d\n", result.ConversationCount, result.QuestionCount, result.Passed, result.Failed)
	_, _ = fmt.Fprintf(w, "  recall@k: %.3f  precision@k: %.3f  mrr: %.3f  duration: %dms\n", result.RecallAtK, result.PrecisionAtK, result.MRR, result.DurationMS)
	for _, category := range result.Categories {
		_, _ = fmt.Fprintf(w, "  category %d: %d/%d  recall@k %.3f  precision@k %.3f  mrr %.3f\n", category.Category, category.Passed, category.Questions, category.RecallAtK, category.PrecisionAtK, category.MRR)
	}
}

func writeEvalComparison(w io.Writer, comparison paxeval.Comparison) {
	_, _ = fmt.Fprintf(w, "comparison: %s -> %s\n", comparison.BaselineSuite, comparison.CurrentSuite)
	_, _ = fmt.Fprintf(w, "  passed %+d  recall@k %+.3f  precision@k %+.3f  mrr %+.3f  false-positive rate %+.3f  duration %+dms\n", comparison.PassedDelta, comparison.RecallAtKDelta, comparison.PrecisionAtKDelta, comparison.MRRDelta, comparison.FalsePositiveRateDelta, comparison.DurationMSDelta)
	if comparison.WriteRecallDelta != 0 || comparison.WritePrecisionDelta != 0 || comparison.WriteFalsePositiveRateDelta != 0 {
		_, _ = fmt.Fprintf(w, "  write recall %+.3f  write precision %+.3f  write false-positive rate %+.3f\n", comparison.WriteRecallDelta, comparison.WritePrecisionDelta, comparison.WriteFalsePositiveRateDelta)
	}
}

func writeEvalReport(w io.Writer, result paxeval.Result) {
	_, _ = fmt.Fprintf(w, "paxm eval: %s (v%d)\n", result.Suite, result.Version)
	_, _ = fmt.Fprintf(w, "cases: %d  passed: %d  failed: %d  duration: %dms\n", result.CaseCount, result.Passed, result.Failed, result.DurationMS)
	if result.ExecutionFailed > 0 {
		_, _ = fmt.Fprintf(w, "execution failures: %d\n", result.ExecutionFailed)
	}
	_, _ = fmt.Fprintf(w, "recall@k: %.3f  precision@k: %.3f  mrr: %.3f  false-positive rate: %.3f\n", result.RecallAtK, result.PrecisionAtK, result.MRR, result.FalsePositiveRate)
	if result.AdapterContractCases > 0 {
		_, _ = fmt.Fprintf(w, "adapter contract: %d/%d passed  failed: %d\n", result.AdapterContractPassed, result.AdapterContractCases, result.AdapterContractFailed)
	}
	if result.WriteCaseCount > 0 {
		_, _ = fmt.Fprintf(w, "writes: %d/%d  write recall: %.3f  write precision: %.3f  write false-positive rate: %.3f\n", result.Writes, result.WriteCaseCount, result.WriteRecall, result.WritePrecision, result.WriteFalsePositiveRate)
		_, _ = fmt.Fprintf(w, "results: %d  returned context: %d bytes  write total: %.3fms  recall total: %.3fms\n", result.ResultCount, result.ReturnedContextBytes, float64(result.WriteDurationUS)/1000, float64(result.RecallDurationUS)/1000)
	}
	for _, group := range result.Categories {
		_, _ = fmt.Fprintf(w, "  %-20s %3d/%-3d  recall@k %.3f  precision@k %.3f  mrr %.3f\n", group.Name, group.Passed, group.CaseCount, group.RecallAtK, group.PrecisionAtK, group.MRR)
		if group.WriteCaseCount > 0 {
			_, _ = fmt.Fprintf(w, "  %-20s write recall %.3f  write precision %.3f  write false-positive rate %.3f\n", "", group.WriteRecall, group.WritePrecision, group.WriteFalsePositiveRate)
		}
	}
	for _, item := range result.Cases {
		if item.Passed {
			continue
		}
		_, _ = fmt.Fprintf(w, "FAIL %s", item.ID)
		if item.Error != "" {
			_, _ = fmt.Fprintf(w, ": %s", item.Error)
		}
		if len(item.Missing) > 0 {
			_, _ = fmt.Fprintf(w, " missing=%s", strings.Join(item.Missing, ","))
		}
		if len(item.Forbidden) > 0 {
			_, _ = fmt.Fprintf(w, " forbidden=%s", strings.Join(item.Forbidden, ","))
		}
		if len(item.Unexpected) > 0 {
			_, _ = fmt.Fprintf(w, " unexpected=%s", strings.Join(item.Unexpected, ","))
		}
		if len(item.WriteMissing) > 0 {
			_, _ = fmt.Fprintf(w, " write-missing=%s", strings.Join(item.WriteMissing, ","))
		}
		if len(item.WriteForbidden) > 0 {
			_, _ = fmt.Fprintf(w, " write-forbidden=%s", strings.Join(item.WriteForbidden, ","))
		}
		if len(item.MetadataMismatches) > 0 {
			_, _ = fmt.Fprintf(w, " metadata=%s", strings.Join(item.MetadataMismatches, ","))
		}
		if len(item.AdapterContractErrors) > 0 {
			_, _ = fmt.Fprintf(w, " adapter=%s", strings.Join(item.AdapterContractErrors, ","))
		}
		_, _ = fmt.Fprintln(w)
	}
}

func (r runner) runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config command requires a subcommand: path, show, doctor")
	}
	switch args[0] {
	case "path":
		_, _ = fmt.Fprintln(r.stdout, r.configFile())
		return nil
	case "show":
		_, rt, err := r.loadRuntime()
		if err != nil {
			return err
		}
		defer func() { _ = rt.Close() }()
		return writeJSON(r.stdout, rt.Operator.Config())
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
	defer func() { _ = rt.Close() }()
	statuses, err := rt.Operator.Health(context.Background())
	if *jsonOut {
		if writeErr := writeJSON(r.stdout, statuses); writeErr != nil {
			return writeErr
		}
		return err
	}
	for _, status := range statuses {
		if status.OK {
			_, _ = fmt.Fprintf(r.stdout, "ok: %s\n", status.Provider)
			continue
		}
		_, _ = fmt.Fprintf(r.stdout, "error: %s: %s\n", status.Provider, status.Error)
	}
	return err
}

func (r runner) runHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	days := fs.Int("days", 7, "number of days to summarize")
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	defer func() { _ = rt.Close() }()
	summary, err := rt.Operator.History(*days)
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
	_, rt, err := r.loadRuntime()
	if err != nil {
		return err
	}
	defer func() { _ = rt.Close() }()
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
		return rt.Operator.FollowEvents(ctx, *tail, 250*time.Millisecond, emit)
	}
	events, err := rt.Operator.TailEvents(*tail)
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
	case "mem0-cloud":
		return 3
	case "memos":
		return 4
	case "memos-cloud":
		return 5
	case "jsonrpc":
		return 6
	case "openviking":
		return 7
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
	case "mem0", "mem0-cloud":
		return promptMem0Provider(reader, writer, cfg, providerName)
	case "memos", "memos-cloud":
		return promptMemOSProvider(reader, writer, cfg, providerName)
	case "openviking":
		return promptOpenVikingProvider(reader, writer, cfg, providerName)
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
	defaultBaseURL := config.DefaultMem0BaseURL()
	if mem0.Type == "mem0-cloud" {
		defaultBaseURL = config.DefaultMem0CloudBaseURL()
	}
	var err error
	mem0.BaseURL, err = promptString(reader, writer, label+" base URL", firstNonEmpty(mem0.BaseURL, defaultBaseURL))
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
	if mem0.Type == "mem0-cloud" && strings.TrimSpace(mem0.APIKey) == "" {
		return errors.New("mem0 cloud setup requires an API key")
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

func promptMemOSProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	label := providerPromptLabel(providerName, provider)
	defaultURL := config.DefaultMemOSBaseURL()
	if provider.Type == "memos-cloud" {
		defaultURL = config.DefaultMemOSCloudBaseURL()
	}
	var err error
	provider.BaseURL, err = promptString(reader, writer, label+" base URL", firstNonEmpty(provider.BaseURL, defaultURL))
	if err != nil {
		return err
	}
	provider.APIKey, err = promptString(reader, writer, label+" API key (blank if self-hosted auth is disabled)", provider.APIKey)
	if err != nil {
		return err
	}
	if provider.Type == "memos-cloud" && strings.TrimSpace(provider.APIKey) == "" {
		return errors.New("memos cloud setup requires an API key")
	}
	provider.UserID, err = promptString(reader, writer, label+" user ID", provider.UserID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(provider.UserID) == "" {
		return errors.New("memos setup requires a user ID")
	}
	if provider.Type == "memos" {
		provider.MemCubeID, err = promptString(reader, writer, label+" memory cube ID", provider.MemCubeID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(provider.MemCubeID) == "" {
			return errors.New("memos setup requires a memory cube ID")
		}
		provider.SearchMode, err = promptSingleSelect(reader, writer, label+" search mode", []setupOption{{ID: "fast", Label: "fast"}, {ID: "fine", Label: "fine"}, {ID: "mixture", Label: "mixture"}}, firstNonEmpty(provider.SearchMode, "fast"))
		if err != nil {
			return err
		}
	} else {
		provider.AgentID, err = promptString(reader, writer, label+" agent ID (optional isolation scope)", provider.AgentID)
		if err != nil {
			return err
		}
	}
	cfg.Providers[providerName] = provider
	return promptProviderRouting(reader, writer, cfg, providerName, label)
}

func promptOpenVikingProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	label := providerPromptLabel(providerName, provider)
	var err error
	provider.BaseURL, err = promptString(reader, writer, label+" base URL", firstNonEmpty(provider.BaseURL, config.DefaultOpenVikingBaseURL()))
	if err != nil {
		return err
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		return errors.New("openviking setup requires a base URL")
	}
	provider.APIKey, err = promptString(reader, writer, label+" API key (blank for trusted local development)", provider.APIKey)
	if err != nil {
		return err
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
	case "mem0-cloud":
		if providerName == "mem0_cloud" {
			return "Mem0 Cloud"
		}
		return providerName + " (Mem0 Cloud)"
	case "memos":
		if providerName == "memos" {
			return "MemOS"
		}
		return providerName + " (MemOS)"
	case "memos-cloud":
		if providerName == "memos_cloud" {
			return "MemOS Cloud"
		}
		return providerName + " (MemOS Cloud)"
	case "openviking":
		if providerName == "openviking" {
			return "OpenViking"
		}
		return providerName + " (OpenViking)"
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
	case "cursor":
		return 4
	case "trae":
		return 5
	case "trae-cn":
		return 6
	case "kimi":
		return 7
	case "zcode":
		return 8
	case "kiro":
		return 9
	case "cline":
		return 10
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
	case "cursor":
		return "Cursor"
	case "trae":
		return "TRAE"
	case "trae-cn":
		return "TRAE CN"
	case "kimi":
		return "Kimi Code"
	case "zcode":
		return "ZCode"
	case "kiro":
		return "Kiro"
	case "cline":
		return "Cline"
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
	if profileName == "passive" || profileName == "passive_initial" {
		for i := range profile.Providers {
			if profile.Providers[i].Name == provider && profile.Providers[i].Timeout == "" {
				profile.Providers[i].Timeout = config.DefaultProviderRecallTimeout(cfg.Providers[provider].Type)
			}
			if profile.Providers[i].Name == provider && isCloudMemoryProvider(cfg.Providers[provider].Type) && profile.Providers[i].Thresholds == nil {
				profile.Providers[i].Thresholds = &config.RecallThresholdConfig{MinRelevance: 0.20, MinScore: 0.20}
			}
		}
	}
	cfg.RecallProfiles[profileName] = profile
}

func isCloudMemoryProvider(providerType string) bool {
	return providerType == "mem0-cloud" || providerType == "memos-cloud"
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
		_, _ = fmt.Fprint(writer, question+suffix)
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
			_, _ = fmt.Fprintln(writer, "Please answer yes or no.")
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
		_, _ = fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if i == defaultIndex {
				marker = "[x]"
			}
			_, _ = fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		_, _ = fmt.Fprintf(writer, "Choose one [%d]: ", defaultIndex+1)
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
		_, _ = fmt.Fprintln(writer, "Please choose one of the listed options.")
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
		_, _ = fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if defaults[option.ID] {
				marker = "[x]"
			}
			_, _ = fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		_, _ = fmt.Fprintf(writer, "Choose numbers, comma-separated, or all/none [%s]: ", defaultText)
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
		_, _ = fmt.Fprintf(writer, "%s\n", parseErr)
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
	_, _ = fmt.Fprintf(writer, "%s [%s]: ", question, defaultValue)
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
	switch target {
	case "claude", "trae", "trae-cn", "kiro":
		outputFlag = ""
	case "kimi":
		outputFlag = " --kimi"
	case "cline":
		outputFlag = " --cline"
	case "cursor":
		outputFlag = " --cursor"
	case "zcode":
		outputFlag = " --zcode"
	}
	extension, script := hookShimScript(runtime.GOOS, binaryPath, config.ExpandPath(configPath), target, event, outputFlag)
	scriptPath += extension
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

func hookShimScript(goos, binaryPath, configPath, target, event, outputFlag string) (string, string) {
	if goos == "windows" {
		script := "& " + powerShellQuote(binaryPath) + " --config " + powerShellQuote(configPath) + " __hook --target " + powerShellQuote(target) + " --event " + powerShellQuote(event) + outputFlag + "\nexit 0\n"
		return ".ps1", script
	}
	script := "#!/bin/sh\n" + shellQuote(binaryPath) + " --config " + shellQuote(configPath) + " __hook --target " + shellQuote(target) + " --event " + shellQuote(event) + outputFlag + " || exit 0\n"
	return "", script
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
	sessionStartScriptPath := strings.TrimSpace(scriptPaths["session_start"])
	userInputScriptPath := strings.TrimSpace(scriptPaths["user_input"])
	turnEndScriptPath := strings.TrimSpace(scriptPaths["turn_end"])
	if sessionStartScriptPath == "" && userInputScriptPath == "" && turnEndScriptPath == "" {
		return errors.New("pi hook requires at least one hook shim")
	}
	path = config.ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(piHookExtensionSource(sessionStartScriptPath, userInputScriptPath, turnEndScriptPath)), 0o644)
}

func piHookExtensionSource(sessionStartScriptPath, userInputScriptPath, turnEndScriptPath string) string {
	sessionStartScriptLiteral := jsonStringLiteral(config.ExpandPath(sessionStartScriptPath))
	userInputScriptLiteral := jsonStringLiteral(config.ExpandPath(userInputScriptPath))
	turnEndScriptLiteral := jsonStringLiteral(config.ExpandPath(turnEndScriptPath))
	return `import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawnSync } from "node:child_process";

const paxmSessionStartHookCommand = ` + sessionStartScriptLiteral + `;
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
let pendingSessionContext = "";
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
	const contexts: string[] = [];
	if (typeof result?.additional_context === "string" && result.additional_context.trim() !== "") {
	  contexts.push(result.additional_context.trim());
	}
	if (result?.skipped || !result?.recall?.hits?.length) return contexts.join("\n\n");
    const lines = ["paxm memory recall:"];
    for (const hit of result.recall.hits) {
      const score = typeof hit.score === "number" ? hit.score.toFixed(4) : "n/a";
      const provider = hit.provider ? String(hit.provider) : "unknown";
      const text = hit.text ? escapePaxmRecallText(String(hit.text).trim()) : "";
      if (text === "") continue;
      lines.push("- [" + provider + " score=" + score + "] " + text);
    }
	if (lines.length > 1) contexts.push('<paxm-recall version="1" mode="passive">\n' + lines.join("\n") + "\n</paxm-recall>");
	return contexts.join("\n\n");
  } catch {
    const text = escapePaxmRecallText(raw.trim());
    if (text === "" || text.includes("<paxm-recall") || text.includes("<paxm-local-time") || text.includes("<paxm-session-identity")) return text;
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
    pendingSessionContext = "";
    if (paxmSessionStartHookCommand !== "") {
      const resolvedCtx = activeCtx(ctx);
      const result = runPaxmHook(paxmSessionStartHookCommand, {
        schema_version: "paxm.pi.session_start.v1",
        target: "pi",
        event: "session_start",
        agent: "pi",
        session_id: currentSessionId(resolvedCtx),
        cwd: currentWorkspace(resolvedCtx),
        workspace: currentWorkspace(resolvedCtx),
        source: "pi",
      }, resolvedCtx, false);
      if (result.ok) pendingSessionContext = result.stdout.trim();
    }
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
    let recallContext = "";

	if (paxmUserInputHookCommand !== "") {
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
      if (result.ok) recallContext = formatPaxmRecall(result.stdout);
    }
    const content = [pendingSessionContext, recallContext].filter(Boolean).join("\n\n");
    pendingSessionContext = "";
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
		command := nativeHookCommand(scriptPath)
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

func writeRecallMarkdown(w io.Writer, result tools.RecallResult) {
	if len(result.Hits) == 0 {
		_, _ = fmt.Fprintln(w, "No memories found.")
		return
	}
	for i, hit := range result.Hits {
		_, _ = fmt.Fprintf(w, "### Memory %d (%s)\n", i+1, hit.Provider)
		provenance := hit.Provenance
		if provenance == (memory.Provenance{}) {
			provenance = memory.ProvenanceFromMetadata(hit.Metadata)
		}
		_, _ = fmt.Fprintf(w, "Scope: %s\n", formatMemoryScope(provenance))
		if provenance.UserID != "" {
			_, _ = fmt.Fprintf(w, "User: %s\n", provenance.UserID)
		}
		if provenance.AgentID != "" {
			_, _ = fmt.Fprintf(w, "Agent: %s\n", provenance.AgentID)
		}
		_, _ = fmt.Fprintf(w, "Score: %.4f\n", hit.Score)
		_, _ = fmt.Fprintf(w, "Relevance: %.4f\n", hit.Relevance)
		if hit.RawScore != nil {
			if hit.RawScoreKind != "" {
				_, _ = fmt.Fprintf(w, "Raw score: %.4f (%s)\n", *hit.RawScore, hit.RawScoreKind)
			} else {
				_, _ = fmt.Fprintf(w, "Raw score: %.4f\n", *hit.RawScore)
			}
		}
		if hit.Source != "" {
			_, _ = fmt.Fprintf(w, "Source: %s\n\n", hit.Source)
		} else {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w, strings.TrimSpace(hit.Text))
		_, _ = fmt.Fprintln(w)
	}
}

func formatMemoryScope(provenance memory.Provenance) string {
	if provenance.ScopeType == "" || provenance.ScopeID == "" {
		return "unknown"
	}
	return provenance.ScopeType + ":" + provenance.ScopeID
}

func writeRecallContextMarkdown(w io.Writer, result tools.RecallResult, mode string) {
	if len(result.Hits) == 0 {
		writeRecallMarkdown(w, result)
		return
	}
	var context bytes.Buffer
	writeRecallMarkdown(&context, result)
	_, _ = fmt.Fprintln(w, tools.WrapRecallContext(mode, context.String()))
}

func writeHistorySummary(w io.Writer, summary telemetry.HistorySummary) {
	_, _ = fmt.Fprintln(w, "== paxm history ==")
	_, _ = fmt.Fprintf(w, "window: last %d days\n", summary.Days)
	if summary.Totals.Events == 0 {
		_, _ = fmt.Fprintln(w, "status: quiet")
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "no telemetry events recorded yet")
		writeHistoryTable(w, "storage", []string{"file", "path"}, [][]string{
			{"events", summary.Storage.EventsFile},
			{"metrics", summary.Storage.MetricsFile},
		})
		return
	}
	_, _ = fmt.Fprintf(w, "status: %s\n", historyStatus(summary.Totals))
	writeHistoryTable(w, "overview", []string{"metric", "value"}, [][]string{
		{"events", formatInt(summary.Totals.Events)},
		{"successes", formatInt(summary.Totals.Successes)},
		{"errors", formatInt(summary.Totals.Errors)},
		{"skipped", formatInt(summary.Totals.Skipped)},
		{"provider_errors", formatInt(summary.Totals.ProviderErrors)},
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
	writeNamedCounters(w, "by provider", []string{"provider", "recalls", "hits", "writes", "refs", "avg_write", "avg_passive_latency", "provider_errors"}, summary.Providers, func(counter telemetry.Counter) []string {
		return []string{formatInt(counter.Recalls), formatInt(counter.Hits), formatInt(counter.Writes), formatInt(counter.Refs), formatAverageMS(counter.ProviderWriteDurationMS, counter.ProviderWriteSamples), formatAverageMS(counter.PassiveWriteLatencyTotalMS, counter.PassiveWriteSamples), formatInt(counter.ProviderErrors)}
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
	_, _ = fmt.Fprintln(w, strings.Join(parts, " "))
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
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, title)
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
			_, _ = fmt.Fprintf(w, "  %-*s", width, value)
			continue
		}
		_, _ = fmt.Fprintf(w, "  %*s", width, value)
	}
	_, _ = fmt.Fprintln(w)
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
