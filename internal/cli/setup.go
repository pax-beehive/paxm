package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/config"
)

func (r runner) runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	force := fs.Bool("force", false, "overwrite an existing config")
	yes := fs.Bool("yes", false, "accept default setup answers")
	userID := fs.String("user-id", "", "stable user identity")
	teamIDs := fs.String("team-id", "", "comma-separated team scope IDs")
	integration := fs.String("integration", config.IntegrationOwnerPaxm, "hook owner: paxm, codex-plugin, or claude-plugin")
	var providerFlags, agentFlags stringListFlag
	fs.Var(&providerFlags, "provider", "memory provider to enable; repeat as needed, skips the provider prompt")
	fs.Var(&agentFlags, "agent", "agent to enable for passive memory; repeat as needed, skips the agent prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *integration != config.IntegrationOwnerPaxm && *integration != config.IntegrationOwnerCodexPlugin && *integration != config.IntegrationOwnerClaudePlugin {
		return fmt.Errorf("unsupported setup integration %q", *integration)
	}

	path := r.configFile()
	prompter := newSetupPrompter(r.stdin, r.stdout)
	selection, proceed, err := r.prepareSetup(path, prompter, *force, *yes, *integration, *userID, *teamIDs, providerFlags, agentFlags)
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

func (r runner) prepareSetup(path string, prompter *setupPrompter, force, yes bool, integration, userID, teamIDs string, providerFlags, agentFlags []string) (setupSelection, bool, error) {
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
	providersPinned := len(providerFlags) > 0
	if providersPinned {
		selectedProviders, err = pinnedSelections(providerFlags, providerOptions(cfg), "provider")
		if err != nil {
			return setupSelection{}, false, err
		}
	}
	agentsPinned := len(agentFlags) > 0
	if agentsPinned {
		selectedHooks, err = pinnedSelections(agentFlags, hookOptions(cfg), "agent")
		if err != nil {
			return setupSelection{}, false, err
		}
		constrainPluginHooks(selectedHooks, pluginTarget)
	}
	if !yes {
		selectedProviders, selectedHooks, proceed, err = r.promptSetupSelections(prompter, &cfg, selectedProviders, selectedHooks, providersPinned, agentsPinned)
		if err != nil || !proceed {
			return setupSelection{}, false, err
		}
	}
	constrainPluginHooks(selectedHooks, pluginTarget)
	if !anySelected(selectedProviders) {
		return setupSelection{}, false, errors.New("setup requires at least one memory provider")
	}
	applySetupSelections(&cfg, selectedProviders, selectedHooks)
	applySetupIntegration(&cfg, integration, pluginTarget, previousEnabled)
	if !yes {
		proceed, err = r.confirmSetupSummary(prompter, cfg, selectedProviders, selectedHooks)
		if err != nil || !proceed {
			return setupSelection{}, false, err
		}
	}
	return setupSelection{cfg: cfg, selectedHooks: selectedHooks}, true, nil
}

// pinnedSelections converts --provider/--agent flag values into a selection
// map, rejecting unknown names with the list of valid options.
func pinnedSelections(names []string, options []setupOption, flagName string) (map[string]bool, error) {
	selected := make(map[string]bool, len(options))
	for _, option := range options {
		selected[option.ID] = false
	}
	for _, name := range names {
		if optionIndex(options, name) == -1 {
			return nil, fmt.Errorf("unknown setup %s %q (options: %s)", flagName, name, strings.Join(optionIDs(options), ", "))
		}
		selected[name] = true
	}
	return selected, nil
}

func optionIDs(options []setupOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.ID)
	}
	return ids
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

func (r runner) promptSetupSelections(prompter *setupPrompter, cfg *config.Config, selectedProviders, selectedHooks map[string]bool, providersPinned, agentsPinned bool) (map[string]bool, map[string]bool, bool, error) {
	var err error
	if !providersPinned {
		selectedProviders, err = prompter.multiSelect("Select memory providers to enable", providerOptions(*cfg), selectedProviders)
		if err != nil {
			return nil, nil, false, r.finishSetupPrompt(err)
		}
	}
	for _, providerName := range providerOptionIDs(*cfg) {
		if !selectedProviders[providerName] {
			continue
		}
		if err := promptProviderInstance(prompter.reader, prompter.output, cfg, providerName); err != nil {
			return nil, nil, false, r.finishSetupPrompt(err)
		}
	}
	if !agentsPinned {
		selectedHooks, err = prompter.multiSelect("Select agents for passive memory", hookOptions(*cfg), selectedHooks)
		if err != nil {
			return nil, nil, false, r.finishSetupPrompt(err)
		}
	}
	return selectedProviders, selectedHooks, true, nil
}

func ensureSetupIdentity(cfg *config.Config) {
	if strings.TrimSpace(cfg.Identity.UserID) == "" {
		cfg.Identity.UserID = config.SlugID(firstNonEmpty(os.Getenv("USER"), "user"))
	}
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

func applySetupSelections(cfg *config.Config, selectedProviders, selectedHooks map[string]bool) {
	for name, provider := range cfg.Providers {
		wasEnabled := provider.Enabled
		provider.Enabled = selectedProviders[name]
		cfg.Providers[name] = provider
		switch {
		case !provider.Enabled:
			removeProviderFromDefaultProfiles(cfg, name)
		case !wasEnabled:
			// Newly enabled providers get the default routing (read+write,
			// required); custom routing for already-enabled providers is
			// left untouched.
			setDefaultProviderMode(cfg, name, "read_write", true)
		}
	}
	for name, agent := range cfg.Agents {
		agent.Enabled = selectedHooks[name]
		cfg.Agents[name] = agent
	}
	for name, selected := range selectedHooks {
		if selected {
			enablePassiveWriteStart(cfg, name)
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
