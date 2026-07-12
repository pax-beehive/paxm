package crossagent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTracerSuiteHasThreeValidatedScenarios(t *testing.T) {
	scenarios, err := LoadScenarios(filepath.Join("..", "..", "evals", "cross-agent", "scenarios"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 3 {
		t.Fatalf("scenarios = %d, want 3", len(scenarios))
	}
	for _, scenario := range scenarios {
		if scenario.BuildSource == "" || scenario.TaskBinary == "" {
			t.Fatalf("scenario %s does not build an opaque task binary", scenario.ID)
		}
	}
}

func TestConsumerPromptSeparatesControlAndMemoryArms(t *testing.T) {
	base := "Complete ORBIT_DEPLOY_731 without triggering the audit trap."
	control := consumerPrompt(base, ArmControl, nil)
	if control != base {
		t.Fatalf("control prompt changed: %q", control)
	}
	assisted := consumerPrompt(base, ArmPassive, []string{"Use PAX_ENV=production before deploy.sh."})
	for _, expected := range []string{"paxm memory", "PAX_ENV=production", base} {
		if !strings.Contains(assisted, expected) {
			t.Fatalf("assisted prompt missing %q: %s", expected, assisted)
		}
	}
}

func TestAggregateReportsAvoidanceAndSuccessRates(t *testing.T) {
	report := Report{Trials: []TrialResult{
		{Arm: ArmControl, Success: true, Avoided: false, SafeSuccess: false},
		{Arm: ArmControl, Success: false, Avoided: true, SafeSuccess: false},
		{Arm: ArmPassive, Success: true, Avoided: true, SafeSuccess: true, RecallHits: 1},
		{Arm: ArmPassive, Success: true, Avoided: true, SafeSuccess: true, RecallHits: 1},
	}}
	report.Aggregate()
	if len(report.Summary) != 2 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	if report.Summary[0].Arm != ArmControl || report.Summary[0].AvoidanceRate != 0.5 || report.Summary[0].SuccessRate != 0.5 {
		t.Fatalf("control summary = %#v", report.Summary[0])
	}
	if report.Summary[1].Arm != ArmPassive || report.Summary[1].AvoidanceRate != 1 || report.Summary[1].SuccessRate != 1 || report.Summary[1].SafeSuccessRate != 1 || report.Summary[1].RecallRate != 1 {
		t.Fatalf("passive summary = %#v", report.Summary[1])
	}
}

func TestSandboxProfileDeniesLeakagePaths(t *testing.T) {
	profile := sandboxProfile([]string{"/repo", "/producer", "/paxm/memory.sqlite"}, []string{"/consumer", "/runtime"}, []string{"/consumer/deploy"}, []string{"/private/tmp/claude-501"})
	for _, expected := range []string{"(allow default)", "(deny file-read*", "(deny file-read-data", "(deny file-write*)", "(allow file-write*", `(subpath "/repo")`, `(subpath "/producer")`, `(subpath "/paxm/memory.sqlite")`, `(subpath "/consumer")`, `(subpath "/runtime")`, `(literal "/consumer/deploy")`, `(literal "/private/tmp/claude-501")`} {
		if !strings.Contains(profile, expected) {
			t.Fatalf("sandbox profile missing %q: %s", expected, profile)
		}
	}
}

func TestScenarioValidationRejectsEscapingPaths(t *testing.T) {
	scenario := Scenario{
		ID: "escape", Token: "token", ProducerPrompt: "producer", ConsumerPrompt: "consumer",
		SuccessMarker: ".success", TrapMarker: ".trap", TrapEvidence: "trap fired", OutcomePath: "../shared.txt", ExpectedOutcome: "done",
	}
	if err := scenario.validate(); err == nil {
		t.Fatal("validate accepted a path outside the scenario workspace")
	}
}

func TestTrapEncounteredSurvivesMarkerDeletion(t *testing.T) {
	scenario := Scenario{TrapMarker: ".trap", TrapEvidence: "benchmark failure:"}
	if !trapEncountered(t.TempDir(), "I saw benchmark failure: missing environment", scenario) {
		t.Fatal("output evidence did not record the encountered trap")
	}
}

func TestParseClaudeOAuthCredential(t *testing.T) {
	token, err := parseClaudeOAuthCredential([]byte(`{"claudeAiOauth":{"accessToken":"test-token","refreshToken":"do-not-use"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestReplaceEnvironmentUsesOverrides(t *testing.T) {
	env := replaceEnvironment([]string{"TMPDIR=/shared", "PATH=/bin"}, []string{"TMPDIR=/isolated"})
	if strings.Join(env, "\n") != "PATH=/bin\nTMPDIR=/isolated" {
		t.Fatalf("environment = %#v", env)
	}
}

func TestClaudeScratchPathIsWorkspaceScoped(t *testing.T) {
	first := claudeScratchPath("/private/tmp/consumer-one")
	second := claudeScratchPath("/private/tmp/consumer-two")
	if first == second || !strings.Contains(first, "-private-tmp-consumer-one") {
		t.Fatalf("scratch paths are not workspace-scoped: %q %q", first, second)
	}
}
