package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

// fixedHitsProvider is a memory.Provider stub that always returns the same
// canned hits, used to exercise router-level provider allocation behaviour
// through the Engine's real Recall path.
type fixedHitsProvider struct {
	name string
	hits []memory.MemoryHit
}

func (p fixedHitsProvider) Name() string { return p.name }
func (p fixedHitsProvider) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return p.hits, nil
}
func (fixedHitsProvider) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	return memory.MemoryRef{}, nil
}
func (fixedHitsProvider) Health(context.Context) error { return nil }

// passiveAllocationTestConfig mirrors the shape the eval harness generates for
// the private_sqlite_plus_team_note arm: two required providers in the
// "passive" recall profile with an overall max_results of 5.
func passiveAllocationTestConfig() config.Config {
	cfg := config.DefaultConfig("config.yaml")
	cfg.RecallProfiles["passive"] = config.RecallProfileConfig{
		Providers: []config.ProviderRouteConfig{
			{Name: "private", Required: true},
			{Name: "team", Required: true},
		},
		MaxResults: 5,
		// Negative thresholds preserve raw top-k (matches the eval harness's
		// own PAXM_PASSIVE_MIN_RELEVANCE/MIN_SCORE default of -1). Leaving
		// this zero-value would make normalizeRecallProfile substitute 0.25,
		// which would filter the low-relevance "team" fixture below out
		// before allocation ever runs and make these tests pass for the
		// wrong reason.
		Thresholds: config.RecallThresholdConfig{MinRelevance: -1, MinScore: -1},
	}
	return cfg
}

func TestSearchPolicyProviderAllocationDefaultsToZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
	}{
		{name: "unset", set: false},
		{name: "empty", set: true, val: ""},
		{name: "zero", set: true, val: "0"},
		{name: "negative", set: true, val: "-3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(config.PassiveProviderAllocationEnv, tc.val)
			}
			engine := New(passiveAllocationTestConfig(), nil)
			policy, err := engine.searchPolicy("passive", 5)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if policy.ProviderAllocation != 0 {
				t.Fatalf("policy.ProviderAllocation = %d, want 0 (today's behaviour)", policy.ProviderAllocation)
			}
		})
	}
}

func TestSearchPolicyProviderAllocationReachesPolicyForPassiveProfile(t *testing.T) {
	t.Setenv(config.PassiveProviderAllocationEnv, "2")
	engine := New(passiveAllocationTestConfig(), nil)
	policy, err := engine.searchPolicy("passive", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.ProviderAllocation != 2 {
		t.Fatalf("policy.ProviderAllocation = %d, want 2", policy.ProviderAllocation)
	}
}

func TestSearchPolicyProviderAllocationDoesNotApplyOutsidePassiveProfiles(t *testing.T) {
	t.Setenv(config.PassiveProviderAllocationEnv, "2")
	engine := New(passiveAllocationTestConfig(), nil)
	policy, err := engine.searchPolicy("default", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.ProviderAllocation != 0 {
		t.Fatalf("policy.ProviderAllocation = %d, want 0 for the default profile", policy.ProviderAllocation)
	}
}

func TestSearchPolicyProviderAllocationMalformedValueErrorsNamingVariable(t *testing.T) {
	t.Setenv(config.PassiveProviderAllocationEnv, "two")
	engine := New(passiveAllocationTestConfig(), nil)
	_, err := engine.searchPolicy("passive", 5)
	if err == nil {
		t.Fatal("expected an error for a malformed PAXM_PASSIVE_PROVIDER_ALLOCATION")
	}
	if !strings.Contains(err.Error(), config.PassiveProviderAllocationEnv) {
		t.Fatalf("error %q does not name the variable", err.Error())
	}
	if !strings.Contains(err.Error(), "two") {
		t.Fatalf("error %q does not include the malformed value", err.Error())
	}
}

// starvationFixtureRouter builds the two-provider router used by both
// starvation tests below: a "private" provider with five hits at
// consistently high relevance (like a verbatim source-event store, where
// many chunks match strongly) and a "team" provider with a single hit at
// much lower relevance (like one extracted note). The router's per-provider
// rank^2 calibration (ranking.go) already discounts a provider's 2nd..5th
// hits heavily, so the quiet provider's one hit must be relevant enough to
// beat position 1 discounted by nothing, yet low enough that it still loses
// to the *undiscounted* top of a 6-hit combined ranking once the overall
// limit (5) is applied - hence 0.01, chosen to sit below every private
// hit's calibrated score including the most-decayed one.
func starvationFixtureRouter(t *testing.T) *memory.Router {
	t.Helper()
	privateHits := []memory.MemoryHit{
		{Provider: "private", ID: "p1", Relevance: 1.0},
		{Provider: "private", ID: "p2", Relevance: 0.9},
		{Provider: "private", ID: "p3", Relevance: 0.8},
		{Provider: "private", ID: "p4", Relevance: 0.7},
		{Provider: "private", ID: "p5", Relevance: 0.6},
	}
	teamHits := []memory.MemoryHit{
		{Provider: "team", ID: "t1", Relevance: 0.01},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{
		{Provider: fixedHitsProvider{name: "private", hits: privateHits}, Read: true},
		{Provider: fixedHitsProvider{name: "team", hits: teamHits}, Read: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

// TestRecallWithoutProviderAllocationStarvesTheQuietProvider documents the
// pre-change behaviour this task fixes: with the env var unset, the two
// providers compete on one global top-N and the quiet "team" provider is
// crowded out entirely.
func TestRecallWithoutProviderAllocationStarvesTheQuietProvider(t *testing.T) {
	router := starvationFixtureRouter(t)
	engine := New(passiveAllocationTestConfig(), router)
	result, err := engine.Recall(context.Background(), RecallInput{Query: "q", Profile: "passive"})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range result.Hits {
		if hit.Provider == "team" {
			t.Fatalf("expected the team provider's hit to be starved without allocation, hits = %#v", result.Hits)
		}
	}
}

// TestRecallAppliesProviderAllocationAcrossProviders proves the allocation
// reaches the router and actually changes what Recall returns for the exact
// same fixture that starves the quiet provider above: with the env var set,
// the team provider's one hit must now survive.
func TestRecallAppliesProviderAllocationAcrossProviders(t *testing.T) {
	t.Setenv(config.PassiveProviderAllocationEnv, "2")
	router := starvationFixtureRouter(t)
	engine := New(passiveAllocationTestConfig(), router)
	result, err := engine.Recall(context.Background(), RecallInput{Query: "q", Profile: "passive"})
	if err != nil {
		t.Fatal(err)
	}
	sawTeam := false
	for _, hit := range result.Hits {
		if hit.Provider == "team" {
			sawTeam = true
		}
	}
	if !sawTeam {
		t.Fatalf("expected the team provider's hit to survive with allocation set, hits = %#v", result.Hits)
	}
}
