package facade

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sqliteadapter "github.com/pax-beehive/paxm/internal/adapters/sqlite"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type captureProvider struct {
	query string
	limit int
	meta  map[string]string
	tiers []memory.MemoryTier
	hits  []memory.MemoryHit
	items []memory.MemoryItem
	delay time.Duration
}

func (p *captureProvider) Name() string {
	return "capture"
}

func (p *captureProvider) Search(_ context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.query = query.Text
	p.limit = query.Limit
	p.meta = query.Metadata
	p.tiers = append([]memory.MemoryTier(nil), query.Tiers...)
	if p.hits != nil {
		return p.hits, nil
	}
	return []memory.MemoryHit{{ID: "1", Text: "hit", Score: 1}}, nil
}

func TestRunHookOverallTimeoutReturnsWithoutFailing(t *testing.T) {
	provider := &captureProvider{delay: 250 * time.Millisecond}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"passive": {Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Timeout: "1s"}}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Enabled: true, Hooks: map[string]config.AgentHookConfig{
				"user_input": {Recall: config.HookRecallConfig{Enabled: true, Profile: "passive", Timeout: "20ms"}},
			}},
		},
	}, router)

	started := time.Now()
	result, err := service.RunHook(context.Background(), HookEvent{Target: "codex", Event: "user_input", Query: "memory"})
	if err != nil {
		t.Fatalf("passive recall timeout affected hook: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("overall passive recall timeout returned after %s", elapsed)
	}
	if result.Recall == nil || len(result.Recall.ProviderErrors) != 1 {
		t.Fatalf("timeout diagnostics missing: %#v", result.Recall)
	}
	if !result.Recall.TimedOut {
		t.Fatalf("overall timeout marker missing: %#v", result.Recall)
	}
}

func TestRecallAdapterContractForwardsRequestAndPreservesProviderHit(t *testing.T) {
	raw := 0.82
	created := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	provider := &captureProvider{hits: []memory.MemoryHit{{
		ID: "provider-id", Text: "provider text", Relevance: 0.9, Score: 0.9, RawScore: &raw, RawScoreKind: "native",
		Source: "provider-source", Metadata: map[string]string{"workspace": "alpha"},
		CreatedAt: created, Tier: memory.TierLTM,
	}}}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{Version: 1, RecallProfiles: map[string]config.RecallProfileConfig{
		"contract": {Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}}},
	}}, router)
	result, err := service.Recall(context.Background(), RecallInput{Query: "exact request", Profile: "contract", Limit: 7, Meta: map[string]string{"session": "consumer"}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query != "exact request" || provider.meta["session"] != "consumer" {
		t.Fatalf("recall request was not forwarded faithfully: %#v", provider)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("hits = %#v", result.Hits)
	}
	hit := result.Hits[0]
	if hit.ID != "provider-id" || hit.Text != "provider text" || hit.RawScore == nil || *hit.RawScore != raw || hit.RawScoreKind != "native" || hit.Source != "provider-source" || hit.Metadata["workspace"] != "alpha" || hit.Tier != memory.TierLTM || !hit.CreatedAt.Equal(created) {
		t.Fatalf("provider hit was not preserved: %#v", hit)
	}
}

func (p *captureProvider) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.items = append(p.items, item)
	return memory.MemoryRef{Provider: "capture", ID: "1"}, nil
}

func (p *captureProvider) Health(context.Context) error {
	return nil
}

func TestIngestBatchToProviderPreservesHistoricalIdentityAndTime(t *testing.T) {
	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{Version: 1}, router)
	createdAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

	_, err = service.IngestBatchToProvider(context.Background(), "capture", IngestBatchInput{Items: []IngestInput{{
		ID:        "historical-turn",
		Text:      "User: hello\nAssistant: hi",
		Source:    "backfill:codex",
		CreatedAt: createdAt,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 1 || provider.items[0].ID != "historical-turn" || !provider.items[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("historical item was not preserved: %#v", provider.items)
	}
	if provider.items[0].Tier != memory.TierLTM {
		t.Fatalf("historical direct provider write should default to LTM: %#v", provider.items[0])
	}
}

func TestIngestConsolidatesEquivalentLongTermMemories(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	firstSeen := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(2 * time.Hour)

	first, err := service.Ingest(context.Background(), IngestInput{
		Text:      "Decision: release workflow requires published binary verification.",
		Profile:   "ltm",
		Source:    "hook:codex",
		Metadata:  map[string]string{"workspace": "/tmp/project"},
		CreatedAt: firstSeen,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ingest(context.Background(), IngestInput{
		Text:      "  DECISION:  release workflow requires published binary verification. ",
		Profile:   "ltm",
		Source:    "mcp",
		Metadata:  map[string]string{"workspace": "/tmp/project"},
		CreatedAt: lastSeen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Refs) != 1 || len(second.Refs) != 1 || first.Refs[0].ID == "" || first.Refs[0].ID != second.Refs[0].ID {
		t.Fatalf("equivalent LTM refs were not consolidated: first=%#v second=%#v", first.Refs, second.Refs)
	}

	recalled, err := service.Recall(context.Background(), RecallInput{Query: "release workflow published binary", Profile: "default", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled.Hits) != 1 {
		t.Fatalf("consolidated recall returned %d hits: %#v", len(recalled.Hits), recalled.Hits)
	}
	hit := recalled.Hits[0]
	if hit.Metadata[memory.MetadataOccurrences] != "2" {
		t.Fatalf("occurrences = %q, want 2: %#v", hit.Metadata[memory.MetadataOccurrences], hit.Metadata)
	}
	if hit.Metadata[memory.MetadataFirstSeenAt] != firstSeen.Format(time.RFC3339Nano) {
		t.Fatalf("first_seen_at = %q, want %q", hit.Metadata[memory.MetadataFirstSeenAt], firstSeen.Format(time.RFC3339Nano))
	}
	if hit.Metadata[memory.MetadataLastSeenAt] != lastSeen.Format(time.RFC3339Nano) {
		t.Fatalf("last_seen_at = %q, want %q", hit.Metadata[memory.MetadataLastSeenAt], lastSeen.Format(time.RFC3339Nano))
	}
	if hit.Metadata[memory.MetadataFingerprint] == "" {
		t.Fatalf("consolidated memory has no fingerprint: %#v", hit.Metadata)
	}
}

func TestIngestScopesLongTermConsolidationByWorkspace(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	first, err := service.Ingest(context.Background(), IngestInput{
		Text:      "Decision: use the repository release workflow.",
		Profile:   "ltm",
		Metadata:  map[string]string{"workspace": "/tmp/project-a"},
		CreatedAt: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ingest(context.Background(), IngestInput{
		Text:      "Decision: use the repository release workflow.",
		Profile:   "ltm",
		Metadata:  map[string]string{"workspace": "/tmp/project-b"},
		CreatedAt: time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Refs) != 1 || len(second.Refs) != 1 || first.Refs[0].ID == second.Refs[0].ID {
		t.Fatalf("workspace-scoped LTM refs collided: first=%#v second=%#v", first.Refs, second.Refs)
	}
}

func TestIngestConsolidationHandlesOutOfOrderEvidenceAndMergesMetadata(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	earlier := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	later := earlier.Add(24 * time.Hour)
	for _, input := range []IngestInput{
		{
			Text:      "Decision: preserve lifecycle chronology.",
			Profile:   "ltm",
			Metadata:  map[string]string{"workspace": "/tmp/project", "later_evidence": "present"},
			CreatedAt: later,
		},
		{
			Text:      "Decision: preserve lifecycle chronology.",
			Profile:   "ltm",
			Metadata:  map[string]string{"workspace": "/tmp/project", "earlier_evidence": "present"},
			CreatedAt: earlier,
		},
	} {
		if _, err := service.Ingest(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	recalled, err := service.Recall(context.Background(), RecallInput{Query: "lifecycle chronology", Profile: "default", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled.Hits) != 1 {
		t.Fatalf("consolidated recall returned %d hits: %#v", len(recalled.Hits), recalled.Hits)
	}
	metadata := recalled.Hits[0].Metadata
	if metadata[memory.MetadataFirstSeenAt] != earlier.Format(time.RFC3339Nano) || metadata[memory.MetadataLastSeenAt] != later.Format(time.RFC3339Nano) {
		t.Fatalf("out-of-order lifecycle timestamps were not consolidated: %#v", metadata)
	}
	if metadata["earlier_evidence"] != "present" || metadata["later_evidence"] != "present" {
		t.Fatalf("source metadata was not merged: %#v", metadata)
	}
}

func TestIngestDoesNotConsolidateShortTermMemories(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	input := IngestInput{
		Text:     "Working note: release verification is in progress.",
		Profile:  "stm",
		Metadata: map[string]string{"workspace": "/tmp/project"},
	}
	first, err := service.Ingest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ingest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Refs) != 1 || len(second.Refs) != 1 || first.Refs[0].ID == second.Refs[0].ID {
		t.Fatalf("STM writes were unexpectedly consolidated: first=%#v second=%#v", first.Refs, second.Refs)
	}
}

func TestIngestBatchConsolidatesEquivalentLongTermMemories(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	firstSeen := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(time.Hour)
	result, err := service.IngestBatch(context.Background(), IngestBatchInput{Items: []IngestInput{
		{
			Text:      "Decision: hooks write durable facts through the LTM profile.",
			Profile:   "ltm",
			Metadata:  map[string]string{"workspace": "/tmp/project"},
			CreatedAt: firstSeen,
		},
		{
			Text:      " decision: hooks write durable facts through the ltm profile. ",
			Profile:   "ltm",
			Metadata:  map[string]string{"workspace": "/tmp/project"},
			CreatedAt: lastSeen,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refs) != 2 || result.Refs[0].ID != result.Refs[1].ID {
		t.Fatalf("batch LTM refs were not consolidated: %#v", result.Refs)
	}
	recalled, err := service.Recall(context.Background(), RecallInput{Query: "hooks durable facts LTM", Profile: "default", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled.Hits) != 1 || recalled.Hits[0].Metadata[memory.MetadataOccurrences] != "2" {
		t.Fatalf("batch consolidation result is invalid: %#v", recalled.Hits)
	}
}

func TestUserInputHookConsolidationIgnoresVolatileRawEventFields(t *testing.T) {
	t.Parallel()

	service := newSQLiteService(t)
	firstItem, ok, err := service.HookWriteItem(HookEvent{
		Target:    "codex",
		Event:     "user_input",
		Prompt:    "Remember that releases require binary verification.",
		Workspace: "/tmp/project",
		Raw:       json.RawMessage(`{"session_id":"session-a","prompt":"Remember that releases require binary verification."}`),
	})
	if err != nil || !ok {
		t.Fatalf("first HookWriteItem() = %#v, %v, %v", firstItem, ok, err)
	}
	secondItem, ok, err := service.HookWriteItem(HookEvent{
		Target:    "codex",
		Event:     "user_input",
		Prompt:    "Remember that releases require binary verification.",
		Workspace: "/tmp/project",
		Raw:       json.RawMessage(`{"session_id":"session-b","prompt":"Remember that releases require binary verification."}`),
	})
	if err != nil || !ok {
		t.Fatalf("second HookWriteItem() = %#v, %v, %v", secondItem, ok, err)
	}
	if strings.Contains(firstItem.Text, "session-a") || strings.Contains(firstItem.Text, "raw_json") {
		t.Fatalf("default hook write leaked raw event content: %q", firstItem.Text)
	}
	if !strings.Contains(firstItem.Text, "Codex user input:") || !strings.Contains(firstItem.Text, "Remember that releases require binary verification.") {
		t.Fatalf("default hook write did not preserve safe prompt text: %q", firstItem.Text)
	}
	first, err := service.Ingest(context.Background(), firstItem)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Ingest(context.Background(), secondItem)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Refs) != 1 || len(second.Refs) != 1 || first.Refs[0].ID != second.Refs[0].ID {
		t.Fatalf("volatile raw fields prevented hook consolidation: first=%#v second=%#v", first.Refs, second.Refs)
	}
}

func TestDefaultHookWriteTextFiltersRuntimePayload(t *testing.T) {
	t.Parallel()

	service := newSQLiteServiceForAgent(t, "claude")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target:    "claude",
		Event:     "turn_end",
		Assistant: "The release must be verified with the downloaded binary.",
		Workspace: "/tmp/project",
		Raw:       json.RawMessage(`{"last_assistant_message":"The release must be verified with the downloaded binary.","tool_use":{"name":"Read","input":{"file":"/secret/path"}},"thinking":"private chain of thought","session_id":"volatile"}`),
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if !strings.Contains(item.Text, "Claude Code assistant response:") || !strings.Contains(item.Text, "downloaded binary") {
		t.Fatalf("safe hook text lost assistant content: %q", item.Text)
	}
	for _, forbidden := range []string{"tool_use", "/secret/path", "private chain", "volatile"} {
		if strings.Contains(item.Text, forbidden) {
			t.Fatalf("safe hook text leaked %q: %q", forbidden, item.Text)
		}
	}
}

func TestDefaultPiTurnEndWriteUsesFilteredMessages(t *testing.T) {
	t.Parallel()

	service := newSQLiteServiceForAgent(t, "pi")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "pi",
		Event:  "turn_end",
		Messages: []HookMessage{
			{Role: "user", Text: "Remember the setup boundary."},
			{Role: "assistant", Text: "Setup owns hook installation."},
			{Role: "toolCall", Text: `Read {"path":"docs/config.md"}`},
			{Role: "toolResult", Text: "configuration documentation"},
			{Role: "thinking", Text: "private chain of thought"},
		},
		Raw: json.RawMessage(`{"messages":[{"role":"user","text":"Remember the setup boundary."},{"role":"assistant","text":"Setup owns hook installation."},{"role":"tool","text":"large tool output"}],"raw_event":{"debug":true}}`),
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	for _, expected := range []string{"User: Remember the setup boundary.", "Assistant: Setup owns hook installation.", `Tool call: Read {"path":"docs/config.md"}`, "Tool result: configuration documentation"} {
		if !strings.Contains(item.Text, expected) {
			t.Fatalf("safe hook text lost %q: %q", expected, item.Text)
		}
	}
	if strings.Contains(item.Text, "private chain of thought") || strings.Contains(item.Text, "raw_event") {
		t.Fatalf("safe hook text leaked thinking or raw payload: %q", item.Text)
	}
	if !strings.Contains(item.Text, "User: Remember the setup boundary.") || !strings.Contains(item.Text, "Assistant: Setup owns hook installation.") {
		t.Fatalf("safe hook text lost visible messages: %q", item.Text)
	}
}

func TestTurnEndCombinesAssistantAndToolMessagesWithoutDuplication(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "claude")
	item, ok, err := service.HookWriteItem(HookEvent{Target: "claude", Event: "turn_end", Assistant: "Done.", Messages: []HookMessage{{Role: "assistant", Text: "Done."}, {Role: "tool_use", Text: "Read README.md"}, {Role: "tool_result", Text: "README contents"}, {Role: "reasoning", Text: "secret"}}})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if strings.Count(item.Text, "Done.") != 1 || !strings.Contains(item.Text, "Tool call: Read README.md") || !strings.Contains(item.Text, "Tool result: README contents") {
		t.Fatalf("unexpected write text: %q", item.Text)
	}
	if strings.Contains(item.Text, "secret") {
		t.Fatalf("reasoning leaked: %q", item.Text)
	}
}

func TestHookWriteItemStripsRecalledContextButKeepsNewConclusion(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "claude")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "claude",
		Event:  "turn_end",
		Assistant: strings.Join([]string{
			"<paxm-recall version=\"1\" mode=\"passive\">",
			"The old deployment requires PAX_ENV=production.",
			"</paxm-recall>",
			"New conclusion: verify the release artifact checksum too.",
		}, "\n"),
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if strings.Contains(item.Text, "old deployment") {
		t.Fatalf("recalled context was written back: %q", item.Text)
	}
	if !strings.Contains(item.Text, "New conclusion: verify the release artifact checksum too.") {
		t.Fatalf("new conclusion was removed: %q", item.Text)
	}
}

func TestHookWriteItemDropsPaxmRecallToolExchange(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "codex")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "codex",
		Event:  "turn_end",
		Messages: []HookMessage{
			{Role: "tool_call", Text: `mcp__paxm__paxm_recall {"query":"deployment"}`},
			{Role: "tool_result", Text: `{"hits":[{"text":"The old deployment requires PAX_ENV=production."}]}`},
			{Role: "assistant", Text: "New conclusion: verify the release artifact checksum too."},
		},
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	for _, forbidden := range []string{"paxm_recall", "old deployment"} {
		if strings.Contains(item.Text, forbidden) {
			t.Fatalf("paxm recall tool exchange was written back: %q", item.Text)
		}
	}
	if !strings.Contains(item.Text, "New conclusion: verify the release artifact checksum too.") {
		t.Fatalf("new conclusion was removed: %q", item.Text)
	}
}

func TestHookWriteItemDropsConfiguredPaxmRecallCommand(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "pi")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "pi",
		Event:  "turn_end",
		Messages: []HookMessage{
			{Role: "tool_call", Text: `Bash {"command":"/usr/local/bin/paxm --config /tmp/paxm.yaml recall --query deployment"}`},
			{Role: "tool_result", Text: "The old deployment requires PAX_ENV=production."},
			{Role: "assistant", Text: "New conclusion: verify the release artifact checksum too."},
		},
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if strings.Contains(item.Text, "old deployment") {
		t.Fatalf("configured paxm recall result was written back: %q", item.Text)
	}
	if !strings.Contains(item.Text, "New conclusion: verify the release artifact checksum too.") {
		t.Fatalf("new conclusion was removed: %q", item.Text)
	}
}

func TestHookWriteItemKeepsCommandsThatOnlyMentionPaxmRecall(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "codex")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "codex",
		Event:  "turn_end",
		Messages: []HookMessage{
			{Role: "tool_call", Text: `Bash {"command":"rg paxm_recall internal"}`},
			{Role: "tool_result", Text: "internal/mcp/server.go: case paxm_recall"},
			{Role: "tool_call", Text: `Bash {"command":"paxm remember --text recall"}`},
			{Role: "tool_result", Text: "stored memory: sqlite/example"},
		},
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	for _, expected := range []string{"rg paxm_recall internal", "internal/mcp/server.go", "paxm remember", "stored memory"} {
		if !strings.Contains(item.Text, expected) {
			t.Fatalf("ordinary tool evidence was removed: %q", item.Text)
		}
	}
}

func TestHookWriteItemDropsProvenanceMarkedRecallResultWhenToolsInterleave(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "codex")
	item, ok, err := service.HookWriteItem(HookEvent{
		Target: "codex",
		Event:  "turn_end",
		Messages: []HookMessage{
			{Role: "tool_call", Text: `mcp__paxm__paxm_recall {"query":"deployment"}`},
			{Role: "tool_call", Text: `Read {"path":"README.md"}`},
			{Role: "tool_result", Text: "README evidence must remain."},
			{Role: "tool_result", Text: `{"query":"deployment","hits":[{"text":"old recalled deployment"}],"paxm_context":{"version":1,"kind":"recall","mode":"active"}}`},
			{Role: "assistant", Text: "New conclusion: verify the release artifact checksum too."},
		},
	})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if strings.Contains(item.Text, "old recalled deployment") {
		t.Fatalf("interleaved recall result was written back: %q", item.Text)
	}
	for _, expected := range []string{"Read", "README evidence must remain.", "New conclusion"} {
		if !strings.Contains(item.Text, expected) {
			t.Fatalf("unrelated evidence was removed: %q", item.Text)
		}
	}
}

func TestRecallEchoDoesNotAccumulateAcrossFiveWriteCycles(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "claude")
	workspace := "/eval/recall-echo-cycle"
	original := "Recall echo sentinel: production deploys require PAX_ENV=production."
	if _, err := service.Ingest(context.Background(), IngestInput{Text: original, Profile: "ltm", Metadata: map[string]string{"workspace": workspace}}); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 5; cycle++ {
		item, ok, err := service.HookWriteItem(HookEvent{
			Target:    "claude",
			Event:     "turn_end",
			Workspace: workspace,
			Assistant: WrapRecallContext("passive", original) + "\nNew conclusion: verify the artifact checksum.",
		})
		if err != nil || !ok {
			t.Fatalf("cycle %d HookWriteItem() = %#v, %v, %v", cycle, item, ok, err)
		}
		if strings.Contains(item.Text, original) {
			t.Fatalf("cycle %d retained recalled content: %q", cycle, item.Text)
		}
		if _, err := service.Ingest(context.Background(), item); err != nil {
			t.Fatalf("cycle %d ingest: %v", cycle, err)
		}
	}
	recalled, err := service.Recall(context.Background(), RecallInput{Query: "Recall echo sentinel PAX_ENV production", Profile: "default", Limit: 10, Meta: map[string]string{"workspace": workspace}})
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, hit := range recalled.Hits {
		if strings.Contains(hit.Text, "Recall echo sentinel") {
			matches++
			if hit.Text != original {
				t.Fatalf("echo-derived memory survived: %q", hit.Text)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("original recall matches = %d, want 1: %#v", matches, recalled.Hits)
	}
}

func TestToolUseHookWritesNormalizedToolActivity(t *testing.T) {
	t.Parallel()
	service := newSQLiteServiceForAgent(t, "claude")
	item, ok, err := service.HookWriteItem(HookEvent{Target: "claude", Event: "tool_use", Messages: []HookMessage{{Role: "tool_call", Text: `Bash {"command":"go test ./..."}`}, {Role: "tool_result", Text: `{"exit_code":0}`}, {Role: "analysis", Text: "hidden"}}})
	if err != nil || !ok {
		t.Fatalf("HookWriteItem() = %#v, %v, %v", item, ok, err)
	}
	if !strings.Contains(item.Text, "Claude Code tool activity:") || !strings.Contains(item.Text, "Tool call: Bash") || !strings.Contains(item.Text, "Tool result:") {
		t.Fatalf("tool content missing: %q", item.Text)
	}
	if strings.Contains(item.Text, "hidden") {
		t.Fatalf("analysis leaked: %q", item.Text)
	}
}

func newSQLiteService(t *testing.T) *Service {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	return newSQLiteServiceWithConfig(t, cfg)
}

func newSQLiteServiceForAgent(t *testing.T, agentName string) *Service {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	agent := cfg.Agents[agentName]
	agent.Enabled = true
	cfg.Agents[agentName] = agent
	return newSQLiteServiceWithConfig(t, cfg)
}

func newSQLiteServiceWithConfig(t *testing.T, cfg config.Config) *Service {
	t.Helper()
	provider, err := sqliteadapter.New("sqlite", filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, router)
}

func TestRunHookUsesExplicitQueryBeforeTemplate(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers:  []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				MaxResults: 8,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
				},
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Recall: config.HookRecallConfig{
							Enabled:       true,
							Profile:       "default",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    8,
						},
					},
				},
			},
		},
	}, router)

	_, err = service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Query:  "explicit query",
		Prompt: "prompt query",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query != "explicit query" {
		t.Fatalf("expected explicit query, got %q", provider.query)
	}
}

func TestRecallUsesAgentActiveRecallProfile(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"active": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "active",
				},
			},
		},
	}, router)

	_, err = service.Recall(context.Background(), RecallInput{Query: "active query"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.query != "active query" {
		t.Fatalf("active recall did not hit provider, got query %q", provider.query)
	}
}

func TestRecallProfilePassesMemoryTiers(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Tiers:     []string{"stm", "ltm"},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled:      true,
				ActiveRecall: config.ActiveRecallConfig{Enabled: true, Profile: "default"},
			},
		},
	}, router)

	_, err = service.Recall(context.Background(), RecallInput{Query: "tiered"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(provider.tiers, []memory.MemoryTier{memory.TierSTM, memory.TierLTM}) {
		t.Fatalf("recall tiers were not passed to provider: %#v", provider.tiers)
	}
}

func TestIngestWriteProfileAppliesMemoryTierAndTTL(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		WriteProfiles: map[string]config.WriteProfileConfig{
			"stm": {
				Providers:    []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Tier:         "stm",
				ExpiresAfter: "24h",
			},
		},
	}, router)
	createdAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	_, err = service.Ingest(context.Background(), IngestInput{
		Text:      "working state",
		Profile:   "stm",
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 1 || provider.items[0].Tier != memory.TierSTM {
		t.Fatalf("write profile tier not applied: %#v", provider.items)
	}
	if provider.items[0].ExpiresAt == nil || !provider.items[0].ExpiresAt.Equal(createdAt.Add(24*time.Hour)) {
		t.Fatalf("write profile expiry not applied: %#v", provider.items[0].ExpiresAt)
	}
}

func TestRecallUsesProviderRouteThresholdOverride(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "provider-override", Text: "provider-specific threshold", Relevance: 0.4, Score: 0.4},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{
					{
						Name:     "capture",
						Required: true,
						Weight:   1,
						Thresholds: &config.RecallThresholdConfig{
							MinRelevance: 0.3,
							MinScore:     0.3,
						},
					},
				},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.8,
					MinScore:     0.8,
				},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				ActiveRecall: config.ActiveRecallConfig{
					Enabled: true,
					Profile: "default",
				},
			},
		},
	}, router)

	result, err := service.Recall(context.Background(), RecallInput{Query: "threshold"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].ID != "provider-override" {
		t.Fatalf("provider threshold override was not applied: %#v", result.Hits)
	}
}

func TestRunHookAppliesInsertionPolicy(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "low-score", Text: "paxm passive recall low score", Score: 0.7, Relevance: 0.7},
			{ID: "no-term", Text: "unrelated high score memory", Score: 0.95, Relevance: 0.95},
			{ID: "keep", Text: "paxm passive recall should be conservative", Score: 0.9, Relevance: 0.9},
			{ID: "over-limit", Text: "another paxm passive recall memory", Score: 0.85, Relevance: 0.85},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"passive": {
				Providers:  []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				MaxResults: 8,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Recall: config.HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    8,
							Insertion: config.HookInsertionConfig{
								MinScore:          0.8,
								MaxItems:          1,
								RequireQueryTerms: true,
							},
						},
					},
				},
			},
		},
	}, router)

	result, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "paxm passive recall",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recall == nil {
		t.Fatal("expected recall result")
	}
	if len(result.Recall.Hits) != 1 || result.Recall.Hits[0].ID != "keep" {
		t.Fatalf("unexpected inserted hits: %#v", result.Recall.Hits)
	}
}

func TestRunHookUsesInitialRecallOverride(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{
		hits: []memory.MemoryHit{
			{ID: "warmup", Text: "project bootstrap memory", Score: 0.4, Relevance: 0.4},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		RecallProfiles: map[string]config.RecallProfileConfig{
			"passive": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.8,
					MinScore:     0.8,
				},
			},
			"passive_initial": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
				Thresholds: config.RecallThresholdConfig{
					MinRelevance: 0.3,
					MinScore:     0.3,
				},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Recall: config.HookRecallConfig{
							Enabled:       true,
							Profile:       "passive",
							QueryTemplate: "{{ .prompt }}",
							MaxResults:    2,
							Insertion: config.HookInsertionConfig{
								MinScore: 0.8,
							},
							Initial: &config.HookInitialRecall{
								Enabled:    true,
								Profile:    "passive_initial",
								MaxResults: 5,
								Insertion: config.HookInsertionConfig{
									MinScore: 0.3,
									MaxItems: 5,
								},
							},
						},
					},
				},
			},
		},
	}, router)

	strict, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "project bootstrap",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strict.Recall == nil || len(strict.Recall.Hits) != 0 {
		t.Fatalf("strict user_input should filter low-confidence hits: %#v", strict.Recall)
	}

	initial, err := service.RunHook(context.Background(), HookEvent{
		Target: "codex",
		Event:  "user_input",
		Prompt: "project bootstrap",
		Metadata: map[string]string{
			HookRecallPhaseMetadataKey: HookRecallPhaseInitial,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Recall == nil || len(initial.Recall.Hits) != 1 || initial.Recall.Hits[0].ID != "warmup" {
		t.Fatalf("initial user_input should use loose policy: %#v", initial.Recall)
	}
}

func TestHookWriteItemRendersTemplateAndMetadata(t *testing.T) {
	t.Parallel()

	provider := &captureProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(config.Config{
		Version: 1,
		WriteProfiles: map[string]config.WriteProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{
							Enabled:  true,
							Profile:  "default",
							Template: "User input: {{ .prompt }} / {{ .raw_json }}",
							Mode:     "user_input",
							Buffer: config.HookBufferConfig{
								Enabled: true,
							},
						},
					},
				},
			},
		},
	}, router)

	item, ok, err := service.HookWriteItem(HookEvent{
		Target:    "codex",
		Event:     "user_input",
		Prompt:    "remember this",
		Workspace: "/tmp/project",
		Metadata:  map[string]string{"project": "paxm"},
		Raw:       json.RawMessage(`{"prompt":"remember this"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected hook write item")
	}
	if item.Text != `User input: remember this / {"prompt":"remember this"}` {
		t.Fatalf("unexpected hook write text: %q", item.Text)
	}
	if item.Source != "hook:codex:user_input" || item.Profile != "default" {
		t.Fatalf("unexpected hook write routing: %#v", item)
	}
	if item.Metadata["hook_event"] != "user_input" || item.Metadata["workspace"] != "/tmp/project" || item.Metadata["project"] != "paxm" {
		t.Fatalf("unexpected hook metadata: %#v", item.Metadata)
	}
}

func TestServiceIngestTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Config
		input     IngestInput
		wantErr   string
		wantItems []string
	}{
		{
			name:    "blank text is rejected",
			cfg:     writeConfigForProfiles("default"),
			input:   IngestInput{Text: " \n\t "},
			wantErr: "ingest text is required",
		},
		{
			name:    "missing write profile is rejected",
			cfg:     writeConfigForProfiles("default"),
			input:   IngestInput{Text: "memory", Profile: "missing"},
			wantErr: "write profile missing is not configured",
		},
		{
			name:      "default profile trims and writes",
			cfg:       writeConfigForProfiles("default"),
			input:     IngestInput{Text: "  keep this  ", Source: "cli", Metadata: map[string]string{"k": "v"}},
			wantItems: []string{"keep this"},
		},
		{
			name:      "explicit profile routes writes",
			cfg:       writeConfigForProfiles("archive"),
			input:     IngestInput{Text: "archive this", Profile: "archive"},
			wantItems: []string{"archive this"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &captureProvider{}
			router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
			if err != nil {
				t.Fatal(err)
			}
			service := New(tt.cfg, router)
			result, err := service.Ingest(context.Background(), tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Ingest() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ingest() error = %v", err)
			}
			if len(result.Refs) != len(tt.wantItems) {
				t.Fatalf("refs = %#v, want %d", result.Refs, len(tt.wantItems))
			}
			gotItems := itemTexts(provider.items)
			if !reflect.DeepEqual(gotItems, tt.wantItems) {
				t.Fatalf("items = %#v, want %#v", gotItems, tt.wantItems)
			}
		})
	}
}

func TestServiceIngestBatchTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       config.Config
		input     IngestBatchInput
		wantErr   string
		wantItems []string
		wantRefs  int
	}{
		{
			name: "empty batch is a no-op",
			cfg:  writeConfigForProfiles("default"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: " "},
			}},
			wantItems: []string{},
		},
		{
			name: "groups default and explicit profiles",
			cfg:  writeConfigForProfiles("default", "archive"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: " default one "},
				{Text: "archive one", Profile: "archive"},
				{Text: ""},
			}},
			wantItems: []string{"archive one", "default one"},
			wantRefs:  2,
		},
		{
			name: "continues valid groups when another profile is missing",
			cfg:  writeConfigForProfiles("default"),
			input: IngestBatchInput{Items: []IngestInput{
				{Text: "keep"},
				{Text: "missing", Profile: "missing"},
			}},
			wantErr:   "write profile missing is not configured",
			wantItems: []string{"keep"},
			wantRefs:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &captureProvider{}
			router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
			if err != nil {
				t.Fatal(err)
			}
			service := New(tt.cfg, router)
			result, err := service.IngestBatch(context.Background(), tt.input)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("IngestBatch() error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("IngestBatch() error = %v", err)
			}
			gotItems := itemTexts(provider.items)
			if !reflect.DeepEqual(gotItems, tt.wantItems) {
				t.Fatalf("items = %#v, want %#v", gotItems, tt.wantItems)
			}
			if len(result.Refs) != tt.wantRefs {
				t.Fatalf("refs = %#v, want %d", result.Refs, tt.wantRefs)
			}
		})
	}
}

func TestHookBufferConfigTable(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Version: 1,
		Agents: map[string]config.AgentConfig{
			"codex": {
				Enabled: true,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{
							Buffer: config.HookBufferConfig{Enabled: true, Flush: true, FlushCount: 3},
						},
					},
				},
			},
			"disabled": {
				Enabled: false,
				Hooks: map[string]config.AgentHookConfig{
					"user_input": {
						Write: config.HookWriteConfig{Buffer: config.HookBufferConfig{Enabled: true}},
					},
				},
			},
		},
	}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: &captureProvider{}, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(cfg, router)

	tests := []struct {
		name  string
		event HookEvent
		want  config.HookBufferConfig
	}{
		{name: "blank target defaults to codex", event: HookEvent{Event: "user_input"}, want: config.HookBufferConfig{Enabled: true, Flush: true, FlushCount: 3}},
		{name: "missing hook returns zero config", event: HookEvent{Target: "codex", Event: "turn_end"}},
		{name: "disabled agent returns zero config", event: HookEvent{Target: "disabled", Event: "user_input"}},
		{name: "missing agent returns zero config", event: HookEvent{Target: "missing", Event: "user_input"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.HookBufferConfig(tt.event); got != tt.want {
				t.Fatalf("HookBufferConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRenderAndInsertionHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("templates", func(t *testing.T) {
		tests := []struct {
			name    string
			tmpl    string
			event   HookEvent
			want    string
			wantErr bool
		}{
			{name: "blank template", tmpl: "  ", want: ""},
			{name: "renders prompt workspace and raw json", tmpl: "{{ .prompt }} @ {{ .workspace }} {{ .raw_json }}", event: HookEvent{Prompt: "hello", Workspace: "/repo", Raw: json.RawMessage(`{"prompt":"hello"}`)}, want: `hello @ /repo {"prompt":"hello"}`},
			{name: "missing key renders zero value", tmpl: "{{ .metadata.missing }}", event: HookEvent{Metadata: map[string]string{"present": "yes"}}, want: ""},
			{name: "invalid template", tmpl: "{{", wantErr: true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := renderHookTemplate(tt.tmpl, tt.event)
				if tt.wantErr {
					if err == nil {
						t.Fatal("expected error")
					}
					return
				}
				if err != nil {
					t.Fatalf("renderHookTemplate() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("renderHookTemplate() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("insertion filters", func(t *testing.T) {
		hits := []memory.MemoryHit{
			{ID: "low", Text: "paxm low score", Score: 0.2},
			{ID: "term", Text: "paxm matching term", Score: 0.9},
			{ID: "other", Text: "unrelated", Score: 0.95},
		}
		tests := []struct {
			name   string
			query  string
			policy config.HookInsertionConfig
			want   []string
		}{
			{name: "zero policy keeps all", query: "paxm", want: []string{"low", "term", "other"}},
			{name: "score term and limit", query: "please paxm", policy: config.HookInsertionConfig{MinScore: 0.8, MaxItems: 1, RequireQueryTerms: true}, want: []string{"term"}},
			{name: "stopwords do not match", query: "please check", policy: config.HookInsertionConfig{RequireQueryTerms: true}, want: []string{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := hitIDs(filterHookInsertionHits(hits, tt.query, tt.policy))
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("filtered hits = %#v, want %#v", got, tt.want)
				}
			})
		}
	})

	t.Run("misc helpers", func(t *testing.T) {
		if got := fmtMissingProfile("write", "archive").Error(); got != "write profile archive is not configured" {
			t.Fatalf("fmtMissingProfile() = %q", got)
		}
		if got := firstNonEmpty("", " ", "value"); got != "value" {
			t.Fatalf("firstNonEmpty() = %q", got)
		}
	})
}

func writeConfigForProfiles(names ...string) config.Config {
	profiles := make(map[string]config.WriteProfileConfig, len(names))
	for _, name := range names {
		profiles[name] = config.WriteProfileConfig{
			Providers: []config.ProviderRouteConfig{{Name: "capture", Required: true, Weight: 1}},
		}
	}
	return config.Config{Version: 1, WriteProfiles: profiles}
}

func itemTexts(items []memory.MemoryItem) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.Text)
	}
	sort.Strings(values)
	return values
}

func hitIDs(hits []memory.MemoryHit) []string {
	values := make([]string, 0, len(hits))
	for _, hit := range hits {
		values = append(values, hit.ID)
	}
	return values
}
