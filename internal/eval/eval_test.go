package eval

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

func TestComparisonAndRegressionBudgetDescribeObservableChanges(t *testing.T) {
	baseline := Result{Suite: "same", Version: 1, CaseCount: 10, Passed: 10, RecallAtK: 1, PrecisionAtK: .9, MRR: 1, FalsePositiveRate: .05, WriteFalsePositiveRate: .1}
	current := Result{Suite: "same", Version: 1, CaseCount: 10, Passed: 9, RecallAtK: .9, PrecisionAtK: .8, MRR: .9, FalsePositiveRate: .1, WriteFalsePositiveRate: .2}
	comparison, err := Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.PassedDelta != -1 || math.Abs(comparison.RecallAtKDelta+.1) > 1e-9 || math.Abs(comparison.FalsePositiveRateDelta-.05) > 1e-9 {
		t.Fatalf("unexpected comparison: %#v", comparison)
	}
	if math.Abs(comparison.WriteFalsePositiveRateDelta-.1) > 1e-9 {
		t.Fatalf("write false-positive delta = %v", comparison.WriteFalsePositiveRateDelta)
	}
	failures := CheckBudget(current, Budget{MinPassRate: 1, MinRecallAtK: .95, MinPrecisionAtK: .8, MinMRR: .9, MaxFalsePositiveRate: .05})
	if len(failures) != 3 || !strings.Contains(strings.Join(failures, " "), "pass rate") {
		t.Fatalf("unexpected budget failures: %#v", failures)
	}
}

func TestComparisonRejectsIncompatibleResults(t *testing.T) {
	_, err := Compare(Result{Suite: "a", Version: 1, CaseCount: 10}, Result{Suite: "b", Version: 1, CaseCount: 10})
	if err == nil {
		t.Fatal("incompatible suites were compared")
	}
}

func TestLoadBudgetRejectsUnknownAndMissingFields(t *testing.T) {
	for _, content := range []string{
		`{"min_pass_rate":1,"min_recall_at_k":1,"min_precision_at_k":1,"min_mrr":1,"max_false_positive_rate":0,"typo":1}`,
		`{"min_pass_rate":1,"min_precision_at_k":1,"min_mrr":1,"max_false_positive_rate":0}`,
	} {
		path := filepath.Join(t.TempDir(), "budget.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBudget(path); err == nil {
			t.Fatalf("invalid budget accepted: %s", content)
		}
	}
}

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

func TestConversationWriteSuiteHasFiftyValidatedCases(t *testing.T) {
	suite, err := Load(filepath.Join("..", "..", "evals", "conversation-write"))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 50 {
		t.Fatalf("conversation-write suite has %d cases, want 50", len(suite.Cases))
	}
	categories := map[string]int{}
	for _, item := range suite.Cases {
		categories[item.Category]++
	}
	if len(categories) != 10 {
		t.Fatalf("conversation-write suite has %d categories, want 10", len(categories))
	}
	for name, count := range categories {
		if count != 5 {
			t.Fatalf("category %s has %d cases, want 5", name, count)
		}
	}
}

func TestLifecycleSuiteHasFortyValidatedCases(t *testing.T) {
	suite, err := Load(filepath.Join("..", "..", "evals", "lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 40 {
		t.Fatalf("lifecycle suite has %d cases, want 40", len(suite.Cases))
	}
	categories := map[string]int{}
	for _, item := range suite.Cases {
		categories[item.Category]++
	}
	if len(categories) != 4 {
		t.Fatalf("lifecycle suite has %d categories, want 4", len(categories))
	}
	for name, count := range categories {
		if count != 10 {
			t.Fatalf("category %s has %d cases, want 10", name, count)
		}
	}
}

func TestConversationWriteSuiteHasNormalizedUserAssistantHistory(t *testing.T) {
	suite, err := Load(filepath.Join("..", "..", "evals", "conversation-write"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range suite.Cases {
		roles := map[string]bool{}
		for _, turn := range item.Turns {
			roles[turn.Role] = true
			if turn.Role != "user" && turn.Role != "assistant" {
				t.Fatalf("case %s history contains non-conversation role %q", item.ID, turn.Role)
			}
		}
		if !roles["user"] || !roles["assistant"] {
			t.Fatalf("case %s history roles = %#v; want user and assistant", item.ID, roles)
		}
	}
}

func TestConversationWriteSuiteIncludesRecallDistractors(t *testing.T) {
	suite, err := Load(filepath.Join("..", "..", "evals", "conversation-write"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range suite.Cases {
		if len(item.Memories) == 0 || len(item.ForbiddenRecall) == 0 {
			t.Fatalf("case %s is missing a seeded recall distractor", item.ID)
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

func TestSuiteValidationRejectsInvalidWriteAssertions(t *testing.T) {
	base := Case{
		ID: "write", Category: "write", Write: &Write{Event: "turn_end"},
		Turns:  []Turn{{Role: "user", Text: "remember this"}, {Role: "assistant", Text: "done"}},
		Recall: Recall{Mode: "active", Query: "remember this"}, ExpectedWrite: []string{"remember this"},
	}
	for _, test := range []struct {
		name   string
		mutate func(*Case)
	}{
		{name: "blank expected", mutate: func(c *Case) { c.ExpectedWrite = []string{" "} }},
		{name: "duplicate expected", mutate: func(c *Case) { c.ExpectedWrite = []string{"fact", "FACT"} }},
		{name: "blank forbidden", mutate: func(c *Case) { c.ForbiddenWrite = []string{""} }},
		{name: "expected also forbidden", mutate: func(c *Case) { c.ForbiddenWrite = []string{"remember this"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := base
			test.mutate(&item)
			suite := Suite{Version: SuiteVersion, Name: "invalid", Cases: []Case{item}}
			if err := suite.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
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

func TestRunnerWritesConversationThroughHookBeforeRecall(t *testing.T) {
	suite := Suite{Version: SuiteVersion, Name: "conversation-write", Cases: []Case{{
		ID:       "codex-turn-end",
		Category: "conversation_write",
		Turns: []Turn{
			{Role: "user", Text: "Keep the lunar cache decision."},
			{Role: "assistant", Text: "The lunar cache uses a seven minute TTL."},
		},
		Write: &Write{
			Target:    "codex",
			Event:     "turn_end",
			Assistant: "The lunar cache uses a seven minute TTL.",
			Messages: []Turn{
				{Role: "user", Text: "Keep the lunar cache decision."},
				{Role: "assistant", Text: "The lunar cache uses a seven minute TTL."},
				{Role: "tool_call", Text: `Read {"path":"docs/cache.md"}`},
				{Role: "tool_result", Text: "Cache policy confirms seven minutes."},
				{Role: "reasoning", Text: "private scratchpad"},
			},
			Workspace: "/eval/lunar-cache",
			Metadata:  map[string]string{"session_id": "session-1"},
		},
		Recall:           Recall{Mode: "active", Query: "lunar cache seven minute TTL", Limit: 3},
		ExpectedWrite:    []string{"lunar cache", "seven minute TTL", "Cache policy confirms"},
		ForbiddenWrite:   []string{"private scratchpad"},
		ExpectedMetadata: map[string]string{"hook_target": "codex", "hook_event": "turn_end", "workspace": "/eval/lunar-cache", "session_id": "session-1"},
	}}}

	result, err := (Runner{Root: t.TempDir()}).Run(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed != 1 || result.Failed != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	caseResult := result.Cases[0]
	if !caseResult.Written || caseResult.WriteRecall != 1 || caseResult.WritePrecision != 1 {
		t.Fatalf("unexpected write metrics: %#v", caseResult)
	}
	if caseResult.RecallAtK != 1 || caseResult.ReturnedContextBytes == 0 {
		t.Fatalf("unexpected recall metrics: %#v", caseResult)
	}
}

func TestAdapterContractCanPassWhenProviderRecallQualityFails(t *testing.T) {
	suite := Suite{Version: SuiteVersion, Name: "adapter-boundary", Cases: []Case{{
		ID: "write-faithful-recall-miss", Category: "adapter_contract",
		Turns:         []Turn{{Role: "user", Text: "Record the cobalt timeout."}, {Role: "assistant", Text: "The cobalt timeout is 17 seconds."}},
		Write:         &Write{Target: "codex", Event: "turn_end", Assistant: "The cobalt timeout is 17 seconds.", Workspace: "/eval/cobalt"},
		Recall:        Recall{Mode: "active", Query: "completely unrelated banana query", Limit: 1},
		ExpectedWrite: []string{"cobalt timeout", "17 seconds"},
	}}}
	result, err := (Runner{Root: t.TempDir()}).Run(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if result.AdapterContractPassed != 1 || result.AdapterContractFailed != 0 {
		t.Fatalf("adapter contract should pass: %#v", result)
	}
	if result.Failed != 1 {
		t.Fatalf("quality evaluation should still report the recall miss: %#v", result)
	}
}

func TestAggregateSeparatesExecutionFailureFromQualityMiss(t *testing.T) {
	result := Result{CaseCount: 2, Cases: []CaseResult{
		{ID: "quality-miss", Error: "expected recall was missing"},
		{ID: "runtime-error", Error: "provider unavailable", ExecutionError: "provider unavailable"},
	}}
	result.aggregate()
	if result.ExecutionFailed != 1 {
		t.Fatalf("execution failures = %d, want 1", result.ExecutionFailed)
	}
}

func TestRunnerCanWriteFromNormalizedHistory(t *testing.T) {
	suite := Suite{Version: SuiteVersion, Name: "history-write", Cases: []Case{{
		ID: "pi-history", Category: "history",
		Turns: []Turn{
			{Role: "user", Text: "Remember the aurora retention policy."},
			{Role: "assistant", Text: "The aurora retention policy keeps thirty days."},
		},
		Write: &Write{
			Target: "pi", Event: "turn_end", IncludeHistory: true,
			Messages: []Turn{{Role: "reasoning", Text: "private scratchpad"}},
		},
		Recall:         Recall{Mode: "active", Query: "aurora retention thirty days", Limit: 3},
		ExpectedWrite:  []string{"aurora retention policy", "thirty days"},
		ForbiddenWrite: []string{"private scratchpad"},
	}}}
	result, err := (Runner{Root: t.TempDir()}).Run(context.Background(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed != 1 {
		t.Fatalf("normalized history was not written and recalled: %#v", result.Cases[0])
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

func TestConversationWriteMetricsCountForbiddenCandidates(t *testing.T) {
	scenario := Case{
		ExpectedWrite:  []string{"durable decision", "seven minute TTL"},
		ForbiddenWrite: []string{"private scratchpad", "volatile session"},
	}
	caseResult := CaseResult{WriteCase: true, Written: true}
	caseResult.scoreConversationWrite(scenario, "durable decision with seven minute TTL and private scratchpad", nil, []memory.MemoryHit{{Text: "durable decision with seven minute TTL"}})
	result := Result{CaseCount: 1, Cases: []CaseResult{caseResult}}
	result.aggregate()

	if result.WriteFalsePositiveRate != 0.5 {
		t.Fatalf("write false-positive rate = %v, want 0.5", result.WriteFalsePositiveRate)
	}
	if result.WritePrecision != float64(2)/3 {
		t.Fatalf("write precision = %v, want %v", result.WritePrecision, float64(2)/3)
	}
}

func TestConversationWriteRequiresMetadataToSurviveRecall(t *testing.T) {
	scenario := Case{ExpectedWrite: []string{"durable decision"}, ExpectedMetadata: map[string]string{"workspace": "/eval/project"}}
	caseResult := CaseResult{WriteCase: true, Written: true}
	caseResult.scoreConversationWrite(
		scenario,
		"durable decision",
		map[string]string{"workspace": "/eval/project"},
		[]memory.MemoryHit{{Text: "durable decision"}},
	)
	if caseResult.Passed {
		t.Fatal("case passed even though recalled metadata was missing")
	}
	if len(caseResult.MetadataMismatches) != 1 {
		t.Fatalf("metadata mismatches = %#v, want recalled workspace mismatch", caseResult.MetadataMismatches)
	}
}
