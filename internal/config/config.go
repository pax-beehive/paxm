package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrConfigMissing = errors.New("paxm config is missing")

const (
	defaultConfigVersion       = 1
	defaultMem0BaseURL         = "http://localhost:8888"
	defaultJSONRPCTransport    = "stdio"
	defaultJSONRPCTimeout      = "30s"
	defaultProviderRouteWeight = 1
	defaultRecallMaxResults    = 3
	defaultRecallMinRelevance  = 0.25
	defaultRecallMinScore      = 0.25
	defaultSTMExpiresAfter     = "24h"

	passiveRecallMaxResults   = 2
	passiveRecallMinRelevance = 0.75
	passiveRecallMinScore     = 0.75

	initialRecallMaxResults   = 5
	initialRecallMinRelevance = 0.35
	initialRecallMinScore     = 0.35

	defaultHookRecallMaxResults      = passiveRecallMaxResults
	defaultHookInsertionMinScore     = 0.8
	defaultHookInsertionMaxItems     = passiveRecallMaxResults
	defaultHookBufferFlushCount      = 10
	defaultTelemetryMaxEventFileSize = 1 << 20
	defaultTelemetryMaxEventFiles    = 3
	defaultTelemetryRetentionDays    = 30
	defaultTelemetryQueryPreview     = 80
)

type Config struct {
	Version        int                            `json:"version" yaml:"version"`
	Providers      map[string]ProviderConfig      `json:"providers" yaml:"providers"`
	RecallProfiles map[string]RecallProfileConfig `json:"recall_profiles,omitempty" yaml:"recall_profiles,omitempty"`
	WriteProfiles  map[string]WriteProfileConfig  `json:"write_profiles,omitempty" yaml:"write_profiles,omitempty"`
	Agents         map[string]AgentConfig         `json:"agents,omitempty" yaml:"agents,omitempty"`
	Telemetry      TelemetryConfig                `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`

	Hooks map[string]LegacyHookConfig `json:"hooks,omitempty" yaml:"hooks,omitempty"`
}

type ProviderConfig struct {
	Type              string            `json:"type" yaml:"type"`
	Enabled           bool              `json:"enabled" yaml:"enabled"`
	Path              string            `json:"path,omitempty" yaml:"path,omitempty"`
	APIKey            string            `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	BaseURL           string            `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Transport         string            `json:"transport,omitempty" yaml:"transport,omitempty"`
	Command           string            `json:"command,omitempty" yaml:"command,omitempty"`
	Args              []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Timeout           string            `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	UserID            string            `json:"user_id,omitempty" yaml:"user_id,omitempty"`
	AgentID           string            `json:"agent_id,omitempty" yaml:"agent_id,omitempty"`
	RunID             string            `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	GraphID           string            `json:"graph_id,omitempty" yaml:"graph_id,omitempty"`
	SearchScope       string            `json:"search_scope,omitempty" yaml:"search_scope,omitempty"`
	MaxCharacters     int               `json:"max_characters,omitempty" yaml:"max_characters,omitempty"`
	SourceDescription string            `json:"source_description,omitempty" yaml:"source_description,omitempty"`
	Infer             *bool             `json:"infer,omitempty" yaml:"infer,omitempty"`

	Read     *bool   `json:"read,omitempty" yaml:"read,omitempty"`
	Write    *bool   `json:"write,omitempty" yaml:"write,omitempty"`
	Required *bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Weight   float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
}

type ProviderRouteConfig struct {
	Name       string                 `json:"name" yaml:"name"`
	Required   bool                   `json:"required" yaml:"required"`
	Weight     float64                `json:"weight,omitempty" yaml:"weight,omitempty"`
	Thresholds *RecallThresholdConfig `json:"thresholds,omitempty" yaml:"thresholds,omitempty"`
}

type RecallProfileConfig struct {
	Providers  []ProviderRouteConfig `json:"providers,omitempty" yaml:"providers,omitempty"`
	MaxResults int                   `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Thresholds RecallThresholdConfig `json:"thresholds,omitempty" yaml:"thresholds,omitempty"`
	Ranking    RankingConfig         `json:"ranking,omitempty" yaml:"ranking,omitempty"`
	Tiers      []string              `json:"tiers,omitempty" yaml:"tiers,omitempty"`
}

type RecallThresholdConfig struct {
	MinRelevance float64 `json:"min_relevance,omitempty" yaml:"min_relevance,omitempty"`
	MinScore     float64 `json:"min_score,omitempty" yaml:"min_score,omitempty"`
}

type RankingConfig struct {
	Type         string  `json:"type,omitempty" yaml:"type,omitempty"`
	RecencyBoost float64 `json:"recency_boost,omitempty" yaml:"recency_boost,omitempty"`
}

type WriteProfileConfig struct {
	Providers    []ProviderRouteConfig `json:"providers,omitempty" yaml:"providers,omitempty"`
	Tier         string                `json:"tier,omitempty" yaml:"tier,omitempty"`
	ExpiresAfter string                `json:"expires_after,omitempty" yaml:"expires_after,omitempty"`
}

type AgentConfig struct {
	Enabled               bool                       `json:"enabled" yaml:"enabled"`
	PassiveWriteStartedAt string                     `json:"passive_write_started_at,omitempty" yaml:"passive_write_started_at,omitempty"`
	ActiveRecall          ActiveRecallConfig         `json:"active_recall,omitempty" yaml:"active_recall,omitempty"`
	Hooks                 map[string]AgentHookConfig `json:"hooks,omitempty" yaml:"hooks,omitempty"`
}

type ActiveRecallConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
	Output  string `json:"output,omitempty" yaml:"output,omitempty"`
}

type AgentHookConfig struct {
	Recall HookRecallConfig `json:"recall,omitempty" yaml:"recall,omitempty"`
	Write  HookWriteConfig  `json:"write,omitempty" yaml:"write,omitempty"`
}

type HookRecallConfig struct {
	Enabled       bool                `json:"enabled" yaml:"enabled"`
	Profile       string              `json:"profile,omitempty" yaml:"profile,omitempty"`
	QueryTemplate string              `json:"query_template,omitempty" yaml:"query_template,omitempty"`
	MaxResults    int                 `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Output        string              `json:"output,omitempty" yaml:"output,omitempty"`
	Insertion     HookInsertionConfig `json:"insertion,omitempty" yaml:"insertion,omitempty"`
	Initial       *HookInitialRecall  `json:"initial,omitempty" yaml:"initial,omitempty"`
}

type HookInitialRecall struct {
	Enabled       bool                `json:"enabled" yaml:"enabled"`
	Profile       string              `json:"profile,omitempty" yaml:"profile,omitempty"`
	QueryTemplate string              `json:"query_template,omitempty" yaml:"query_template,omitempty"`
	MaxResults    int                 `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Insertion     HookInsertionConfig `json:"insertion,omitempty" yaml:"insertion,omitempty"`
}

type HookInsertionConfig struct {
	MinScore          float64 `json:"min_score,omitempty" yaml:"min_score,omitempty"`
	MaxItems          int     `json:"max_items,omitempty" yaml:"max_items,omitempty"`
	RequireQueryTerms bool    `json:"require_query_terms,omitempty" yaml:"require_query_terms,omitempty"`
}

type HookWriteConfig struct {
	Enabled  bool             `json:"enabled" yaml:"enabled"`
	Profile  string           `json:"profile,omitempty" yaml:"profile,omitempty"`
	Template string           `json:"template,omitempty" yaml:"template,omitempty"`
	Mode     string           `json:"mode,omitempty" yaml:"mode,omitempty"`
	Buffer   HookBufferConfig `json:"buffer,omitempty" yaml:"buffer,omitempty"`
}

type HookBufferConfig struct {
	Enabled    bool `json:"enabled" yaml:"enabled"`
	Flush      bool `json:"flush,omitempty" yaml:"flush,omitempty"`
	FlushCount int  `json:"flush_count,omitempty" yaml:"flush_count,omitempty"`
}

type LegacyHookConfig struct {
	Enabled bool                             `json:"enabled" yaml:"enabled"`
	Events  map[string]LegacyHookEventConfig `json:"events,omitempty" yaml:"events,omitempty"`
}

type LegacyHookEventConfig struct {
	Recall HookRecallConfig `json:"recall" yaml:"recall"`
}

type TelemetryConfig struct {
	Enabled             *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Dir                 string `json:"dir,omitempty" yaml:"dir,omitempty"`
	EventsFile          string `json:"events_file,omitempty" yaml:"events_file,omitempty"`
	MetricsFile         string `json:"metrics_file,omitempty" yaml:"metrics_file,omitempty"`
	MaxEventFileBytes   int64  `json:"max_event_file_bytes,omitempty" yaml:"max_event_file_bytes,omitempty"`
	MaxEventFiles       int    `json:"max_event_files,omitempty" yaml:"max_event_files,omitempty"`
	RetentionDays       int    `json:"retention_days,omitempty" yaml:"retention_days,omitempty"`
	CaptureQueryPreview bool   `json:"capture_query_preview,omitempty" yaml:"capture_query_preview,omitempty"`
	QueryPreviewChars   int    `json:"query_preview_chars,omitempty" yaml:"query_preview_chars,omitempty"`
}

func DefaultConfigPath() string {
	if path := os.Getenv("PAXM_CONFIG"); path != "" {
		return ExpandPath(path)
	}
	return filepath.Join(defaultConfigDir(), "config.yaml")
}

func defaultConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(ExpandPath(dir), "paxm")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".paxm"
	}
	return filepath.Join(home, ".config", "paxm")
}

func DefaultDataPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(ExpandPath(dir), "paxm", "memory.sqlite")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".paxm", "memory.sqlite")
	}
	return filepath.Join(home, ".local", "share", "paxm", "memory.sqlite")
}

func DefaultStateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(ExpandPath(dir), "paxm")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".paxm", "state")
	}
	return filepath.Join(home, ".local", "state", "paxm")
}

func DefaultConfig(configPath string) Config {
	configPath = ExpandPath(configPath)
	dataPath := DefaultDataPath()
	if configPath != "" && configPath != DefaultConfigPath() {
		dataPath = filepath.Join(filepath.Dir(configPath), "memory.sqlite")
	}
	defaultRecallProfile := RecallProfileConfig{
		Providers: []ProviderRouteConfig{
			{Name: "sqlite", Required: true, Weight: defaultProviderRouteWeight},
		},
		MaxResults: defaultRecallMaxResults,
		Thresholds: RecallThresholdConfig{
			MinRelevance: defaultRecallMinRelevance,
			MinScore:     defaultRecallMinScore,
		},
		Ranking: RankingConfig{
			Type: "weighted_relevance",
		},
		Tiers: []string{"stm", "ltm"},
	}
	defaultWriteRoutes := []ProviderRouteConfig{
		{Name: "sqlite", Required: true},
	}
	return Config{
		Version: defaultConfigVersion,
		Providers: map[string]ProviderConfig{
			"sqlite": {
				Type:    "sqlite",
				Enabled: true,
				Path:    dataPath,
			},
			"zep": {
				Type:        "zep",
				Enabled:     false,
				SearchScope: "episodes",
			},
			"mem0": {
				Type:    "mem0",
				Enabled: false,
				BaseURL: defaultMem0BaseURL,
			},
			"jsonrpc": {
				Type:      "jsonrpc",
				Enabled:   false,
				Transport: defaultJSONRPCTransport,
				Timeout:   defaultJSONRPCTimeout,
			},
		},
		RecallProfiles: map[string]RecallProfileConfig{
			"default":         defaultRecallProfile,
			"passive":         PassiveRecallProfileFrom(defaultRecallProfile),
			"passive_initial": PassiveInitialRecallProfileFrom(defaultRecallProfile),
		},
		WriteProfiles: map[string]WriteProfileConfig{
			"default": LTMWriteProfileFrom(defaultWriteRoutes),
			"stm":     STMWriteProfileFrom(defaultWriteRoutes),
			"ltm":     LTMWriteProfileFrom(defaultWriteRoutes),
		},
		Agents: map[string]AgentConfig{
			"claude": {
				Enabled: false,
				ActiveRecall: ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
					Output:  "markdown",
				},
				Hooks: map[string]AgentHookConfig{
					"session_start": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Claude Code session started.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "session_start",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
					"user_input": {
						Recall: HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    defaultHookRecallMaxResults,
							Output:        "markdown",
							Insertion: HookInsertionConfig{
								MinScore:          defaultHookInsertionMinScore,
								MaxItems:          defaultHookInsertionMaxItems,
								RequireQueryTerms: true,
							},
							Initial: defaultInitialHookRecall(),
						},
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Claude Code user input:\n{{ .prompt }}\n\nEvent:\n{{ .raw_json }}",
							Mode:     "user_input",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
					"turn_end": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Claude Code turn ended.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "turn_end",
							Buffer: HookBufferConfig{
								Enabled:    true,
								Flush:      true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
				},
			},
			"codex": {
				Enabled: true,
				ActiveRecall: ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
					Output:  "markdown",
				},
				Hooks: map[string]AgentHookConfig{
					"session_start": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Session started.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "session_start",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
					"user_input": {
						Recall: HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    defaultHookRecallMaxResults,
							Output:        "markdown",
							Insertion: HookInsertionConfig{
								MinScore:          defaultHookInsertionMinScore,
								MaxItems:          defaultHookInsertionMaxItems,
								RequireQueryTerms: true,
							},
							Initial: defaultInitialHookRecall(),
						},
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "User input:\n{{ .prompt }}\n\nEvent:\n{{ .raw_json }}",
							Mode:     "user_input",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
					"turn_end": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Turn ended.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "turn_end",
							Buffer: HookBufferConfig{
								Enabled:    true,
								Flush:      true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
				},
			},
			"pi": {
				Enabled: false,
				ActiveRecall: ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
					Output:  "markdown",
				},
				Hooks: map[string]AgentHookConfig{
					"user_input": {
						Recall: HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    defaultHookRecallMaxResults,
							Output:        "markdown",
							Insertion: HookInsertionConfig{
								MinScore:          defaultHookInsertionMinScore,
								MaxItems:          defaultHookInsertionMaxItems,
								RequireQueryTerms: true,
							},
							Initial: defaultInitialHookRecall(),
						},
					},
					"turn_end": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: "Pi turn ended.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "turn_end",
							Buffer: HookBufferConfig{
								Enabled:    true,
								Flush:      true,
								FlushCount: defaultHookBufferFlushCount,
							},
						},
					},
				},
			},
		},
		Telemetry: defaultTelemetryConfig(configPath),
	}
}

func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	path = ExpandPath(path)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			legacyPath := legacyJSONPath(path)
			if legacyPath != path {
				if legacyFile, legacyErr := os.Open(legacyPath); legacyErr == nil {
					defer legacyFile.Close()
					var cfg Config
					if decodeErr := decodeConfig(legacyFile, legacyPath, &cfg); decodeErr != nil {
						return Config{}, decodeErr
					}
					if validateErr := Validate(cfg); validateErr != nil {
						return Config{}, validateErr
					}
					return Normalize(cfg), nil
				}
			}
			return Config{}, fmt.Errorf("%w: %s", ErrConfigMissing, path)
		}
		return Config{}, err
	}
	defer file.Close()

	var cfg Config
	if err := decodeConfig(file, path, &cfg); err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return Normalize(cfg), nil
}

func Save(path string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	if path == "" {
		path = DefaultConfigPath()
	}
	path = ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	return encodeConfig(file, path, Normalize(cfg))
}

func Validate(cfg Config) error {
	recallNames := sortedKeys(cfg.RecallProfiles)
	for _, name := range recallNames {
		for _, tier := range cfg.RecallProfiles[name].Tiers {
			if _, ok := canonicalTier(tier); !ok {
				return fmt.Errorf("recall profile %q has invalid tier %q; expected stm or ltm", name, tier)
			}
		}
	}

	writeNames := sortedKeys(cfg.WriteProfiles)
	for _, name := range writeNames {
		profile := cfg.WriteProfiles[name]
		tier := strings.TrimSpace(profile.Tier)
		if tier != "" {
			var ok bool
			tier, ok = canonicalTier(tier)
			if !ok {
				return fmt.Errorf("write profile %q has invalid tier %q; expected stm or ltm", name, profile.Tier)
			}
		} else if strings.EqualFold(strings.TrimSpace(name), "stm") {
			tier = "stm"
		} else {
			tier = "ltm"
		}

		expiresAfter := strings.TrimSpace(profile.ExpiresAfter)
		if tier == "ltm" {
			if expiresAfter != "" {
				return fmt.Errorf("write profile %q with tier ltm must not set expires_after", name)
			}
			continue
		}
		if expiresAfter == "" {
			return fmt.Errorf("write profile %q with tier stm requires expires_after", name)
		}
		duration, err := time.ParseDuration(expiresAfter)
		if err != nil {
			return fmt.Errorf("write profile %q has invalid expires_after %q: %w", name, profile.ExpiresAfter, err)
		}
		if duration <= 0 {
			return fmt.Errorf("write profile %q expires_after must be positive", name)
		}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func canonicalTier(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stm":
		return "stm", true
	case "ltm":
		return "ltm", true
	default:
		return "", false
	}
}

func Exists(path string) bool {
	if path == "" {
		path = DefaultConfigPath()
	}
	path = ExpandPath(path)
	if _, err := os.Stat(path); err == nil {
		return true
	}
	legacyPath := legacyJSONPath(path)
	if legacyPath == path {
		return false
	}
	_, err := os.Stat(legacyPath)
	return err == nil
}

func Normalize(cfg Config) Config {
	if cfg.Version == 0 {
		cfg.Version = defaultConfigVersion
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	renamedLegacyLocal := normalizeProviders(&cfg)
	if len(cfg.RecallProfiles) == 0 {
		cfg.RecallProfiles = map[string]RecallProfileConfig{
			"default": legacyRecallProfile(cfg.Providers),
		}
	}
	for name, profile := range cfg.RecallProfiles {
		cfg.RecallProfiles[name] = normalizeRecallProfile(profile)
	}
	if _, ok := cfg.RecallProfiles["passive"]; !ok {
		cfg.RecallProfiles["passive"] = PassiveRecallProfileFrom(cfg.RecallProfiles["default"])
	}
	if _, ok := cfg.RecallProfiles["passive_initial"]; !ok {
		base, ok := cfg.RecallProfiles["passive"]
		if !ok {
			base = cfg.RecallProfiles["default"]
		}
		cfg.RecallProfiles["passive_initial"] = PassiveInitialRecallProfileFrom(base)
	}
	if len(cfg.WriteProfiles) == 0 {
		cfg.WriteProfiles = map[string]WriteProfileConfig{
			"default": legacyWriteProfile(cfg.Providers),
		}
	}
	if renamedLegacyLocal {
		renameProviderRoutes(&cfg, "local", "sqlite")
	}
	for name, profile := range cfg.WriteProfiles {
		cfg.WriteProfiles[name] = normalizeWriteProfile(name, profile)
	}
	ensureMemoryTierWriteProfiles(&cfg)
	if len(cfg.Agents) == 0 {
		cfg.Agents = legacyAgents(cfg.Hooks)
	}
	for name, agent := range cfg.Agents {
		cfg.Agents[name] = normalizeAgent(agent)
	}
	cfg.Telemetry = normalizeTelemetry(cfg.Telemetry)
	for name, provider := range cfg.Providers {
		provider.Read = nil
		provider.Write = nil
		provider.Required = nil
		provider.Weight = 0
		cfg.Providers[name] = provider
	}
	cfg.Hooks = nil
	return cfg
}

func normalizeProviders(cfg *Config) bool {
	normalized := make(map[string]ProviderConfig, len(cfg.Providers))
	var legacyLocal *ProviderConfig
	for name, provider := range cfg.Providers {
		if provider.Type == "" {
			provider.Type = name
		}
		if provider.Type == "local" {
			provider.Type = "sqlite"
			provider.Path = sqlitePathFromLegacyLocalPath(provider.Path)
		}
		if name == "local" && provider.Type == "sqlite" {
			provider = normalizeProviderConfig(provider)
			legacyLocal = &provider
			continue
		}
		normalized[name] = normalizeProviderConfig(provider)
	}
	if legacyLocal != nil {
		if _, exists := normalized["sqlite"]; !exists {
			normalized["sqlite"] = *legacyLocal
		}
		cfg.Providers = normalized
		return true
	}
	cfg.Providers = normalized
	return false
}

func normalizeProviderConfig(provider ProviderConfig) ProviderConfig {
	if provider.Path != "" {
		provider.Path = ExpandPath(provider.Path)
	}
	if provider.SearchScope == "" && provider.Type == "zep" {
		provider.SearchScope = "episodes"
	}
	if provider.BaseURL == "" && provider.Type == "mem0" {
		provider.BaseURL = defaultMem0BaseURL
	}
	if provider.Transport == "" && provider.Type == "jsonrpc" {
		provider.Transport = defaultJSONRPCTransport
	}
	if provider.Timeout == "" && provider.Type == "jsonrpc" {
		provider.Timeout = defaultJSONRPCTimeout
	}
	return provider
}

func renameProviderRoutes(cfg *Config, from, to string) {
	for name, profile := range cfg.RecallProfiles {
		profile.Providers = renameProviderRouteList(profile.Providers, from, to)
		cfg.RecallProfiles[name] = profile
	}
	for name, profile := range cfg.WriteProfiles {
		profile.Providers = renameProviderRouteList(profile.Providers, from, to)
		cfg.WriteProfiles[name] = profile
	}
}

func renameProviderRouteList(routes []ProviderRouteConfig, from, to string) []ProviderRouteConfig {
	renamed := routes[:0]
	indexByName := make(map[string]int, len(routes))
	for _, route := range routes {
		if route.Name == from {
			route.Name = to
		}
		if existingIndex, ok := indexByName[route.Name]; ok {
			existing := renamed[existingIndex]
			existing.Required = existing.Required || route.Required
			if route.Weight > existing.Weight {
				existing.Weight = route.Weight
			}
			renamed[existingIndex] = existing
			continue
		}
		indexByName[route.Name] = len(renamed)
		renamed = append(renamed, route)
	}
	return renamed
}

func decodeConfig(file *os.File, path string, cfg *Config) error {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return json.NewDecoder(file).Decode(cfg)
	}
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(false)
	return decoder.Decode(cfg)
}

func encodeConfig(file *os.File, path string, cfg Config) error {
	if strings.EqualFold(filepath.Ext(path), ".json") {
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		return encoder.Encode(cfg)
	}
	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)
	defer encoder.Close()
	return encoder.Encode(cfg)
}

func legacyJSONPath(path string) string {
	if !strings.EqualFold(filepath.Base(path), "config.yaml") {
		return path
	}
	return filepath.Join(filepath.Dir(path), "config.json")
}

func sqlitePathFromLegacyLocalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	if strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return strings.TrimSuffix(path, filepath.Ext(path)) + ".sqlite"
	}
	return path
}

func normalizeRecallProfile(profile RecallProfileConfig) RecallProfileConfig {
	if profile.MaxResults == 0 {
		profile.MaxResults = defaultRecallMaxResults
	}
	if profile.Thresholds == (RecallThresholdConfig{}) {
		profile.Thresholds = RecallThresholdConfig{
			MinRelevance: defaultRecallMinRelevance,
			MinScore:     defaultRecallMinScore,
		}
	}
	if profile.Ranking.Type == "" {
		profile.Ranking.Type = "weighted_relevance"
	}
	profile.Tiers = normalizeTierList(profile.Tiers)
	for i, route := range profile.Providers {
		profile.Providers[i] = normalizeProviderRoute(route)
	}
	return profile
}

func normalizeWriteProfile(name string, profile WriteProfileConfig) WriteProfileConfig {
	profile.Tier = normalizeWriteProfileTier(name, profile.Tier)
	for i, route := range profile.Providers {
		profile.Providers[i] = normalizeProviderRoute(route)
	}
	return profile
}

func ensureMemoryTierWriteProfiles(cfg *Config) {
	base := cfg.WriteProfiles["default"]
	if len(base.Providers) == 0 {
		base = legacyWriteProfile(cfg.Providers)
	}
	if _, ok := cfg.WriteProfiles["ltm"]; !ok {
		cfg.WriteProfiles["ltm"] = LTMWriteProfileFrom(base.Providers)
	}
	if _, ok := cfg.WriteProfiles["stm"]; !ok {
		cfg.WriteProfiles["stm"] = STMWriteProfileFrom(base.Providers)
	}
}

func normalizeProviderRoute(route ProviderRouteConfig) ProviderRouteConfig {
	if route.Weight == 0 {
		route.Weight = defaultProviderRouteWeight
	}
	return route
}

func DefaultMem0BaseURL() string {
	return defaultMem0BaseURL
}

func DefaultSTMExpiresAfter() string {
	return defaultSTMExpiresAfter
}

func DefaultRecallThresholds() RecallThresholdConfig {
	return RecallThresholdConfig{
		MinRelevance: defaultRecallMinRelevance,
		MinScore:     defaultRecallMinScore,
	}
}

func IsDefaultRecallProfile(profile RecallProfileConfig) bool {
	return profile.MaxResults == defaultRecallMaxResults && IsDefaultRecallThresholds(profile.Thresholds)
}

func IsDefaultRecallThresholds(thresholds RecallThresholdConfig) bool {
	defaults := DefaultRecallThresholds()
	return thresholds.MinRelevance == defaults.MinRelevance && thresholds.MinScore == defaults.MinScore
}

func ProviderRouteRequired(routes []ProviderRouteConfig, provider string) (bool, bool) {
	for _, route := range routes {
		if route.Name == provider {
			return route.Required, true
		}
	}
	return false, false
}

func UpsertProviderRoute(routes []ProviderRouteConfig, provider string, required bool) []ProviderRouteConfig {
	for i, route := range routes {
		if route.Name == provider {
			route.Required = required
			if route.Weight == 0 {
				route.Weight = defaultProviderRouteWeight
			}
			routes[i] = route
			return routes
		}
	}
	return append(routes, ProviderRouteConfig{Name: provider, Required: required, Weight: defaultProviderRouteWeight})
}

func RemoveProviderRoute(routes []ProviderRouteConfig, provider string) []ProviderRouteConfig {
	filtered := routes[:0]
	for _, route := range routes {
		if route.Name != provider {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

func normalizeTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stm":
		return "stm"
	default:
		return "ltm"
	}
}

func normalizeWriteProfileTier(name, value string) string {
	if strings.TrimSpace(value) != "" {
		return normalizeTier(value)
	}
	if strings.EqualFold(strings.TrimSpace(name), "stm") {
		return "stm"
	}
	return "ltm"
}

func normalizeTierList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		tier := normalizeTier(value)
		if _, ok := seen[tier]; ok {
			continue
		}
		seen[tier] = struct{}{}
		normalized = append(normalized, tier)
	}
	return normalized
}

func normalizeAgent(agent AgentConfig) AgentConfig {
	if agent.ActiveRecall.Profile == "" {
		agent.ActiveRecall.Profile = "default"
	}
	if agent.ActiveRecall.Output == "" {
		agent.ActiveRecall.Output = "markdown"
	}
	if agent.Hooks == nil {
		agent.Hooks = make(map[string]AgentHookConfig)
	}
	if legacyHook, ok := agent.Hooks["user_prompt"]; ok {
		if _, exists := agent.Hooks["user_input"]; !exists {
			agent.Hooks["user_input"] = legacyHook
		}
		delete(agent.Hooks, "user_prompt")
	}
	for name, hook := range agent.Hooks {
		hook = normalizeHookRecall(name, hook)
		hook = normalizeHookWrite(hook)
		agent.Hooks[name] = hook
	}
	return agent
}

func normalizeHookRecall(name string, hook AgentHookConfig) AgentHookConfig {
	if name == "user_input" && hook.Recall.Enabled && hook.Recall.Initial == nil {
		hook.Recall.Initial = defaultInitialHookRecall()
	}
	if hook.Recall.Profile == "" {
		hook.Recall.Profile = "default"
	}
	if hook.Recall.Output == "" {
		hook.Recall.Output = "markdown"
	}
	if hook.Recall.Initial != nil {
		normalizeInitialHookRecall(hook.Recall.Initial, hook.Recall)
	}
	return hook
}

func normalizeInitialHookRecall(initial *HookInitialRecall, recall HookRecallConfig) {
	if initial.Profile == "" {
		initial.Profile = recall.Profile
	}
	if initial.QueryTemplate == "" {
		initial.QueryTemplate = recall.QueryTemplate
	}
	if initial.MaxResults == 0 {
		initial.MaxResults = recall.MaxResults
	}
}

func normalizeHookWrite(hook AgentHookConfig) AgentHookConfig {
	if hook.Write.Profile == "" {
		hook.Write.Profile = "default"
	}
	if hook.Write.Template == "" {
		hook.Write.Template = "{{ .prompt }}"
	}
	if hook.Write.Mode == "" {
		hook.Write.Mode = "prompt"
	}
	if hook.Write.Enabled && !hook.Write.Buffer.Enabled {
		hook.Write.Buffer.Enabled = true
	}
	if hook.Write.Buffer.Enabled && hook.Write.Buffer.FlushCount == 0 {
		hook.Write.Buffer.FlushCount = defaultHookBufferFlushCount
	}
	return hook
}

func PassiveRecallProfileFrom(base RecallProfileConfig) RecallProfileConfig {
	return RecallProfileConfig{
		Providers:  append([]ProviderRouteConfig(nil), base.Providers...),
		MaxResults: passiveRecallMaxResults,
		Thresholds: RecallThresholdConfig{
			MinRelevance: passiveRecallMinRelevance,
			MinScore:     passiveRecallMinScore,
		},
		Ranking: RankingConfig{
			Type:         "weighted_relevance",
			RecencyBoost: base.Ranking.RecencyBoost,
		},
		Tiers: []string{"ltm"},
	}
}

func PassiveInitialRecallProfileFrom(base RecallProfileConfig) RecallProfileConfig {
	return RecallProfileConfig{
		Providers:  append([]ProviderRouteConfig(nil), base.Providers...),
		MaxResults: initialRecallMaxResults,
		Thresholds: RecallThresholdConfig{
			MinRelevance: initialRecallMinRelevance,
			MinScore:     initialRecallMinScore,
		},
		Ranking: RankingConfig{
			Type:         "weighted_relevance",
			RecencyBoost: base.Ranking.RecencyBoost,
		},
		Tiers: []string{"ltm"},
	}
}

func STMWriteProfileFrom(routes []ProviderRouteConfig) WriteProfileConfig {
	return WriteProfileConfig{
		Providers:    copyProviderRoutes(routes),
		Tier:         "stm",
		ExpiresAfter: defaultSTMExpiresAfter,
	}
}

func LTMWriteProfileFrom(routes []ProviderRouteConfig) WriteProfileConfig {
	return WriteProfileConfig{
		Providers: copyProviderRoutes(routes),
		Tier:      "ltm",
	}
}

func copyProviderRoutes(routes []ProviderRouteConfig) []ProviderRouteConfig {
	return append([]ProviderRouteConfig(nil), routes...)
}

func defaultInitialHookRecall() *HookInitialRecall {
	return &HookInitialRecall{
		Enabled:    true,
		Profile:    "passive_initial",
		MaxResults: initialRecallMaxResults,
		Insertion: HookInsertionConfig{
			MinScore: initialRecallMinScore,
			MaxItems: initialRecallMaxResults,
		},
	}
}

func defaultTelemetryConfig(configPath string) TelemetryConfig {
	enabled := true
	return TelemetryConfig{
		Enabled:           &enabled,
		Dir:               defaultTelemetryDir(configPath),
		EventsFile:        "events.jsonl",
		MetricsFile:       "metrics.json",
		MaxEventFileBytes: defaultTelemetryMaxEventFileSize,
		MaxEventFiles:     defaultTelemetryMaxEventFiles,
		RetentionDays:     defaultTelemetryRetentionDays,
		QueryPreviewChars: defaultTelemetryQueryPreview,
	}
}

func defaultTelemetryDir(configPath string) string {
	if configPath != "" && configPath != DefaultConfigPath() {
		return filepath.Join(filepath.Dir(configPath), "state")
	}
	return DefaultStateDir()
}

func normalizeTelemetry(telemetry TelemetryConfig) TelemetryConfig {
	if telemetry.Enabled == nil {
		enabled := true
		telemetry.Enabled = &enabled
	}
	if telemetry.EventsFile == "" {
		telemetry.EventsFile = "events.jsonl"
	}
	if telemetry.MetricsFile == "" {
		telemetry.MetricsFile = "metrics.json"
	}
	if telemetry.MaxEventFileBytes == 0 {
		telemetry.MaxEventFileBytes = defaultTelemetryMaxEventFileSize
	}
	if telemetry.MaxEventFiles == 0 {
		telemetry.MaxEventFiles = defaultTelemetryMaxEventFiles
	}
	if telemetry.RetentionDays == 0 {
		telemetry.RetentionDays = defaultTelemetryRetentionDays
	}
	if telemetry.QueryPreviewChars == 0 {
		telemetry.QueryPreviewChars = defaultTelemetryQueryPreview
	}
	if telemetry.Dir != "" {
		telemetry.Dir = ExpandPath(telemetry.Dir)
	}
	return telemetry
}

func legacyRecallProfile(providers map[string]ProviderConfig) RecallProfileConfig {
	routes := make([]ProviderRouteConfig, 0, len(providers))
	for name, provider := range providers {
		if !provider.Enabled || !legacyBool(provider.Read, true) {
			continue
		}
		routes = append(routes, ProviderRouteConfig{
			Name:     name,
			Required: legacyBool(provider.Required, true),
			Weight:   legacyWeight(provider.Weight),
		})
	}
	return normalizeRecallProfile(RecallProfileConfig{
		Providers:  routes,
		MaxResults: defaultRecallMaxResults,
		Thresholds: RecallThresholdConfig{
			MinRelevance: defaultRecallMinRelevance,
			MinScore:     defaultRecallMinScore,
		},
		Ranking: RankingConfig{Type: "weighted_relevance"},
	})
}

func legacyWriteProfile(providers map[string]ProviderConfig) WriteProfileConfig {
	routes := make([]ProviderRouteConfig, 0, len(providers))
	for name, provider := range providers {
		if !provider.Enabled || !legacyBool(provider.Write, true) {
			continue
		}
		routes = append(routes, ProviderRouteConfig{
			Name:     name,
			Required: legacyBool(provider.Required, true),
			Weight:   legacyWeight(provider.Weight),
		})
	}
	return normalizeWriteProfile("default", WriteProfileConfig{Providers: routes})
}

func legacyAgents(hooks map[string]LegacyHookConfig) map[string]AgentConfig {
	if len(hooks) == 0 {
		return DefaultConfig("").Agents
	}
	agents := make(map[string]AgentConfig, len(hooks))
	for name, hook := range hooks {
		agent := AgentConfig{
			Enabled: hook.Enabled,
			ActiveRecall: ActiveRecallConfig{
				Enabled: true,
				Profile: "default",
				Output:  "markdown",
			},
			Hooks: make(map[string]AgentHookConfig),
		}
		for eventName, event := range hook.Events {
			recall := event.Recall
			if recall.Profile == "" {
				recall.Profile = "default"
			}
			agent.Hooks[eventName] = AgentHookConfig{Recall: recall}
		}
		agents[name] = normalizeAgent(agent)
	}
	return agents
}

func legacyBool(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func legacyWeight(weight float64) float64 {
	if weight == 0 {
		return defaultProviderRouteWeight
	}
	return weight
}

func ExpandPath(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if len(path) > 1 && os.IsPathSeparator(path[1]) {
		return filepath.Join(home, path[2:])
	}
	return path
}
