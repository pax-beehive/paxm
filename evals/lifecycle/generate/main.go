package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	paxeval "github.com/pax-beehive/paxm/internal/eval"
)

type topic struct{ name, decision, value string }

var topics = []topic{
	{"ember-cache", "ember cache TTL", "eleven minutes"}, {"harbor-backup", "harbor backup window", "02:40 UTC"},
	{"indigo-index", "indigo index shards", "nine shards"}, {"juniper-api", "juniper API version", "v4"},
	{"kepler-queue", "kepler queue limit", "640 messages"}, {"maple-deploy", "maple deploy region", "us-west-2"},
	{"nebula-log", "nebula log retention", "twenty-one days"}, {"opal-worker", "opal worker count", "six workers"},
	{"pearl-timeout", "pearl request timeout", "forty seconds"}, {"raven-rollout", "raven rollout batch", "twelve percent"},
}

func main() {
	suite := paxeval.Suite{Version: paxeval.SuiteVersion, Name: "memory-lifecycle-sqlite-40"}
	for i, value := range topics {
		n := i + 1
		suite.Cases = append(suite.Cases,
			writeRecall(n, value, "passive_after_restart", "passive", 1),
			writeRecall(n, value, "active_after_restart", "active", 1),
			writeRecall(n, value, "duplicate_consolidation", "active", 2),
			echoSuppression(n, value),
		)
	}
	if err := suite.Validate(); err != nil {
		panic(err)
	}
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		panic(err)
	}
	data = append(data, '\n')
	path := filepath.Join("evals", "lifecycle", "suite.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %d cases to %s\n", len(suite.Cases), path)
}

func writeRecall(n int, value topic, category, mode string, repeat int) paxeval.Case {
	sentence := fmt.Sprintf("Decision: the %s is %s.", value.decision, value.value)
	item := paxeval.Case{
		ID: fmt.Sprintf("%s-%02d", category, n), Category: category,
		Turns:         []paxeval.Turn{{Role: "user", Text: "Record the durable decision."}, {Role: "assistant", Text: sentence}},
		Write:         &paxeval.Write{Target: "codex", Event: "turn_end", Assistant: sentence, Workspace: "/eval/lifecycle/" + value.name, Metadata: map[string]string{"session_id": fmt.Sprintf("producer-%02d", n)}, Repeat: repeat},
		Recall:        paxeval.Recall{Mode: mode, Query: value.decision + " " + value.value, Limit: 3, Workspace: "/eval/lifecycle/" + value.name, Metadata: map[string]string{"session_id": fmt.Sprintf("consumer-%02d", n)}},
		ExpectedWrite: []string{value.decision, value.value}, RestartBeforeRecall: true,
	}
	if repeat > 1 {
		item.MaxMatchingHits = 1
	}
	return item
}

func echoSuppression(n int, value topic) paxeval.Case {
	recalled := "recalled-old-value-" + value.name
	decision := fmt.Sprintf("Decision: the %s is %s.", value.decision, value.value)
	return paxeval.Case{
		ID: fmt.Sprintf("recall_echo_after_restart-%02d", n), Category: "recall_echo_after_restart",
		Turns: []paxeval.Turn{{Role: "user", Text: "Use memory without writing it back."}, {Role: "assistant", Text: decision}},
		Write: &paxeval.Write{Target: "codex", Event: "turn_end", Messages: []paxeval.Turn{
			{Role: "tool_call", Text: `mcp__paxm__paxm_recall {"query":"project decision"}`},
			{Role: "tool_result", Text: recalled}, {Role: "assistant", Text: decision},
		}, Workspace: "/eval/lifecycle/" + value.name, Metadata: map[string]string{"session_id": fmt.Sprintf("echo-%02d", n)}},
		Recall:        paxeval.Recall{Mode: "passive", Query: value.decision + " " + value.value, Limit: 3, Workspace: "/eval/lifecycle/" + value.name},
		ExpectedWrite: []string{value.decision, value.value}, ForbiddenWrite: []string{recalled, "paxm_recall"}, RestartBeforeRecall: true,
	}
}
