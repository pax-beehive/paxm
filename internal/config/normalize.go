package config

import (
	"strings"
	"unicode"
)

func Normalize(cfg Config) Config {
	if cfg.Version == 0 {
		cfg.Version = defaultConfigVersion
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	renamedLegacyLocal := normalizeProviders(&cfg)
	normalizeProfiles(&cfg, renamedLegacyLocal)
	normalizeAgents(&cfg)
	normalizeIdentity(&cfg)
	normalizeRuntime(&cfg)
	return cfg
}

func normalizeIdentity(cfg *Config) {
	cfg.Identity.UserID = normalizeUserID(cfg.Identity.UserID)
	for name, agent := range cfg.Agents {
		agent.AgentID = slugID(agent.AgentID)
		if agent.AgentID == "" && cfg.Identity.UserID != "" {
			agent.AgentID = slugID(name + "-" + cfg.Identity.UserID)
		}
		cfg.Agents[name] = agent
	}
	for name, profile := range cfg.WriteProfiles {
		profile.Scope.Type = strings.ToLower(strings.TrimSpace(profile.Scope.Type))
		if profile.Scope.Type == "personal" {
			profile.Scope.ID = normalizeUserID(profile.Scope.ID)
		} else {
			profile.Scope.ID = slugID(profile.Scope.ID)
		}
		if profile.Scope.Type == "" && profile.Scope.ID == "" && cfg.Identity.UserID != "" {
			profile.Scope = MemoryScopeConfig{Type: "personal", ID: cfg.Identity.UserID}
		}
		cfg.WriteProfiles[name] = profile
	}
}

func normalizeUserID(value string) string {
	value = strings.TrimSpace(value)
	if isOpaqueTeamMemoryUserID(value) {
		return value
	}
	return slugID(value)
}

func isOpaqueTeamMemoryUserID(value string) bool {
	if !strings.HasPrefix(value, "usr_") || len(value) == len("usr_") {
		return false
	}
	for _, char := range value[len("usr_"):] {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func SlugID(value string) string { return slugID(value) }

func DefaultAgentID(agentName, userID string) string {
	if slugID(userID) == "" {
		return ""
	}
	return slugID(agentName + "-" + userID)
}

func slugID(value string) string {
	var result strings.Builder
	lastDash := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			result.WriteRune(char)
			lastDash = false
		case result.Len() > 0 && !lastDash:
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func normalizeProfiles(cfg *Config, renamedLegacyLocal bool) {
	if len(cfg.RecallProfiles) == 0 {
		cfg.RecallProfiles = map[string]RecallProfileConfig{"default": legacyRecallProfile(cfg.Providers)}
	}
	for name, profile := range cfg.RecallProfiles {
		profile = normalizeRecallProfile(profile)
		if name == "passive" || name == "passive_initial" {
			profile.Providers = normalizePassiveProviderRoutes(profile.Providers, cfg.Providers)
		}
		cfg.RecallProfiles[name] = profile
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
		cfg.WriteProfiles = map[string]WriteProfileConfig{"default": legacyWriteProfile(cfg.Providers)}
	}
	if renamedLegacyLocal {
		renameProviderRoutes(cfg, "local", "sqlite")
	}
	for name, profile := range cfg.WriteProfiles {
		cfg.WriteProfiles[name] = normalizeWriteProfile(name, profile)
	}
	ensureMemoryTierWriteProfiles(cfg)
}

func normalizePassiveProviderRoutes(routes []ProviderRouteConfig, providers map[string]ProviderConfig) []ProviderRouteConfig {
	for i := range routes {
		route := &routes[i]
		providerType := providers[route.Name].Type
		if route.Timeout == "" || (isManagedCloudProvider(providerType) && route.Timeout == defaultProviderRecallTimeout) {
			route.Timeout = DefaultProviderRecallTimeout(providerType)
		}
		if isManagedCloudProvider(providerType) && route.Thresholds == nil {
			route.Thresholds = defaultCloudThresholds()
		}
	}
	return routes
}

func isManagedCloudProvider(providerType string) bool {
	return providerType == "mem0-cloud" || providerType == "memos-cloud"
}

func normalizeAgents(cfg *Config) {
	if len(cfg.Agents) == 0 {
		cfg.Agents = legacyAgents(cfg.Hooks)
	}
	for name, agent := range cfg.Agents {
		cfg.Agents[name] = normalizeAgent(agent)
	}
}

func normalizeRuntime(cfg *Config) {
	cfg.Telemetry = normalizeTelemetry(cfg.Telemetry)
	if cfg.CaptureQueue.Path != "" {
		cfg.CaptureQueue.Path = ExpandPath(cfg.CaptureQueue.Path)
	}
	if cfg.CaptureQueue.MaxEpisodeAge == "" {
		cfg.CaptureQueue.MaxEpisodeAge = defaultCaptureMaxEpisodeAge
	}
	if cfg.CaptureQueue.RetryMin == "" {
		cfg.CaptureQueue.RetryMin = defaultCaptureRetryMin
	}
	if cfg.CaptureQueue.ProviderConcurrency == nil {
		cfg.CaptureQueue.ProviderConcurrency = make(map[string]int)
	}
	if _, ok := cfg.CaptureQueue.ProviderConcurrency["sqlite"]; !ok {
		cfg.CaptureQueue.ProviderConcurrency["sqlite"] = 1
	}
	if _, ok := cfg.CaptureQueue.ProviderConcurrency["default"]; !ok {
		cfg.CaptureQueue.ProviderConcurrency["default"] = 4
	}
	if cfg.CaptureQueue.MaxAttempts == 0 {
		cfg.CaptureQueue.MaxAttempts = defaultCaptureMaxAttempts
	}
	for name, provider := range cfg.Providers {
		provider.Read = nil
		provider.Write = nil
		provider.Required = nil
		provider.Weight = 0
		cfg.Providers[name] = provider
	}
	cfg.Hooks = nil
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
	if provider.BaseURL == "" && provider.Type == "mem0-cloud" {
		provider.BaseURL = defaultMem0CloudBaseURL
	}
	provider = normalizeMem0ScoreSemantics(provider)
	provider = normalizeMem0SearchScopePayload(provider)
	if provider.BaseURL == "" && provider.Type == "memos" {
		provider.BaseURL = defaultMemOSBaseURL
	}
	if provider.SearchMode == "" && provider.Type == "memos" {
		provider.SearchMode = "fast"
	}
	if provider.BaseURL == "" && provider.Type == "memos-cloud" {
		provider.BaseURL = defaultMemOSCloudBaseURL
	}
	if provider.Type == "mem0-cloud" && provider.Infer == nil {
		infer := false
		provider.Infer = &infer
	}
	if provider.Transport == "" && provider.Type == "jsonrpc" {
		provider.Transport = defaultJSONRPCTransport
	}
	if provider.Timeout == "" && provider.Type == "jsonrpc" {
		provider.Timeout = defaultJSONRPCTimeout
	}
	return provider
}

func normalizeMem0SearchScopePayload(provider ProviderConfig) ProviderConfig {
	if provider.Type == "mem0" {
		provider.SearchScopePayload = string(NormalizeMem0SearchScopePayload(provider.SearchScopePayload))
	}
	return provider
}

func normalizeMem0ScoreSemantics(provider ProviderConfig) ProviderConfig {
	if provider.Type == "mem0" || provider.Type == "mem0-cloud" {
		provider.ScoreSemantics = string(NormalizeScoreSemantics(provider.ScoreSemantics))
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
		if profile.Providers[i].Timeout == "" {
			profile.Providers[i].Timeout = defaultProviderWriteTimeout
		}
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
	if hook.Recall.Enabled && hook.Recall.Timeout == "" && hook.Recall.TimeoutExtra == "" {
		hook.Recall.TimeoutExtra = defaultPassiveRecallTimeoutExtra
	}
	if hook.Recall.Timeout == defaultPassiveRecallTimeout && hook.Recall.TimeoutExtra == "" {
		hook.Recall.Timeout = ""
		hook.Recall.TimeoutExtra = defaultPassiveRecallTimeoutExtra
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
	if initial.Timeout == "" && initial.TimeoutExtra == "" {
		initial.Timeout = recall.Timeout
		initial.TimeoutExtra = recall.TimeoutExtra
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
