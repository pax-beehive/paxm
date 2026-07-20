package cli

import (
	"sort"

	"github.com/pax-beehive/paxm/internal/config"
)

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
