package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	paxeval "github.com/pax-beehive/memory-adaptor/internal/eval"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type pattern struct {
	name  string
	mode  string
	tier  memory.MemoryTier
	limit int
}

func main() {
	topics := []struct{ token, fact string }{
		{"amber-orchid-17", "the release channel is stable"},
		{"blue-harbor-23", "the API uses cursor pagination"},
		{"cedar-raven-31", "the workspace prefers table driven tests"},
		{"delta-maple-42", "the deployment region is us west one"},
		{"ember-sparrow-56", "the default provider is sqlite"},
		{"frost-willow-64", "the hook must fail open"},
		{"golden-otter-72", "the config format is yaml"},
		{"hazel-comet-81", "the binary name is paxm"},
		{"indigo-fox-93", "the recall path reuses the facade"},
		{"juniper-moon-08", "the desktop client will be native macos"},
	}
	patterns := []pattern{
		{"exact_phrase_ltm", "active", memory.TierLTM, 3},
		{"exact_phrase_stm", "active", memory.TierSTM, 3},
		{"partial_terms", "active", memory.TierLTM, 3},
		{"active_limit", "active", memory.TierLTM, 1},
		{"passive", "passive", memory.TierLTM, 2},
		{"passive_initial", "passive_initial", memory.TierLTM, 5},
		{"expired_suppression", "active", memory.TierLTM, 3},
		{"ranking", "active", memory.TierLTM, 3},
		{"mixed_tiers", "active", memory.TierSTM, 3},
		{"irrelevant_suppression", "active", memory.TierLTM, 3},
	}
	suite := paxeval.Suite{Version: paxeval.SuiteVersion, Name: "sqlite-baseline-100"}
	for _, p := range patterns {
		for i, topic := range topics {
			prefix := fmt.Sprintf("%s-%02d", p.name, i+1)
			targetID, forbiddenID := prefix+"-target", prefix+"-forbidden"
			query := topic.token + " " + topic.fact
			memoryText := query
			if p.name == "partial_terms" {
				query = topic.token + " durable"
				memoryText = topic.token + " " + topic.fact
			}
			memories := []paxeval.Memory{
				{ID: targetID, Text: "Project memory: " + memoryText + ".", Tier: p.tier, Metadata: map[string]string{"workspace": "/eval/project-" + topic.token}},
				{ID: forbiddenID, Text: "Unrelated cooking note about sourdough hydration and oven temperature.", Tier: memory.TierLTM},
			}
			if p.name == "ranking" {
				memories = append(memories, paxeval.Memory{ID: prefix + "-partial", Text: "A partial project note mentions " + topic.token + " without the durable decision.", Tier: memory.TierLTM})
			}
			if p.name == "expired_suppression" {
				expired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
				forbiddenID = prefix + "-expired"
				memories = append(memories, paxeval.Memory{ID: forbiddenID, Text: query, Tier: memory.TierSTM, ExpiresAt: &expired})
			}
			expected := []string{targetID}
			if p.name == "mixed_tiers" {
				secondID := prefix + "-ltm"
				memories = append(memories, paxeval.Memory{ID: secondID, Text: "Archived decision: " + query + ".", Tier: memory.TierLTM})
				expected = append(expected, secondID)
			}
			if p.name == "active_limit" {
				memories = append(memories, paxeval.Memory{ID: prefix + "-also-relevant", Text: "A weaker note mentions " + topic.token + ".", Tier: memory.TierLTM})
			}
			expectedFirst := ""
			if p.name == "ranking" {
				expectedFirst = targetID
			}
			maxHits := 0
			if p.name == "active_limit" {
				maxHits = 1
			}
			suite.Cases = append(suite.Cases, paxeval.Case{
				ID: prefix, Category: p.name,
				Turns:    []paxeval.Turn{{Role: "user", Text: "Please remember " + query}, {Role: "assistant", Text: "I will use that project decision later."}},
				Memories: memories,
				Recall:   paxeval.Recall{Mode: p.mode, Query: query, Limit: p.limit, Workspace: "/eval/project-" + topic.token, Metadata: map[string]string{"workspace": "/eval/project-" + topic.token}},
				Expected: expected, Forbidden: []string{forbiddenID}, ExpectedFirst: expectedFirst, MaxHits: maxHits,
			})
		}
	}
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		panic(err)
	}
	path := filepath.Join("evals", "baseline", "suite.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		panic(err)
	}
}
