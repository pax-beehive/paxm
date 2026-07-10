package runtime

import (
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
	"github.com/pax-beehive/memory-adaptor/internal/telemetry"
)

type RecallTelemetryInput struct {
	Kind      string
	Source    string
	Target    string
	HookEvent string
	Profile   string
	Result    facade.RecallResult
	Skipped   bool
	Duration  time.Duration
	Err       error
}

type RememberTelemetryInput struct {
	Kind      string
	Source    string
	Profile   string
	ItemCount int
	Result    facade.IngestResult
	Duration  time.Duration
	Err       error
}

func RecallTelemetryEvent(cfg config.Config, input RecallTelemetryInput) telemetry.Event {
	return telemetry.Event{
		Time:                 time.Now().UTC(),
		Kind:                 input.Kind,
		Source:               input.Source,
		Command:              sourceCommand(input.Source, input.Kind),
		Target:               input.Target,
		HookEvent:            input.HookEvent,
		Profile:              EffectiveRecallProfile(cfg, input.Profile),
		Success:              input.Err == nil,
		Skipped:              input.Skipped,
		DurationMS:           input.Duration.Milliseconds(),
		HitCount:             len(input.Result.Hits),
		InsertedCount:        insertedCount(input.Kind, input.Result.Hits),
		ProviderRecalls:      RecallProviderRoutes(cfg, input.Profile, input.Skipped),
		ProviderHits:         telemetry.ProviderHits(input.Result.Hits),
		ProviderErrorDetails: telemetry.ProviderErrors(input.Result.ProviderErrors),
		Error:                TelemetryError(input.Err),
	}
}

func RememberTelemetryEvent(cfg config.Config, input RememberTelemetryInput) telemetry.Event {
	return telemetry.Event{
		Time:                 time.Now().UTC(),
		Kind:                 input.Kind,
		Source:               input.Source,
		Command:              sourceCommand(input.Source, input.Kind),
		Profile:              EffectiveWriteProfile(input.Profile),
		Success:              input.Err == nil,
		DurationMS:           input.Duration.Milliseconds(),
		ItemCount:            input.ItemCount,
		RefCount:             len(input.Result.Refs),
		ProviderWrites:       WriteProviderRoutes(cfg, input.Profile),
		ProviderRefs:         telemetry.ProviderRefs(input.Result.Refs),
		ProviderErrorDetails: telemetry.ProviderErrors(input.Result.ProviderErrors),
		Error:                TelemetryError(input.Err),
	}
}

func EffectiveRecallProfile(cfg config.Config, profile string) string {
	if strings.TrimSpace(profile) != "" {
		return profile
	}
	if agent, ok := cfg.Agents["codex"]; ok && agent.ActiveRecall.Enabled && strings.TrimSpace(agent.ActiveRecall.Profile) != "" {
		return agent.ActiveRecall.Profile
	}
	return "default"
}

func EffectiveWriteProfile(profile string) string {
	if strings.TrimSpace(profile) == "" {
		return "default"
	}
	return profile
}

func RecallProviderRoutes(cfg config.Config, profile string, skipped bool) map[string]int {
	if skipped {
		return nil
	}
	recallProfile, ok := cfg.RecallProfiles[EffectiveRecallProfile(cfg, profile)]
	if !ok {
		return nil
	}
	return providerRouteCounts(recallProfile.Providers)
}

func WriteProviderRoutes(cfg config.Config, profile string) map[string]int {
	writeProfile, ok := cfg.WriteProfiles[EffectiveWriteProfile(profile)]
	if !ok {
		return nil
	}
	return providerRouteCounts(writeProfile.Providers)
}

func WriteProviderRoutesForItems(cfg config.Config, items []facade.IngestInput) map[string]int {
	counts := make(map[string]int)
	for _, item := range items {
		for provider, count := range WriteProviderRoutes(cfg, item.Profile) {
			counts[provider] += count
		}
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func TelemetryError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) <= 240 {
		return value
	}
	return value[:240]
}

func sourceCommand(source, kind string) string {
	if source == "hook" {
		return "hook"
	}
	return kind
}

func insertedCount(kind string, hits []memory.MemoryHit) int {
	if kind != "hook_recall" {
		return 0
	}
	return len(hits)
}

func providerRouteCounts(routes []config.ProviderRouteConfig) map[string]int {
	counts := make(map[string]int)
	for _, route := range routes {
		if strings.TrimSpace(route.Name) == "" {
			continue
		}
		counts[route.Name]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}
