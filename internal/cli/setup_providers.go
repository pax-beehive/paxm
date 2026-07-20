package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

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
		// SQLite works out of the box; path and routing stay at their
		// defaults and can be tuned in the config file.
		return nil
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
		return nil
	}
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
	cfg.Providers[providerName] = zep
	return nil
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
	return nil
}

func promptJSONRPCProvider(reader *bufio.Reader, writer io.Writer, cfg *config.Config, providerName string) error {
	provider := cfg.Providers[providerName]
	label := providerPromptLabel(providerName, provider)
	var err error
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
	cfg.Providers[providerName] = provider
	return nil
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
	}
	cfg.Providers[providerName] = provider
	return nil
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
	return nil
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
