package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type fakeAgentExecutor struct{ requests []AgentRequest }

func (f *fakeAgentExecutor) Execute(_ context.Context, request AgentRequest) (AgentResponse, error) {
	f.requests = append(f.requests, request)
	answers := map[AgentArm]string{AgentArmControl: "a cat", AgentArmPassive: "a dog", AgentArmActive: "dog"}
	return AgentResponse{Text: answers[request.Arm], SessionID: "session-" + string(request.Arm), InputTokens: 100, OutputTokens: 2}, nil
}

func TestLoCoMoAgentRunnerMeasuresMemoryLiftAcrossArms(t *testing.T) {
	dataset := LoCoMoDataset{Conversations: []LoCoMoConversation{{
		ID:        "sample-1",
		Sessions:  []LoCoMoSession{{Number: 1, Turns: []LoCoMoTurn{{Speaker: "Alice", ID: "D1:1", Text: "I adopted a dog."}}}},
		Questions: []LoCoMoQuestion{{Question: "What did Alice adopt?", Answer: json.RawMessage(`"a dog"`), Evidence: []string{"D1:1"}, Category: 1}},
	}}}
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["primary"] = config.ProviderConfig{Type: "mem0", Enabled: true, RunID: "normal"}
	provider := &locomoTestProvider{}
	agent := &fakeAgentExecutor{}
	runner := LoCoMoAgentRunner{
		BuildProvider: func(string, config.ProviderConfig) (memory.Provider, error) { return provider, nil },
		Agent:         agent,
	}
	result, err := runner.Run(context.Background(), dataset, LoCoMoAgentOptions{
		Config: cfg, Provider: "primary", RunID: "agent-test", ManifestDir: dir,
		AgentName: "opencode", Arms: []AgentArm{AgentArmControl, AgentArmPassive, AgentArmActive}, MatchThreshold: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trials) != 3 || len(agent.requests) != 3 {
		t.Fatalf("trials=%d requests=%d", len(result.Trials), len(agent.requests))
	}
	if result.Summaries[0].Arm != AgentArmControl || result.Summaries[0].Matched != 0 {
		t.Fatalf("control summary = %#v", result.Summaries[0])
	}
	if result.Summaries[1].Arm != AgentArmPassive || result.Summaries[1].Matched != 1 || result.Summaries[1].MeanF1 != 1 {
		t.Fatalf("passive summary = %#v", result.Summaries[1])
	}
	if result.Summaries[2].Arm != AgentArmActive || result.Summaries[2].Matched != 1 {
		t.Fatalf("active summary = %#v", result.Summaries[2])
	}
	if result.PassiveLift != 1 || result.ActiveLift != 1 {
		t.Fatalf("lift passive=%v active=%v", result.PassiveLift, result.ActiveLift)
	}
	if len(provider.deleted) != 1 {
		t.Fatalf("cleanup refs = %#v", provider.deleted)
	}
	loaded, err := config.Load(agent.requests[0].PaxmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for event, hook := range loaded.Agents["opencode"].Hooks {
		if hook.Write.Enabled != (event == "turn_end") {
			t.Fatalf("eval config left %s passive write enabled", event)
		}
	}
}
