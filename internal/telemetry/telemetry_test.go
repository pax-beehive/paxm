package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
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
