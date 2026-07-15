package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestRecorderRotatesEventLogsAndKeepsMetrics(t *testing.T) {
	t.Parallel()

	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{
		Enabled:           &enabled,
		Dir:               dir,
		EventsFile:        "events.jsonl",
		MetricsFile:       "metrics.json",
		MaxEventFileBytes: 180,
		MaxEventFiles:     2,
		RetentionDays:     7,
	}, filepath.Join(dir, "config.yaml"))

	for i := 0; i < 8; i++ {
		if err := recorder.Record(Event{
			Time:     time.Now().UTC(),
			Kind:     "recall",
			Source:   "cli",
			Command:  "recall",
			Profile:  "default",
			Success:  true,
			HitCount: 2,
			ProviderRecalls: map[string]int{
				"sqlite": 1,
			},
			ProviderHits: map[string]int{
				"sqlite": 2,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl.1")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl.2")); !os.IsNotExist(err) {
		t.Fatalf("expected only one rotated backup, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "metrics.json")); err != nil {
		t.Fatal(err)
	}

	summary, err := recorder.History(7)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Recalls != 8 || summary.Totals.Hits != 16 {
		t.Fatalf("unexpected summary totals: %#v", summary.Totals)
	}
	if len(summary.Providers) != 1 || summary.Providers[0].Name != "sqlite" || summary.Providers[0].Counter.Hits != 16 {
		t.Fatalf("unexpected provider summary: %#v", summary.Providers)
	}
	if summary.Providers[0].Counter.Recalls != 8 {
		t.Fatalf("unexpected provider recall count: %#v", summary.Providers[0])
	}
	if summary.Storage.TotalBytes == 0 || summary.Storage.MaxFiles != 2 {
		t.Fatalf("unexpected storage summary: %#v", summary.Storage)
	}
}

func TestRecorderTailEventsReadsAcrossRotatedLogs(t *testing.T) {
	t.Parallel()

	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{
		Enabled:           &enabled,
		Dir:               dir,
		EventsFile:        "events.jsonl",
		MetricsFile:       "metrics.json",
		MaxEventFileBytes: 180,
		MaxEventFiles:     4,
		RetentionDays:     7,
	}, filepath.Join(dir, "config.yaml"))
	for i := 1; i <= 6; i++ {
		if err := recorder.Record(Event{
			Time:    time.Date(2026, 7, 10, 10, i, 0, 0, time.UTC),
			Kind:    "test",
			Command: "event-" + strconv.Itoa(i),
			Success: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	events, err := recorder.TailEvents(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("TailEvents() returned %d events: %#v", len(events), events)
	}
	for i, want := range []string{"event-4", "event-5", "event-6"} {
		if events[i].Command != want {
			t.Fatalf("TailEvents()[%d].Command = %q, want %q", i, events[i].Command, want)
		}
	}
}

func TestRecorderFollowEventsStreamsAppendAndRotation(t *testing.T) {
	t.Parallel()

	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{
		Enabled:           &enabled,
		Dir:               dir,
		EventsFile:        "events.jsonl",
		MetricsFile:       "metrics.json",
		MaxEventFileBytes: 220,
		MaxEventFiles:     4,
		RetentionDays:     7,
	}, filepath.Join(dir, "config.yaml"))
	if err := recorder.Record(Event{Kind: "test", Command: "initial", Success: true}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- recorder.FollowEvents(ctx, 1, 100*time.Millisecond, func(event Event) error {
			commands <- event.Command
			return nil
		})
	}()
	waitCommand := func(want string) {
		t.Helper()
		select {
		case got := <-commands:
			if got != want {
				t.Fatalf("follow command = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for follow command %q", want)
		}
	}
	waitCommand("initial")
	if err := recorder.Record(Event{Kind: "test", Command: "appended", Success: true}); err != nil {
		t.Fatal(err)
	}
	waitCommand("appended")
	if err := recorder.Record(Event{Kind: "test", Command: "rotation-one", Success: false, Error: strings.Repeat("x", 180)}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(Event{Kind: "test", Command: "rotation-two", Success: false, Error: strings.Repeat("y", 180)}); err != nil {
		t.Fatal(err)
	}
	waitCommand("rotation-one")
	waitCommand("rotation-two")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("FollowEvents did not stop after cancellation")
	}
}

func TestRecorderAggregatesPassiveAgentsAndProviderWrites(t *testing.T) {
	t.Parallel()

	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{
		Enabled:       &enabled,
		Dir:           dir,
		RetentionDays: 7,
	}, filepath.Join(dir, "config.yaml"))

	events := []Event{
		{
			Time:          time.Now().UTC(),
			Kind:          "hook_recall",
			Source:        "hook",
			Target:        "codex",
			HookEvent:     "user_input",
			Profile:       "passive",
			Success:       true,
			HitCount:      2,
			InsertedCount: 1,
			ProviderRecalls: map[string]int{
				"zep": 1,
			},
			ProviderHits: map[string]int{
				"zep": 2,
			},
		},
		{
			Time:      time.Now().UTC(),
			Kind:      "hook_write",
			Source:    "hook",
			Target:    "codex",
			HookEvent: "turn_end",
			Profile:   "default",
			Success:   true,
			ItemCount: 3,
			Flushed:   3,
			ProviderWrites: map[string]int{
				"zep": 1,
			},
			ProviderRefs: map[string]int{
				"zep": 3,
			},
		},
	}
	for _, event := range events {
		if err := recorder.Record(event); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := recorder.History(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Agents) != 1 || summary.Agents[0].Name != "codex" {
		t.Fatalf("unexpected agent summary: %#v", summary.Agents)
	}
	agent := summary.Agents[0].Counter
	if agent.Recalls != 1 || agent.Writes != 1 || agent.Inserted != 1 || agent.Flushes != 1 {
		t.Fatalf("unexpected agent counter: %#v", agent)
	}
	if len(summary.Providers) != 1 || summary.Providers[0].Name != "zep" {
		t.Fatalf("unexpected provider summary: %#v", summary.Providers)
	}
	provider := summary.Providers[0].Counter
	if provider.Recalls != 1 || provider.Hits != 2 || provider.Writes != 1 || provider.Refs != 3 {
		t.Fatalf("unexpected provider counter: %#v", provider)
	}
}

func TestRecorderAggregatesProviderWriteAndPassiveLatencySeparately(t *testing.T) {
	t.Parallel()
	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{Enabled: &enabled, Dir: dir, RetentionDays: 7}, filepath.Join(dir, "config.yaml"))
	for _, event := range []Event{
		{Time: time.Now().UTC(), Kind: "hook_delivery", Success: true, Provider: "sqlite", ProviderWrites: map[string]int{"sqlite": 1}, ProviderDurationMS: 10, PassiveWriteLatencyTotalMS: 110, PassiveWriteSamples: 1},
		{Time: time.Now().UTC(), Kind: "hook_delivery", Success: true, Provider: "sqlite", ProviderWrites: map[string]int{"sqlite": 1}, ProviderDurationMS: 30, PassiveWriteLatencyTotalMS: 380, PassiveWriteSamples: 2},
		{Time: time.Now().UTC(), Kind: "hook_delivery", Success: false, Provider: "sqlite", ProviderDurationMS: 999, PassiveWriteLatencyTotalMS: 999, PassiveWriteSamples: 1},
	} {
		if err := recorder.Record(event); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := recorder.History(7)
	if err != nil {
		t.Fatal(err)
	}
	provider := summary.Providers[0].Counter
	if provider.ProviderWriteSamples != 2 || provider.ProviderWriteDurationMS != 40 || provider.PassiveWriteSamples != 3 || provider.PassiveWriteLatencyTotalMS != 490 {
		t.Fatalf("unexpected provider latency aggregates: %#v", provider)
	}
}

func TestRecorderAggregatesProviderRecallLatencyAndIsolationOutcomes(t *testing.T) {
	t.Parallel()
	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{Enabled: &enabled, Dir: dir, RetentionDays: 7}, filepath.Join(dir, "config.yaml"))
	event := Event{
		Time: time.Now().UTC(), Kind: "hook_recall", Success: true, RecallTimedOut: true,
		ProviderRecalls: map[string]int{"sqlite": 1, "zep": 1},
		ProviderRecallDetails: []memory.ProviderRecall{
			{Provider: "sqlite", DurationMS: 12, Outcome: memory.ProviderRecallSuccess, TimeoutMS: 250},
			{Provider: "zep", DurationMS: 250, Outcome: memory.ProviderRecallTimeout, TimeoutMS: 250, BulkheadBusy: true},
		},
	}
	if err := recorder.Record(event); err != nil {
		t.Fatal(err)
	}
	summary, err := recorder.History(7)
	if err != nil {
		t.Fatal(err)
	}
	providers := map[string]Counter{}
	for _, provider := range summary.Providers {
		providers[provider.Name] = provider.Counter
	}
	if summary.Totals.RecallTimeouts != 1 {
		t.Fatalf("overall recall timeouts = %d, want 1", summary.Totals.RecallTimeouts)
	}
	if got := providers["sqlite"]; got.ProviderRecallSamples != 1 || got.ProviderRecallDurationMS != 12 || got.ProviderRecallTimeouts != 0 {
		t.Fatalf("sqlite recall metrics = %#v", got)
	}
	if got := ProviderRecallP95MS(providers["sqlite"]); got != 25 {
		t.Fatalf("sqlite p95 bucket = %dms, want 25ms", got)
	}
	if got := providers["zep"]; got.ProviderRecallSamples != 1 || got.ProviderRecallDurationMS != 250 || got.ProviderRecallTimeouts != 1 || got.ProviderRecallBulkheadSkips != 1 {
		t.Fatalf("zep recall metrics = %#v", got)
	}
	if got := ProviderRecallP95MS(providers["zep"]); got != 250 {
		t.Fatalf("zep p95 bucket = %dms, want 250ms", got)
	}
}

func TestRecorderPersistsProviderScoreDiagnostics(t *testing.T) {
	t.Parallel()
	enabled := true
	dir := t.TempDir()
	recorder := NewRecorder(config.TelemetryConfig{Enabled: &enabled, Dir: dir}, filepath.Join(dir, "config.yaml"))
	if err := recorder.Record(Event{
		Time: time.Now().UTC(), Kind: "recall", Success: true,
		ProviderRecallDetails: []memory.ProviderRecall{{
			Provider: "mem0", CandidateCount: 2, EligibleCount: 1, RawScoreKinds: []string{"mem0_distance"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := recorder.TailEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].ProviderRecallDetails) != 1 {
		t.Fatalf("provider recall diagnostics missing: %#v", events)
	}
	diagnostic := events[0].ProviderRecallDetails[0]
	if diagnostic.CandidateCount != 2 || diagnostic.EligibleCount != 1 || len(diagnostic.RawScoreKinds) != 1 || diagnostic.RawScoreKinds[0] != "mem0_distance" {
		t.Fatalf("provider score diagnostics = %#v", diagnostic)
	}
}

func TestQueryFieldsAvoidPreviewByDefault(t *testing.T) {
	t.Parallel()

	hash, length, preview := QueryFields("PASSIVE_RECALL_PROBE_001", false, 8)
	if hash == "" || length != 24 || preview != "" {
		t.Fatalf("unexpected query fields without preview: hash=%q length=%d preview=%q", hash, length, preview)
	}
	_, _, preview = QueryFields("PASSIVE_RECALL_PROBE_001", true, 7)
	if preview != "PASSIVE" {
		t.Fatalf("unexpected preview: %q", preview)
	}
}
