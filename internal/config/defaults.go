package config

import "path/filepath"

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
		{Name: "sqlite", Required: true, Timeout: defaultProviderWriteTimeout},
	}
	inferFalse := false
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
				Type:               "mem0",
				Enabled:            false,
				BaseURL:            defaultMem0BaseURL,
				ScoreSemantics:     string(ScoreSemanticsSimilarity),
				SearchScopePayload: string(Mem0SearchScopePayloadAuto),
			},
			"mem0_cloud": {
				Type:           "mem0-cloud",
				Enabled:        false,
				BaseURL:        defaultMem0CloudBaseURL,
				ScoreSemantics: string(ScoreSemanticsSimilarity),
				Infer:          &inferFalse,
			},
			"memos": {
				Type:       "memos",
				Enabled:    false,
				BaseURL:    defaultMemOSBaseURL,
				SearchMode: "fast",
			},
			"memos_cloud": {
				Type:    "memos-cloud",
				Enabled: false,
				BaseURL: defaultMemOSCloudBaseURL,
			},
			"openviking": {
				Type:    "openviking",
				Enabled: false,
				BaseURL: defaultOpenVikingBaseURL,
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
			"opencode": {
				Enabled:      false,
				ActiveRecall: ActiveRecallConfig{Enabled: true, Profile: "default", Output: "markdown"},
				Hooks: map[string]AgentHookConfig{
					"user_input": {
						Recall: HookRecallConfig{
							Enabled: true, Profile: "passive", QueryTemplate: "{{ .prompt }}", MaxResults: defaultHookRecallMaxResults,
							TimeoutExtra: defaultPassiveRecallTimeoutExtra, Output: "markdown",
							Insertion: HookInsertionConfig{MinScore: defaultHookInsertionMinScore, MaxItems: defaultHookInsertionMaxItems, RequireQueryTerms: true},
							Initial:   defaultInitialHookRecall(),
						},
					},
					"turn_end": {
						Write: HookWriteConfig{Enabled: true, Profile: "ltm", Template: defaultHookWriteTemplate, Mode: "turn_end", Buffer: HookBufferConfig{Enabled: true, Flush: true, FlushCount: defaultHookBufferFlushCount}},
					},
				},
			},
			"claude": {
				Enabled: false,
				ActiveRecall: ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
					Output:  "markdown",
				},
				Hooks: map[string]AgentHookConfig{
					"tool_use":     defaultToolWriteHook("tool_use"),
					"tool_failure": defaultToolWriteHook("tool_failure"),
					"session_start": {
						Write: HookWriteConfig{
							Enabled:  true,
							Profile:  "ltm",
							Template: defaultHookWriteTemplate,
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
							TimeoutExtra:  defaultPassiveRecallTimeoutExtra,
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
							Template: defaultHookWriteTemplate,
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
							Template: defaultHookWriteTemplate,
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
							Template: defaultHookWriteTemplate,
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
							TimeoutExtra:  defaultPassiveRecallTimeoutExtra,
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
							Template: defaultHookWriteTemplate,
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
							Template: defaultHookWriteTemplate,
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
							TimeoutExtra:  defaultPassiveRecallTimeoutExtra,
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
							Template: defaultHookWriteTemplate,
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
			"cursor":  defaultCompatibleAgent(false),
			"trae":    defaultCompatibleAgent(true),
			"trae-cn": defaultCompatibleAgent(true),
			"kimi":    defaultCompatibleAgent(true),
			"zcode":   defaultCompatibleAgent(true),
			"kiro":    defaultCompatibleAgent(true),
			"cline":   defaultCompatibleAgent(true),
		},
		Telemetry: defaultTelemetryConfig(configPath),
		CaptureQueue: CaptureQueueConfig{
			MaxEpisodeAge: defaultCaptureMaxEpisodeAge,
			RetryMin:      defaultCaptureRetryMin,
			MaxAttempts:   defaultCaptureMaxAttempts,
			ProviderConcurrency: map[string]int{
				"sqlite":  1,
				"default": 4,
			},
		},
	}
}

func defaultCompatibleAgent(passiveRecall bool) AgentConfig {
	userInput := AgentHookConfig{
		Write: HookWriteConfig{
			Enabled:  true,
			Profile:  "ltm",
			Template: defaultHookWriteTemplate,
			Mode:     "user_input",
			Buffer: HookBufferConfig{
				Enabled:    true,
				FlushCount: defaultHookBufferFlushCount,
			},
		},
	}
	if passiveRecall {
		userInput.Recall = HookRecallConfig{
			Enabled:       true,
			Profile:       "passive",
			QueryTemplate: "{{ .prompt }}",
			MaxResults:    defaultHookRecallMaxResults,
			TimeoutExtra:  defaultPassiveRecallTimeoutExtra,
			Output:        "markdown",
			Insertion: HookInsertionConfig{
				MinScore:          defaultHookInsertionMinScore,
				MaxItems:          defaultHookInsertionMaxItems,
				RequireQueryTerms: true,
			},
			Initial: defaultInitialHookRecall(),
		}
	}
	return AgentConfig{
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
					Template: defaultHookWriteTemplate,
					Mode:     "session_start",
					Buffer: HookBufferConfig{
						Enabled:    true,
						FlushCount: defaultHookBufferFlushCount,
					},
				},
			},
			"user_input": userInput,
			"turn_end": {
				Write: HookWriteConfig{
					Enabled:  true,
					Profile:  "ltm",
					Template: defaultHookWriteTemplate,
					Mode:     "turn_end",
					Buffer: HookBufferConfig{
						Enabled:    true,
						Flush:      true,
						FlushCount: defaultHookBufferFlushCount,
					},
				},
			},
		},
	}
}

func defaultToolWriteHook(mode string) AgentHookConfig {
	return AgentHookConfig{Write: HookWriteConfig{
		Enabled: true, Profile: "ltm", Template: defaultHookWriteTemplate, Mode: mode,
		Buffer: HookBufferConfig{Enabled: true, FlushCount: defaultHookBufferFlushCount},
	}}
}

func DefaultMem0BaseURL() string {
	return defaultMem0BaseURL
}

func DefaultMem0CloudBaseURL() string {
	return defaultMem0CloudBaseURL
}

func DefaultMemOSBaseURL() string { return defaultMemOSBaseURL }

func DefaultMemOSCloudBaseURL() string { return defaultMemOSCloudBaseURL }

func DefaultOpenVikingBaseURL() string { return defaultOpenVikingBaseURL }

func DefaultProviderRecallTimeout(providerType string) string {
	if isManagedCloudProvider(providerType) {
		return defaultCloudRecallTimeout
	}
	return defaultProviderRecallTimeout
}

func defaultCloudThresholds() *RecallThresholdConfig {
	return &RecallThresholdConfig{MinRelevance: defaultCloudRecallThreshold, MinScore: defaultCloudRecallThreshold}
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

func PassiveRecallProfileFrom(base RecallProfileConfig) RecallProfileConfig {
	routes := append([]ProviderRouteConfig(nil), base.Providers...)
	for i := range routes {
		routes[i].Timeout = defaultProviderRecallTimeout
	}
	return RecallProfileConfig{
		Providers:  routes,
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
	routes := append([]ProviderRouteConfig(nil), base.Providers...)
	for i := range routes {
		routes[i].Timeout = defaultProviderRecallTimeout
	}
	return RecallProfileConfig{
		Providers:  routes,
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
		// CaptureQueryPreview stays unset so previews default to off.
	}
}

func defaultTelemetryDir(configPath string) string {
	if configPath != "" && configPath != DefaultConfigPath() {
		return filepath.Join(filepath.Dir(configPath), "state")
	}
	return DefaultStateDir()
}
