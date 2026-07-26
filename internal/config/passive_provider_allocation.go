package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PassiveProviderAllocationEnv is the environment variable that sets
// memory.SearchPolicy.ProviderAllocation for passive recall, alongside the
// PAXM_PASSIVE_MIN_RELEVANCE / PAXM_PASSIVE_MIN_SCORE /
// PAXM_PASSIVE_PROVIDER_TIMEOUT knobs the eval harness already sets for the
// same recall path.
const PassiveProviderAllocationEnv = "PAXM_PASSIVE_PROVIDER_ALLOCATION"

// ProviderAllocationFromEnv reads PAXM_PASSIVE_PROVIDER_ALLOCATION and
// returns the value to use for memory.SearchPolicy.ProviderAllocation.
//
// Unset, empty, zero, or negative all resolve to 0 - today's behaviour, one
// global top-N truncation with no per-provider allocation - because every
// existing caller depends on that being a no-op. A value that fails to
// parse as an integer is treated as a caller configuration mistake, not a
// silent fallback: it is reported as an error naming the variable and the
// value that failed to parse, following the same convention as intEnv in
// team-memory's cmd/eval-v2-memory.
func ProviderAllocationFromEnv() (int, error) {
	raw := strings.TrimSpace(os.Getenv(PassiveProviderAllocationEnv))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", PassiveProviderAllocationEnv, raw, err)
	}
	if value <= 0 {
		return 0, nil
	}
	return value, nil
}
