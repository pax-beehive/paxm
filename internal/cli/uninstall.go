package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

func (r runner) runUninstall(args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(r.stderr)
	agentName := flags.String("agent", "", "uninstall one agent integration")
	yes := flags.Bool("yes", false, "skip confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("uninstall does not accept positional arguments")
	}

	targets, err := uninstallTargets(*agentName)
	if err != nil {
		return err
	}
	if !*yes {
		confirmed, err := r.confirmUninstall(targets)
		if err != nil || !confirmed {
			return err
		}
	}
	return r.applyUninstall(r.configFile(), *agentName, targets)
}

func (r runner) confirmUninstall(targets []string) (bool, error) {
	_, _ = fmt.Fprintf(r.stdout, "Passive integrations to remove: %s\n", strings.Join(agentDisplayNames(targets), ", "))
	prompter := newSetupPrompter(r.stdin, r.stdout)
	confirmed, err := prompter.confirm("Continue uninstall?", false)
	if err != nil {
		if errors.Is(err, errPromptCancelled) {
			_, _ = fmt.Fprintln(r.stdout, "uninstall cancelled")
			return false, nil
		}
		return false, err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(r.stdout, "uninstall cancelled")
	}
	return confirmed, nil
}

func (r runner) applyUninstall(configPath, agentName string, targets []string) error {
	cfg, loadErr := config.Load(configPath)
	hasConfig := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, config.ErrConfigMissing) {
		return loadErr
	}
	if err := flushExistingHookBuffer(configPath, agentName == ""); err != nil {
		_, _ = fmt.Fprintf(r.stderr, "paxm hook buffer flush skipped: %s\n", err)
	}
	if hasConfig {
		for _, target := range targets {
			agent, ok := cfg.Agents[target]
			if !ok {
				continue
			}
			agent.Enabled = false
			cfg.Agents[target] = agent
		}
		if err := config.Save(configPath, cfg); err != nil {
			return err
		}
	}

	var cleanupErrors []error
	for _, target := range targets {
		if err := uninstallAgentIntegration(configPath, target); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("%s: %w", target, err))
			continue
		}
		_, _ = fmt.Fprintf(r.stdout, "uninstalled %s passive integration\n", agentDisplayName(target))
	}
	if agentName == "" {
		if err := removeSharedHookState(configPath); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func removeSharedHookState(configPath string) error {
	hooksDir := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks")
	var errs []error
	for _, path := range []string{hookSocketPath(configPath), hookSessionStatePath(configPath)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	entries, err := os.ReadDir(hooksDir)
	if err == nil && len(entries) == 0 {
		if err := os.Remove(hooksDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func uninstallTargets(agentName string) ([]string, error) {
	name := normalizeAgentName(agentName)
	if name == "" {
		return supportedPassiveAgents(), nil
	}
	if isSupportedPassiveAgent(name) {
		return []string{name}, nil
	}
	return nil, fmt.Errorf("unsupported agent %q; expected one of %s", agentName, strings.Join(supportedPassiveAgents(), ", "))
}

func supportedPassiveAgents() []string {
	return append([]string{"codex", "claude", "pi", "opencode"}, requestedAgentNames()...)
}

func isSupportedPassiveAgent(name string) bool {
	for _, candidate := range supportedPassiveAgents() {
		if name == candidate {
			return true
		}
	}
	return false
}

func normalizeAgentName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude-code", "claude_code", "claudecode":
		return "claude"
	case "trae cn", "trae_cn", "traecn":
		return "trae-cn"
	case "kimi code", "kimi-code", "kimi_code", "kimicode":
		return "kimi"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

func agentDisplayNames(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, agentDisplayName(name))
	}
	return result
}

func uninstallAgentIntegration(configPath, target string) error {
	marker := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", target+"-")
	var errs []error
	switch target {
	case "codex":
		if err := removeCodexGlobalHooks(codexConfigPath(), marker); err != nil {
			errs = append(errs, err)
		}
	case "claude":
		if err := removeClaudeGlobalHooks(claudeSettingsPath(), marker); err != nil {
			errs = append(errs, err)
		}
	case "pi":
		if err := os.RemoveAll(filepath.Dir(piExtensionPath())); err != nil {
			errs = append(errs, err)
		}
	case "opencode":
		if err := os.Remove(openCodePluginPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	default:
		if isRequestedAgent(target) {
			if err := uninstallRequestedNativeHooks(target, marker); err != nil {
				errs = append(errs, err)
			}
			if err := uninstallAgentMCP(configPath, target); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := removeAgentHookShims(configPath, target); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func removeAgentHookShims(configPath, target string) error {
	hooksDir := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks")
	var errs []error
	for _, event := range installedHookEvents() {
		path := filepath.Join(hooksDir, target+"-"+event.ConfigEvent)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	legacyPath := filepath.Join(hooksDir, target+"-user_prompt")
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func removeCodexGlobalHooks(path, marker string) error {
	path = config.ExpandPath(path)
	original, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	updated, changed := removeCodexHooksByMarker(string(original), marker)
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func removeCodexHooksByMarker(content, marker string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	changed := false
	for index, line := range lines {
		if !strings.Contains(line, marker) {
			continue
		}
		updated := removeInlineTomlArrayEntries(line, marker)
		if updated == line {
			continue
		}
		changed = true
		if inlineTomlArrayIsEmpty(updated) {
			lines[index] = ""
		} else {
			lines[index] = updated
		}
	}
	return strings.Join(lines, ""), changed
}

func inlineTomlArrayIsEmpty(line string) bool {
	equals := strings.Index(line, "=")
	if equals == -1 {
		return false
	}
	return strings.TrimSpace(line[equals+1:]) == "[]"
}

func removeClaudeGlobalHooks(path, marker string) error {
	path = config.ExpandPath(path)
	original, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	settings := make(map[string]json.RawMessage)
	if err := json.Unmarshal(original, &settings); err != nil {
		return fmt.Errorf("decode Claude Code settings %s: %w", path, err)
	}
	rawHooks, ok := settings["hooks"]
	if !ok {
		return nil
	}
	hooks := make(map[string][]json.RawMessage)
	if err := json.Unmarshal(rawHooks, &hooks); err != nil {
		return fmt.Errorf("decode Claude Code hooks %s: %w", path, err)
	}
	changed := false
	for eventName, groups := range hooks {
		filteredGroups := make([]json.RawMessage, 0, len(groups))
		for _, rawGroup := range groups {
			updated, keep, groupChanged, err := removeClaudeHookGroupHandlers(rawGroup, marker)
			if err != nil {
				return err
			}
			changed = changed || groupChanged
			if keep {
				filteredGroups = append(filteredGroups, updated)
			}
		}
		if len(filteredGroups) == 0 {
			delete(hooks, eventName)
		} else {
			hooks[eventName] = filteredGroups
		}
	}
	if !changed {
		return nil
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		updatedHooks, err := json.Marshal(hooks)
		if err != nil {
			return err
		}
		settings["hooks"] = updatedHooks
	}
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	return os.WriteFile(path, updated, 0o600)
}

func removeClaudeHookGroupHandlers(rawGroup json.RawMessage, marker string) (json.RawMessage, bool, bool, error) {
	group := make(map[string]json.RawMessage)
	if err := json.Unmarshal(rawGroup, &group); err != nil {
		return nil, false, false, err
	}
	rawHandlers, ok := group["hooks"]
	if !ok {
		return rawGroup, true, false, nil
	}
	var handlers []json.RawMessage
	if err := json.Unmarshal(rawHandlers, &handlers); err != nil {
		return nil, false, false, err
	}
	filtered := make([]json.RawMessage, 0, len(handlers))
	changed := false
	for _, rawHandler := range handlers {
		var handler struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(rawHandler, &handler); err != nil {
			return nil, false, false, err
		}
		if strings.Contains(handler.Command, marker) {
			changed = true
			continue
		}
		filtered = append(filtered, rawHandler)
	}
	if !changed {
		return rawGroup, true, false, nil
	}
	if len(filtered) == 0 {
		return nil, false, true, nil
	}
	updatedHandlers, err := json.Marshal(filtered)
	if err != nil {
		return nil, false, false, err
	}
	group["hooks"] = updatedHandlers
	updatedGroup, err := json.Marshal(group)
	if err != nil {
		return nil, false, false, err
	}
	return updatedGroup, true, true, nil
}
