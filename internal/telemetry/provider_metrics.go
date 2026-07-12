package telemetry

import (
	"strings"

	"github.com/pax-beehive/paxm/internal/memory"
)

func providerCountersForEvent(event Event) map[string]Counter {
	counters := make(map[string]Counter)
	for provider, count := range event.ProviderRecalls {
		counter := counters[provider]
		counter.Recalls += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderWrites {
		counter := counters[provider]
		counter.Writes += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderHits {
		counter := counters[provider]
		counter.Hits += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderRefs {
		counter := counters[provider]
		counter.Refs += count
		counters[provider] = counter
	}
	for _, providerErr := range event.ProviderErrorDetails {
		counter := counters[providerErr.Provider]
		counter.ProviderErrors++
		counter.Errors++
		counters[providerErr.Provider] = counter
	}
	for _, recall := range event.ProviderRecallDetails {
		counter := counters[recall.Provider]
		counter.ProviderRecallSamples++
		counter.ProviderRecallDurationMS += recall.DurationMS
		counter.ProviderRecallLatencyBuckets = observeRecallLatency(counter.ProviderRecallLatencyBuckets, recall.DurationMS)
		if recall.Outcome == memory.ProviderRecallTimeout {
			counter.ProviderRecallTimeouts++
		}
		if recall.BulkheadBusy {
			counter.ProviderRecallBulkheadSkips++
		}
		counters[recall.Provider] = counter
	}
	if event.Kind == "hook_delivery" && event.Success && !event.Skipped && strings.TrimSpace(event.Provider) != "" {
		counter := counters[event.Provider]
		counter.ProviderWriteSamples++
		counter.ProviderWriteDurationMS += event.ProviderDurationMS
		counter.PassiveWriteLatencyTotalMS += event.PassiveWriteLatencyTotalMS
		counter.PassiveWriteSamples += event.PassiveWriteSamples
		counters[event.Provider] = counter
	}
	for provider, counter := range counters {
		if provider == "" {
			delete(counters, provider)
			continue
		}
		counter.Events = 1
		counters[provider] = counter
	}
	return counters
}

var providerRecallLatencyBoundsMS = [...]int64{10, 25, 50, 100, 250, 500, 800, 1000, 2000}

func observeRecallLatency(buckets []int, durationMS int64) []int {
	if len(buckets) != len(providerRecallLatencyBoundsMS)+1 {
		buckets = make([]int, len(providerRecallLatencyBoundsMS)+1)
	}
	index := len(providerRecallLatencyBoundsMS)
	for i, bound := range providerRecallLatencyBoundsMS {
		if durationMS <= bound {
			index = i
			break
		}
	}
	buckets[index]++
	return buckets
}

func ProviderRecallP95MS(counter Counter) int64 {
	if counter.ProviderRecallSamples == 0 || len(counter.ProviderRecallLatencyBuckets) == 0 {
		return 0
	}
	target := (counter.ProviderRecallSamples*95 + 99) / 100
	seen := 0
	for i, count := range counter.ProviderRecallLatencyBuckets {
		seen += count
		if seen >= target {
			if i < len(providerRecallLatencyBoundsMS) {
				return providerRecallLatencyBoundsMS[i]
			}
			return providerRecallLatencyBoundsMS[len(providerRecallLatencyBoundsMS)-1]
		}
	}
	return 0
}
