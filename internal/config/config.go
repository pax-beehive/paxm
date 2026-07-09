package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrConfigMissing = errors.New("paxm config is missing")

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
	Type              string `json:"type" yaml:"type"`
	Enabled           bool   `json:"enabled" yaml:"enabled"`
	Path              string `json:"path,omitempty" yaml:"path,omitempty"`
	APIKey            string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	BaseURL           string `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	UserID            string `json:"user_id,omitempty" yaml:"user_id,omitempty"`
	GraphID           string `json:"graph_id,omitempty" yaml:"graph_id,omitempty"`
	SearchScope       string `json:"search_scope,omitempty" yaml:"search_scope,omitempty"`
	MaxCharacters     int    `json:"max_characters,omitempty" yaml:"max_characters,omitempty"`
	SourceDescription string `json:"source_description,omitempty" yaml:"source_description,omitempty"`

	Read     *bool   `json:"read,omitempty" yaml:"read,omitempty"`
	Write    *bool   `json:"write,omitempty" yaml:"write,omitempty"`
	Required *bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Weight   float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
}

type ProviderRouteConfig struct {
	Name     string  `json:"name" yaml:"name"`
	Required bool    `json:"required" yaml:"required"`
	Weight   float64 `json:"weight,omitempty" yaml:"weight,omitempty"`
}

type RecallProfileConfig struct {
	Providers  []ProviderRouteConfig `json:"providers,omitempty" yaml:"providers,omitempty"`
	MaxResults int                   `json:"max_results,omitempty" yaml:"max_results,omitempty"`
	Thresholds RecallThresholdConfig `json:"thresholds,omitempty" yaml:"thresholds,omitempty"`
	Ranking    RankingConfig         `json:"ranking,omitempty" yaml:"ranking,omitempty"`
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
	Providers []ProviderRouteConfig `json:"providers,omitempty" yaml:"providers,omitempty"`
}

type AgentConfig struct {
	Enabled      bool                       `json:"enabled" yaml:"enabled"`
	ActiveRecall ActiveRecallConfig         `json:"active_recall,omitempty" yaml:"active_recall,omitempty"`
	Hooks        map[string]AgentHookConfig `json:"hooks,omitempty" yaml:"hooks,omitempty"`
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
		return filepath.Join(ExpandPath(dir), "paxm", "memory.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".paxm", "memory.jsonl")
	}
	return filepath.Join(home, ".local", "share", "paxm", "memory.jsonl")
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
		dataPath = filepath.Join(filepath.Dir(configPath), "memory.jsonl")
	}
	return Config{
		Version: 1,
		Providers: map[string]ProviderConfig{
			"local": {
				Type:    "local",
				Enabled: true,
				Path:    dataPath,
			},
			"zep": {
				Type:        "zep",
				Enabled:     false,
				SearchScope: "episodes",
			},
		},
		RecallProfiles: map[string]RecallProfileConfig{
			"default": {
				Providers: []ProviderRouteConfig{
					{Name: "local", Required: true, Weight: 1},
				},
				MaxResults: 3,
				Thresholds: RecallThresholdConfig{
					MinRelevance: 0.25,
					MinScore:     0.25,
				},
				Ranking: RankingConfig{
					Type: "weighted_relevance",
				},
			},
			"passive": {
				Providers: []ProviderRouteConfig{
					{Name: "local", Required: true, Weight: 1},
				},
				MaxResults: 2,
				Thresholds: RecallThresholdConfig{
					MinRelevance: 0.75,
					MinScore:     0.75,
				},
				Ranking: RankingConfig{
					Type: "weighted_relevance",
				},
			},
		},
		WriteProfiles: map[string]WriteProfileConfig{
			"default": {
				Providers: []ProviderRouteConfig{
					{Name: "local", Required: true},
				},
			},
		},
		Agents: map[string]AgentConfig{
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
							Profile:  "default",
							Template: "Session started.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "session_start",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: 10,
							},
						},
					},
					"user_input": {
						Recall: HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    2,
							Output:        "markdown",
							Insertion: HookInsertionConfig{
								MinScore:          0.8,
								MaxItems:          2,
								RequireQueryTerms: true,
							},
						},
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "default",
							Template: "User input:\n{{ .prompt }}\n\nEvent:\n{{ .raw_json }}",
							Mode:     "user_input",
							Buffer: HookBufferConfig{
								Enabled:    true,
								FlushCount: 10,
							},
						},
					},
					"turn_end": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "default",
							Template: "Turn ended.\n\nEvent:\n{{ .raw_json }}",
							Mode:     "turn_end",
							Buffer: HookBufferConfig{
								Enabled:    true,
								Flush:      true,
								FlushCount: 10,
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
	return Normalize(cfg), nil
}

func Save(path string, cfg Config) error {
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
		cfg.Version = 1
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	for name, provider := range cfg.Providers {
		if provider.Type == "" {
			provider.Type = name
		}
		if provider.Path != "" {
			provider.Path = ExpandPath(provider.Path)
		}
		if provider.SearchScope == "" && provider.Type == "zep" {
			provider.SearchScope = "episodes"
		}
		cfg.Providers[name] = provider
	}
	if len(cfg.RecallProfiles) == 0 {
		cfg.RecallProfiles = map[string]RecallProfileConfig{
			"default": legacyRecallProfile(cfg.Providers),
		}
	}
	for name, profile := range cfg.RecallProfiles {
		cfg.RecallProfiles[name] = normalizeRecallProfile(profile)
	}
	if len(cfg.WriteProfiles) == 0 {
		cfg.WriteProfiles = map[string]WriteProfileConfig{
			"default": legacyWriteProfile(cfg.Providers),
		}
	}
	for name, profile := range cfg.WriteProfiles {
		cfg.WriteProfiles[name] = normalizeWriteProfile(profile)
	}
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

func normalizeRecallProfile(profile RecallProfileConfig) RecallProfileConfig {
	if profile.MaxResults == 0 {
		profile.MaxResults = 3
	}
	if profile.Thresholds == (RecallThresholdConfig{}) {
		profile.Thresholds = RecallThresholdConfig{
			MinRelevance: 0.25,
			MinScore:     0.25,
		}
	}
	if profile.Ranking.Type == "" {
		profile.Ranking.Type = "weighted_relevance"
	}
	for i, route := range profile.Providers {
		profile.Providers[i] = normalizeProviderRoute(route)
	}
	return profile
}

func normalizeWriteProfile(profile WriteProfileConfig) WriteProfileConfig {
	for i, route := range profile.Providers {
		profile.Providers[i] = normalizeProviderRoute(route)
	}
	return profile
}

func normalizeProviderRoute(route ProviderRouteConfig) ProviderRouteConfig {
	if route.Weight == 0 {
		route.Weight = 1
	}
	return route
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
		if hook.Recall.Profile == "" {
			hook.Recall.Profile = "default"
		}
		if hook.Recall.Output == "" {
			hook.Recall.Output = "markdown"
		}
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
			hook.Write.Buffer.FlushCount = 10
		}
		agent.Hooks[name] = hook
	}
	return agent
}

func defaultTelemetryConfig(configPath string) TelemetryConfig {
	enabled := true
	return TelemetryConfig{
		Enabled:           &enabled,
		Dir:               defaultTelemetryDir(configPath),
		EventsFile:        "events.jsonl",
		MetricsFile:       "metrics.json",
		MaxEventFileBytes: 1 << 20,
		MaxEventFiles:     3,
		RetentionDays:     30,
		QueryPreviewChars: 80,
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
		telemetry.MaxEventFileBytes = 1 << 20
	}
	if telemetry.MaxEventFiles == 0 {
		telemetry.MaxEventFiles = 3
	}
	if telemetry.RetentionDays == 0 {
		telemetry.RetentionDays = 30
	}
	if telemetry.QueryPreviewChars == 0 {
		telemetry.QueryPreviewChars = 80
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
		MaxResults: 3,
		Thresholds: RecallThresholdConfig{
			MinRelevance: 0.25,
			MinScore:     0.25,
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
	return normalizeWriteProfile(WriteProfileConfig{Providers: routes})
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
		return 1
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
