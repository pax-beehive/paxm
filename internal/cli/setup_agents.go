package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

const (
	agentBehaviorRecall = "recall"
	agentBehaviorWrite  = "write"
)

func configureSelectedAgents(prompter *setupPrompter, cfg *config.Config, selected map[string]bool) error {
	selectedOptions := make([]setupOption, 0)
	for _, option := range hookOptions(*cfg) {
		if selected[option.ID] {
			selectedOptions = append(selectedOptions, option)
		}
	}
	for index, option := range selectedOptions {
		fmt.Fprintf(prompter.output, "\nConfigure %s (%d/%d)\n", option.Label, index+1, len(selectedOptions))
		agent := cfg.Agents[option.ID]
		behaviors, err := promptRequiredMultiSelect(
			prompter,
			"Passive memory behavior",
			agentBehaviorOptions(agent),
			agentBehaviorDefaults(agent),
			"Select passive recall, passive write, or both.",
		)
		if err != nil {
			return err
		}
		if err := configureAgentRecall(prompter, cfg, &agent, behaviors[agentBehaviorRecall]); err != nil {
			return err
		}
		if err := configureAgentWrites(prompter, cfg, &agent, behaviors[agentBehaviorWrite]); err != nil {
			return err
		}
		agent.Enabled = true
		cfg.Agents[option.ID] = agent
	}
	for name, agent := range cfg.Agents {
		if !selected[name] {
			agent.Enabled = false
			cfg.Agents[name] = agent
		}
	}
	return nil
}

func writeSetupSummary(writer io.Writer, cfg config.Config, providers, agents map[string]bool) {
	fmt.Fprintln(writer, "\nSetup summary")
	fmt.Fprintf(writer, "  Providers: %s\n", strings.Join(selectedOptionLabels(providerOptions(cfg), providers), ", "))
	selectedAgents := selectedOptionLabels(hookOptions(cfg), agents)
	if len(selectedAgents) == 0 {
		fmt.Fprintln(writer, "  Agents: none")
		return
	}
	fmt.Fprintln(writer, "  Agents:")
	for _, option := range hookOptions(cfg) {
		if !agents[option.ID] {
			continue
		}
		agent := cfg.Agents[option.ID]
		recall := "off"
		if hook, ok := agent.Hooks["user_input"]; ok && hook.Recall.Enabled {
			recall = firstNonEmpty(hook.Recall.Profile, "default")
		}
		writeEvents := make([]string, 0)
		for _, eventName := range agentWriteEvents(agent) {
			if agent.Hooks[eventName].Write.Enabled {
				writeEvents = append(writeEvents, eventName)
			}
		}
		writeSummary := "off"
		if len(writeEvents) > 0 {
			writeSummary = strings.Join(writeEvents, ",") + " profile=" + firstNonEmpty(agentWriteProfile(agent), "ltm")
		}
		fmt.Fprintf(writer, "    %s: recall=%s write=%s\n", option.Label, recall, writeSummary)
	}
}

func selectedOptionLabels(options []setupOption, selected map[string]bool) []string {
	labels := make([]string, 0)
	for _, option := range options {
		if selected[option.ID] {
			labels = append(labels, option.Label)
		}
	}
	return labels
}

func agentBehaviorOptions(agent config.AgentConfig) []setupOption {
	options := make([]setupOption, 0, 2)
	if agentSupportsRecall(agent) {
		options = append(options, setupOption{ID: agentBehaviorRecall, Label: "Passive recall"})
	}
	if len(agentWriteEvents(agent)) > 0 {
		options = append(options, setupOption{ID: agentBehaviorWrite, Label: "Passive write"})
	}
	return options
}

func agentBehaviorDefaults(agent config.AgentConfig) map[string]bool {
	selected := map[string]bool{
		agentBehaviorRecall: false,
		agentBehaviorWrite:  false,
	}
	if hook, ok := agent.Hooks["user_input"]; ok {
		selected[agentBehaviorRecall] = hook.Recall.Enabled
	}
	for _, eventName := range agentWriteEvents(agent) {
		if agent.Hooks[eventName].Write.Enabled {
			selected[agentBehaviorWrite] = true
			break
		}
	}
	if !selected[agentBehaviorRecall] && !selected[agentBehaviorWrite] {
		selected[agentBehaviorRecall] = agentSupportsRecall(agent)
		selected[agentBehaviorWrite] = len(agentWriteEvents(agent)) > 0
	}
	return selected
}

func configureAgentRecall(prompter *setupPrompter, cfg *config.Config, agent *config.AgentConfig, enabled bool) error {
	hook, ok := agent.Hooks["user_input"]
	if !ok || !agentSupportsRecall(*agent) {
		return nil
	}
	hook.Recall.Enabled = enabled
	if !enabled {
		agent.Hooks["user_input"] = hook
		return nil
	}
	profiles := recallProfileOptions(*cfg)
	profile, err := prompter.selectOne("Passive recall profile", profiles, firstNonEmpty(hook.Recall.Profile, "passive"))
	if err != nil {
		return err
	}
	hook.Recall.Profile = profile
	if hook.Recall.Initial != nil {
		initialProfile, err := prompter.selectOne("Initial recall profile", profiles, firstNonEmpty(hook.Recall.Initial.Profile, "passive_initial"))
		if err != nil {
			return err
		}
		hook.Recall.Initial.Enabled = true
		hook.Recall.Initial.Profile = initialProfile
	}
	agent.Hooks["user_input"] = hook
	return nil
}

func configureAgentWrites(prompter *setupPrompter, cfg *config.Config, agent *config.AgentConfig, enabled bool) error {
	eventNames := agentWriteEvents(*agent)
	if len(eventNames) == 0 {
		return nil
	}
	if !enabled {
		for _, eventName := range eventNames {
			hook := agent.Hooks[eventName]
			hook.Write.Enabled = false
			agent.Hooks[eventName] = hook
		}
		return nil
	}
	profileDefault := "ltm"
	for _, eventName := range eventNames {
		if profile := strings.TrimSpace(agent.Hooks[eventName].Write.Profile); profile != "" {
			profileDefault = profile
			break
		}
	}
	profile, err := prompter.selectOne("Passive write profile", writeProfileOptions(*cfg), profileDefault)
	if err != nil {
		return err
	}
	eventOptions := make([]setupOption, 0, len(eventNames))
	defaults := make(map[string]bool, len(eventNames))
	for _, eventName := range eventNames {
		eventOptions = append(eventOptions, setupOption{ID: eventName, Label: hookEventLabel(eventName)})
		defaults[eventName] = agent.Hooks[eventName].Write.Enabled
	}
	if !anySelected(defaults) {
		for _, eventName := range eventNames {
			defaults[eventName] = true
		}
	}
	selected, err := promptRequiredMultiSelect(prompter, "Passive write events", eventOptions, defaults, "Select at least one passive write event.")
	if err != nil {
		return err
	}
	for _, eventName := range eventNames {
		hook := agent.Hooks[eventName]
		hook.Write.Enabled = selected[eventName]
		hook.Write.Profile = profile
		agent.Hooks[eventName] = hook
	}
	return nil
}

func promptRequiredMultiSelect(prompter *setupPrompter, question string, options []setupOption, defaults map[string]bool, message string) (map[string]bool, error) {
	if len(options) == 0 {
		return nil, errors.New("no selectable options")
	}
	for {
		selected, err := prompter.multiSelect(question, options, defaults)
		if err != nil {
			return nil, err
		}
		if anySelected(selected) {
			return selected, nil
		}
		fmt.Fprintln(prompter.output, message)
	}
}

func agentSupportsRecall(agent config.AgentConfig) bool {
	hook, ok := agent.Hooks["user_input"]
	if !ok {
		return false
	}
	return hook.Recall.QueryTemplate != "" || hook.Recall.Profile != "" || hook.Recall.Initial != nil
}

func agentWriteEvents(agent config.AgentConfig) []string {
	ordered := []string{"session_start", "user_input", "turn_end"}
	seen := make(map[string]bool)
	events := make([]string, 0, len(agent.Hooks))
	for _, eventName := range ordered {
		if hook, ok := agent.Hooks[eventName]; ok && hookSupportsWrite(hook) {
			events = append(events, eventName)
			seen[eventName] = true
		}
	}
	var extra []string
	for eventName, hook := range agent.Hooks {
		if !seen[eventName] && hookSupportsWrite(hook) {
			extra = append(extra, eventName)
		}
	}
	sort.Strings(extra)
	return append(events, extra...)
}

func hookSupportsWrite(hook config.AgentHookConfig) bool {
	return hook.Write.Enabled || hook.Write.Profile != "" || hook.Write.Template != "" || hook.Write.Mode != "" || hook.Write.Buffer != (config.HookBufferConfig{})
}

func agentPassiveWriteEnabled(agent config.AgentConfig) bool {
	for _, eventName := range agentWriteEvents(agent) {
		if agent.Hooks[eventName].Write.Enabled {
			return true
		}
	}
	return false
}

func agentWriteProfile(agent config.AgentConfig) string {
	for _, eventName := range agentWriteEvents(agent) {
		if profile := strings.TrimSpace(agent.Hooks[eventName].Write.Profile); profile != "" {
			return profile
		}
	}
	return ""
}

func recallProfileOptions(cfg config.Config) []setupOption {
	return namedProfileOptions(cfg.RecallProfiles)
}

func writeProfileOptions(cfg config.Config) []setupOption {
	return namedProfileOptions(cfg.WriteProfiles)
}

func namedProfileOptions[T any](profiles map[string]T) []setupOption {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	options := make([]setupOption, 0, len(names))
	for _, name := range names {
		options = append(options, setupOption{ID: name, Label: name})
	}
	return options
}

func hookEventLabel(eventName string) string {
	switch eventName {
	case "session_start":
		return "Session start"
	case "user_input":
		return "User input"
	case "tool_use":
		return "Tool calls and results"
	case "tool_failure":
		return "Failed tool calls"
	case "turn_end":
		return "Turn end"
	default:
		return eventName
	}
}
