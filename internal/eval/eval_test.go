package eval

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

func TestBaselineSuiteHasOneHundredValidatedCases(t *testing.T) {
	suite, err := Load(filepath.Join("..", "..", "evals", "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 100 {
		t.Fatalf("baseline has %d cases, want 100", len(suite.Cases))
	}
	categories := map[string]int{}
	for _, item := range suite.Cases {
		categories[item.Category]++
	}
	if len(categories) != 10 {
		t.Fatalf("baseline has %d categories, want 10", len(categories))
	}
	for name, count := range categories {
		if count != 10 {
			t.Fatalf("category %s has %d cases, want 10", name, count)
		}
	}
}

func TestSuiteValidationRejectsUnknownExpectedMemory(t *testing.T) {
	suite := Suite{Version: SuiteVersion, Name: "invalid", Cases: []Case{{
		ID: "one", Category: "active", Memories: []Memory{{ID: "known", Text: "known fact"}},
		Recall: Recall{Mode: "active", Query: "known fact"}, Expected: []string{"missing"},
	}}}
	if err := suite.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRunnerUsesActiveAndPassiveProductionPaths(t *testing.T) {
	suite := Suite{Version: SuiteVersion, Name: "runner", Cases: []Case{
		{ID: "active", Category: "active", Memories: []Memory{{ID: "active-hit", Text: "violet beacon active fact", Tier: memory.TierLTM}, {ID: "active-noise", Text: "unrelated recipe"}}, Recall: Recall{Mode: "active", Query: "violet beacon active fact", Limit: 3}, Expected: []string{"active-hit"}, Forbidden: []string{"active-noise"}},
		{ID: "passive", Category: "passive", Memories: []Memory{{ID: "passive-hit", Text: "silver meadow passive fact", Tier: memory.TierLTM}, {ID: "passive-noise", Text: "unrelated weather"}}, Recall: Recall{Mode: "passive", Query: "silver meadow passive fact", Limit: 2}, Expected: []string{"passive-hit"}, Forbidden: []string{"passive-noise"}},
	}}
	result, err := (Runner{Root: t.TempDir()}).Run(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed != 2 || result.Failed != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.RecallAtK != 1 || result.MRR != 1 || result.FalsePositiveRate != 0 {
		t.Fatalf("unexpected metrics: %#v", result)
	}
}

func TestCaseAssertionsCheckOrderLimitAndUnexpectedHits(t *testing.T) {
	scenario := Case{Expected: []string{"best"}, ExpectedFirst: "best", MaxHits: 1}
	result := CaseResult{HitIDs: []string{"weaker", "best"}}
	result.score(scenario)
	if result.Passed {
		t.Fatal("expected order and limit assertions to fail")
	}
	if len(result.Unexpected) != 1 || result.Unexpected[0] != "weaker" {
		t.Fatalf("unexpected hits = %#v", result.Unexpected)
	}
	if result.ReciprocalRank != 0.5 {
		t.Fatalf("reciprocal rank = %v, want 0.5", result.ReciprocalRank)
	}
}
