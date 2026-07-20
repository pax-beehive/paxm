package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

func writeSetupSummary(writer io.Writer, cfg config.Config, providers, agents map[string]bool) {
	_, _ = fmt.Fprintln(writer, "\nSetup summary")
	_, _ = fmt.Fprintf(writer, "  User: %s\n", firstNonEmpty(cfg.Identity.UserID, "unknown"))
	_, _ = fmt.Fprintf(writer, "  Providers: %s\n", strings.Join(selectedOptionLabels(providerOptions(cfg), providers), ", "))
	selectedAgents := selectedOptionLabels(hookOptions(cfg), agents)
	if len(selectedAgents) == 0 {
		_, _ = fmt.Fprintln(writer, "  Agents: none")
		return
	}
	_, _ = fmt.Fprintln(writer, "  Agents:")
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
		_, _ = fmt.Fprintf(writer, "    %s (%s): recall=%s write=%s\n", option.Label, firstNonEmpty(agent.AgentID, "unknown"), recall, writeSummary)
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
