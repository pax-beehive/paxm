package capturequeue_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
)

func BenchmarkDurableAppend(b *testing.B) {
	queue, err := capturequeue.Open(filepath.Join(b.TempDir(), "capture.sqlite"), capturequeue.Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer queue.Close()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := queue.Append(context.Background(), capturequeue.Event{
			SessionKey: "codex/session/benchmark",
			Item:       facade.IngestInput{Text: "benchmark passive write event", Profile: "ltm"},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDuplicateEventIDRejectsDifferentPayload(t *testing.T) {
	ctx := context.Background()
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	first := capturequeue.Event{ID: "stable", SessionKey: "codex/workspace/a/session/1", Item: facade.IngestInput{Text: "first"}}
	if _, err := queue.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	if receipt, err := queue.Append(ctx, first); err != nil || !receipt.Duplicate {
		t.Fatalf("identical retry was not idempotent: receipt=%#v err=%v", receipt, err)
	}
	first.Item.Text = "changed"
	if _, err := queue.Append(ctx, first); err == nil {
		t.Fatal("conflicting duplicate event was accepted")
	}
}

func TestAppendSurvivesQueueRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := queue.Append(ctx, capturequeue.Event{
		ID:         "event-1",
		SessionKey: "codex/session/session-a",
		Item: facade.IngestInput{
			Text:    "Codex user input:\nfix the release pipeline",
			Profile: "ltm",
			Source:  "hook:codex:user_input",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Duplicate || receipt.Sequence != 1 {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := capturequeue.Open(path, capturequeue.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stats, err := reopened.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingEvents != 1 {
		t.Fatalf("pending events=%d, want 1", stats.PendingEvents)
	}
}

func TestTerminalEpisodeDeliversProvidersIndependentlyAndConcurrently(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	started := make(map[string]bool)
	release := make(chan struct{})
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite", "mem0"} },
		Deliver: func(_ context.Context, provider string, episode capturequeue.Episode) (string, error) {
			mu.Lock()
			started[provider] = true
			bothStarted := started["sqlite"] && started["mem0"]
			mu.Unlock()
			if bothStarted {
				close(release)
			}
			select {
			case <-release:
			case <-time.After(time.Second):
				return "", errors.New("providers were not started concurrently")
			}
			if !episode.Complete || len(episode.Events) != 2 {
				return "", errors.New("incomplete episode")
			}
			return provider + "-ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for index, event := range []capturequeue.Event{
		{ID: "event-user", SessionKey: "codex/session/a", Item: facade.IngestInput{Text: "question", Profile: "ltm"}},
		{ID: "event-end", SessionKey: "codex/session/a", Terminal: true, Item: facade.IngestInput{Text: "answer", Profile: "ltm"}},
	} {
		if _, err := queue.Append(ctx, event); err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
	}
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingEvents != 0 || stats.PendingEpisodes != 1 || stats.PendingDeliveries != 2 {
		t.Fatalf("unexpected sealed stats: %#v", stats)
	}
	result, err := queue.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 2 || result.Failed != 0 {
		t.Fatalf("unexpected delivery result: %#v", result)
	}
}

func TestRunOnceRefreshesOnlyEpisodesTouchedByTheRun(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	options := capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver:   func(context.Context, string, capturequeue.Episode) (string, error) { return "ref", nil },
	}
	queue, err := capturequeue.Open(path, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session/first", Terminal: true, Item: facade.IngestInput{Text: "first", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var firstID string
	if err := db.QueryRow(`SELECT episode_id FROM capture_episodes WHERE session_key = 'session/first'`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE capture_episodes SET state = 'pending' WHERE episode_id = ?`, firstID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	queue, err = capturequeue.Open(path, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session/second", Terminal: true, Item: facade.IngestInput{Text: "second", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow(`SELECT state FROM capture_episodes WHERE episode_id = ?`, firstID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("unrelated episode state = %q, want pending", state)
	}
}

func TestQueueCreatesSchedulingIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"capture_deliveries_schedule", "capture_episodes_schedule"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("scheduling index %q missing", name)
		}
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN
SELECT d.episode_id, d.provider, d.profiles_json, e.payload_json, e.payload_hash, d.attempts
FROM capture_deliveries d JOIN capture_episodes e ON e.episode_id = d.episode_id
WHERE d.state IN ('pending', 'retry')
  AND (d.next_attempt_at = '' OR d.next_attempt_at <= ?)
  AND NOT EXISTS (
    SELECT 1 FROM capture_deliveries prior_d
    JOIN capture_episodes prior_e ON prior_e.episode_id = prior_d.episode_id
    WHERE prior_d.provider = d.provider
      AND prior_e.session_key = e.session_key
      AND prior_e.first_sequence < e.first_sequence
      AND prior_d.state NOT IN ('delivered', 'dead')
  )
ORDER BY e.created_at LIMIT 100`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"capture_deliveries_schedule", "capture_episodes_schedule"} {
		if !strings.Contains(plan.String(), name) {
			t.Fatalf("scheduler query plan does not use %q:\n%s", name, plan.String())
		}
	}
}

func TestTerminalOnlySealsItsOwnSession(t *testing.T) {
	ctx := context.Background()
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			delivered = episode
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, event := range []capturequeue.Event{
		{SessionKey: "codex/session/a", Item: facade.IngestInput{Text: "a question", Profile: "ltm"}},
		{SessionKey: "claude/session/b", Item: facade.IngestInput{Text: "b question", Profile: "ltm"}},
		{SessionKey: "codex/session/a", Terminal: true, Item: facade.IngestInput{Text: "a answer", Profile: "ltm"}},
	} {
		if _, err := queue.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered.SessionKey != "codex/session/a" || len(delivered.Events) != 2 {
		t.Fatalf("wrong episode delivered: %#v", delivered)
	}
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingEvents != 1 || stats.PendingDeliveries != 0 {
		t.Fatalf("session B should remain independently queued: %#v", stats)
	}
}

func TestTerminalMarksEpisodeIncompleteWhenSourceSequenceHasGap(t *testing.T) {
	ctx := context.Background()
	one, three := int64(1), int64(3)
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			delivered = episode
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, event := range []capturequeue.Event{
		{SessionKey: "pi/session/a", Sequence: &one, Item: facade.IngestInput{Text: "question", Profile: "ltm"}},
		{SessionKey: "pi/session/a", Sequence: &three, Final: &three, Terminal: true, Item: facade.IngestInput{Text: "answer", Profile: "ltm"}},
	} {
		if _, err := queue.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered.Complete || len(delivered.Missing) != 1 || delivered.Missing[0] != 2 {
		t.Fatalf("gap was not reported: %#v", delivered)
	}
	item := delivered.IngestInput()
	if item.Metadata["paxm_episode_complete"] != "false" || item.Metadata["paxm_missing_sequences"] != "2" {
		t.Fatalf("integrity metadata missing: %#v", item.Metadata)
	}
}

func TestTerminalRejectsDuplicateAndConflictingSourceSequences(t *testing.T) {
	ctx := context.Background()
	one, two, three := int64(1), int64(2), int64(3)
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			delivered = episode
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, event := range []capturequeue.Event{
		{SessionKey: "pi/session/a", Sequence: &one, Final: &two, Item: facade.IngestInput{Text: "first", Profile: "ltm"}},
		{SessionKey: "pi/session/a", Sequence: &one, Final: &three, Terminal: true, Item: facade.IngestInput{Text: "duplicate", Profile: "ltm"}},
	} {
		if _, err := queue.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered.Complete || len(delivered.Integrity) < 2 {
		t.Fatalf("source sequence conflicts were not reported: %#v", delivered)
	}
}

func TestProviderRetryDoesNotRepeatSuccessfulProvider(t *testing.T) {
	ctx := context.Background()
	calls := map[string]int{}
	var callsMu sync.Mutex
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		RetryMin:  time.Millisecond,
		Providers: func(string) []string { return []string{"sqlite", "mem0"} },
		Deliver: func(_ context.Context, provider string, _ capturequeue.Episode) (string, error) {
			callsMu.Lock()
			defer callsMu.Unlock()
			calls[provider]++
			if provider == "mem0" && calls[provider] == 1 {
				return "", errors.New("temporary failure")
			}
			return provider + "-ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "codex/session/a", Terminal: true, Item: facade.IngestInput{Text: "episode", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	first, err := queue.RunOnce(ctx)
	if err != nil || first.Delivered != 1 || first.Failed != 1 {
		t.Fatalf("first run=%#v err=%v", first, err)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := queue.RunOnce(ctx)
	if err != nil || second.Delivered != 1 || second.Failed != 0 {
		t.Fatalf("second run=%#v err=%v", second, err)
	}
	if calls["sqlite"] != 1 || calls["mem0"] != 2 {
		t.Fatalf("successful provider was repeated: %#v", calls)
	}
}

func TestProviderPreservesEpisodeOrderWithinSession(t *testing.T) {
	ctx := context.Background()
	var delivered []string
	failFirst := true
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		RetryMin:  time.Millisecond,
		Providers: func(string) []string { return []string{"mem0"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			text := episode.IngestInput().Text
			if text == "first" && failFirst {
				failFirst = false
				return "", errors.New("retry first")
			}
			delivered = append(delivered, text)
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, text := range []string{"first", "second"} {
		if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "codex/session/a", Terminal: true, Item: facade.IngestInput{Text: text, Profile: "ltm"}}); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := queue.RunOnce(ctx); err != nil || result.Failed != 1 || len(delivered) != 0 {
		t.Fatalf("later episode bypassed failed predecessor: result=%#v delivered=%v err=%v", result, delivered, err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0] != "first" || delivered[1] != "second" {
		t.Fatalf("delivery order=%v", delivered)
	}
}

func TestDeadLetterUnblocksLaterEpisodeAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	var delivered []string
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		MaxAttempts: 1,
		Providers:   func(string) []string { return []string{"mem0"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			text := episode.IngestInput().Text
			if text == "poison" {
				return "", errors.New("permanent failure")
			}
			delivered = append(delivered, text)
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, text := range []string{"poison", "later"} {
		if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: text, Profile: "ltm"}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := queue.RunOnce(ctx)
	if err != nil || first.Dead != 1 {
		t.Fatalf("first run=%#v err=%v", first, err)
	}
	second, err := queue.RunOnce(ctx)
	if err != nil || second.Delivered != 1 || len(delivered) != 1 || delivered[0] != "later" {
		t.Fatalf("later episode remained blocked: result=%#v delivered=%v err=%v", second, delivered, err)
	}
}

func TestMaxEpisodeAgeSealsAbandonedTurnAsIncomplete(t *testing.T) {
	ctx := context.Background()
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		MaxEpisodeAge: time.Millisecond,
		Providers:     func(string) []string { return []string{"sqlite"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			delivered = episode
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "claude/session/abandoned", Item: facade.IngestInput{Text: "unfinished tool result", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if sealed, err := queue.SealExpired(ctx); err != nil || sealed != 1 {
		t.Fatalf("sealed=%d err=%v", sealed, err)
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered.Complete || len(delivered.Events) != 1 {
		t.Fatalf("abandoned episode should be delivered incomplete: %#v", delivered)
	}
}

func TestEpisodePreservesMixedProfileTierAndExpiry(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	startedAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Minute)
	episode := capturequeue.Episode{ID: "episode", SessionKey: "session", Complete: true, Events: []facade.IngestInput{
		{Text: "short task state", Profile: "stm", Tier: memory.TierSTM, ExpiresAt: &expires, CreatedAt: startedAt},
		{Text: "durable decision", Profile: "ltm", Tier: memory.TierLTM, CreatedAt: endedAt},
	}}
	items := episode.IngestInputs()
	if len(items) != 2 {
		t.Fatalf("items=%d, want 2", len(items))
	}
	byProfile := map[string]facade.IngestInput{items[0].Profile: items[0], items[1].Profile: items[1]}
	if byProfile["stm"].Tier != memory.TierSTM || byProfile["stm"].ExpiresAt == nil || !byProfile["stm"].ExpiresAt.Equal(expires) {
		t.Fatalf("STM policy lost: %#v", byProfile["stm"])
	}
	if byProfile["ltm"].Tier != memory.TierLTM || byProfile["ltm"].ExpiresAt != nil {
		t.Fatalf("LTM policy lost: %#v", byProfile["ltm"])
	}
	if byProfile["stm"].ID == byProfile["ltm"].ID {
		t.Fatal("mixed policy groups must have independent idempotency IDs")
	}
	for profile, item := range byProfile {
		if item.Turn == nil || !item.Turn.StartedAt.Equal(startedAt) || !item.Turn.EndedAt.Equal(endedAt) {
			t.Fatalf("%s turn boundary = %#v", profile, item.Turn)
		}
	}
}

func TestEpisodeIngestInputPreservesUnboundedTurnBoundary(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Minute)
	episode := capturequeue.Episode{
		ID:         "turn-42",
		SessionKey: "codex/workspace/project/session/session-7",
		Events: []facade.IngestInput{
			{Text: "User: " + strings.Repeat("question ", 4000), CreatedAt: startedAt, Metadata: map[string]string{"session_id": "session-7"}},
			{Text: "Assistant: answer", CreatedAt: endedAt, Metadata: map[string]string{"session_id": "session-7"}},
		},
	}

	item := episode.IngestInput()
	if !strings.Contains(item.Text, "Assistant: answer") || len(item.Text) < 24*1024 {
		t.Fatalf("turn text was truncated or split: %d bytes", len(item.Text))
	}
	if item.Turn == nil {
		t.Fatal("turn boundary is missing")
	}
	if item.Turn.SessionID != "session-7" || item.Turn.TurnID != "turn-42" {
		t.Fatalf("turn identity = %#v", item.Turn)
	}
	if !item.Turn.StartedAt.Equal(startedAt) || !item.Turn.EndedAt.Equal(endedAt) {
		t.Fatalf("turn times = %#v", item.Turn)
	}
}

func TestDeliveryUsesRoutingSnapshotFromSealTime(t *testing.T) {
	ctx := context.Background()
	routes := map[string][]string{"alpha": {"mem0"}, "beta": {"sqlite"}}
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(profile string) []string { return routes[profile] },
		Deliver: func(_ context.Context, provider string, episode capturequeue.Episode) (string, error) {
			if provider == "mem0" {
				delivered = episode
			}
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for _, event := range []capturequeue.Event{
		{SessionKey: "session", Item: facade.IngestInput{Text: "alpha event", Profile: "alpha"}},
		{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: "beta event", Profile: "beta"}},
	} {
		if _, err := queue.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	routes = map[string][]string{}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(delivered.Events) != 1 || delivered.Events[0].Profile != "alpha" {
		t.Fatalf("delivery route was re-evaluated or mixed: %#v", delivered)
	}
}

func TestCorruptedEpisodeFailsChecksumBeforeDelivery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{Providers: func(string) []string { return []string{"sqlite"} }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: "original", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "healthy-session", Terminal: true, Item: facade.IngestInput{Text: "healthy", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE capture_episodes SET payload_json = replace(payload_json, 'original', 'tampered')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	called := 0
	queue, err = capturequeue.Open(path, capturequeue.Options{Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
		called++
		return "ref", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	result, err := queue.RunOnce(ctx)
	if err != nil || result.Dead != 1 || result.Delivered != 1 {
		t.Fatalf("corrupted episode was not quarantined: result=%#v err=%v", result, err)
	}
	if called != 1 {
		t.Fatalf("provider calls=%d, want only the healthy episode", called)
	}
}

func TestAdmissionTextSurvivesEpisodeChecksumVerification(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	var delivered capturequeue.Episode
	queue, err := capturequeue.Open(path, capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(_ context.Context, _ string, episode capturequeue.Episode) (string, error) {
			delivered = episode
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{
		Text: "visible text", AdmissionText: "private admission text", Profile: "ltm",
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := queue.RunOnce(ctx)
	if err != nil || result.Delivered != 1 || result.Dead != 0 {
		t.Fatalf("delivery result=%#v err=%v", result, err)
	}
	if len(delivered.Events) != 1 || delivered.Events[0].Text != "visible text" {
		t.Fatalf("delivered episode=%#v", delivered)
	}
}

func TestMalformedRouteSnapshotIsQuarantinedWithoutBlockingHealthyDelivery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{Providers: func(string) []string { return []string{"sqlite"} }})
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"bad", "healthy"} {
		if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: session, Terminal: true, Item: facade.IngestInput{Text: session, Profile: "ltm"}}); err != nil {
			t.Fatal(err)
		}
	}
	queue.Close()
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`UPDATE capture_deliveries SET profiles_json = '{' WHERE episode_id = (SELECT episode_id FROM capture_episodes WHERE session_key = 'bad')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	delivered := 0
	queue, err = capturequeue.Open(path, capturequeue.Options{Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
		delivered++
		return "ref", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	result, err := queue.RunOnce(ctx)
	if err != nil || result.Dead != 1 || result.Delivered != 1 || delivered != 1 {
		t.Fatalf("malformed route blocked queue: result=%#v delivered=%d err=%v", result, delivered, err)
	}
}

func TestDeliveringStateRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{Providers: func(string) []string { return []string{"mem0"} }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: "recover me", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	queue.Close()
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`UPDATE capture_deliveries SET state = 'delivering'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	delivered := 0
	queue, err = capturequeue.Open(path, capturequeue.Options{Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
		delivered++
		return "ref", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("recovered deliveries=%d, want 1", delivered)
	}
}

func TestConfiguredProviderConcurrencyIsEnforced(t *testing.T) {
	ctx := context.Background()
	var current, maximum atomic.Int32
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers:           func(string) []string { return []string{"sqlite"} },
		ProviderConcurrency: func(string) int { return 1 },
		Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
			now := current.Add(1)
			for now > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), now) {
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	for index := 0; index < 3; index++ {
		if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: fmt.Sprintf("session-%d", index), Terminal: true, Item: facade.IngestInput{Text: "event", Profile: "ltm"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrency=%d, want 1", maximum.Load())
	}
}

func TestDeliveryOutcomeSeparatesProviderDurationFromPassiveWriteLatency(t *testing.T) {
	ctx := context.Background()
	capturedAt := time.Now().UTC().Add(-200 * time.Millisecond)
	var outcome capturequeue.DeliveryOutcome
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "ref", nil
		},
		OnDelivery: func(value capturequeue.DeliveryOutcome) { outcome = value },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: "event", Profile: "ltm", CreatedAt: capturedAt}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if outcome.Duration < 8*time.Millisecond || outcome.Duration > 100*time.Millisecond {
		t.Fatalf("provider duration=%s", outcome.Duration)
	}
	if outcome.PassiveWriteSamples != 1 || outcome.PassiveWriteLatencyTotal < 25*time.Millisecond || outcome.PassiveWriteLatencyTotal > time.Second {
		t.Fatalf("passive write latency total=%s samples=%d", outcome.PassiveWriteLatencyTotal, outcome.PassiveWriteSamples)
	}
}

func TestPassiveWriteCaptureTimeSurvivesQueueRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "capture.sqlite")
	queue, err := capturequeue.Open(path, capturequeue.Options{Providers: func(string) []string { return []string{"sqlite"} }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Append(ctx, capturequeue.Event{SessionKey: "session", Terminal: true, Item: facade.IngestInput{Text: "event", Profile: "ltm"}}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	var outcome capturequeue.DeliveryOutcome
	queue, err = capturequeue.Open(path, capturequeue.Options{
		Deliver:    func(context.Context, string, capturequeue.Episode) (string, error) { return "ref", nil },
		OnDelivery: func(value capturequeue.DeliveryOutcome) { outcome = value },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if outcome.PassiveWriteSamples != 1 || outcome.PassiveWriteLatencyTotal < 15*time.Millisecond {
		t.Fatalf("restart lost capture time: total=%s samples=%d", outcome.PassiveWriteLatencyTotal, outcome.PassiveWriteSamples)
	}
}
