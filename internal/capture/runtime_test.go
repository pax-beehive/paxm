package capture

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters/sqlite"
	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/operator"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

func TestRuntimeStoresCompletedTurnAsSingleSQLiteMemory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	provider, err := sqlite.New("sqlite", filepath.Join(dir, "memory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Read: true, Write: true, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	engine := tools.New(cfg, router)
	core := facade.New(cfg, router)
	runtime, err := Open(OpenOptions{
		Config: cfg, QueuePath: filepath.Join(dir, "capture.sqlite"), Policy: New(core),
		Operator: operator.New(filepath.Join(dir, "config.yaml"), cfg, engine, router),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)

	sessionID := "session-turn-7"
	longPrompt := "User evidence: " + strings.Repeat("background material ", 1800)
	if _, err := runtime.Process(context.Background(), Command{EventID: "event-user", Event: Event{
		Target: "codex", Event: "user_input", Prompt: longPrompt,
		Messages: []Message{{Role: "user", Text: longPrompt}}, Metadata: map[string]string{"session_id": sessionID},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Process(context.Background(), Command{EventID: "event-assistant", Event: Event{
		Target: "codex", Event: "turn_end", Assistant: "The neon marmot decision is approved.",
		Messages: []Message{{Role: "assistant", Text: "The neon marmot decision is approved."}}, Metadata: map[string]string{"session_id": sessionID},
	}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		hits, searchErr := provider.Search(context.Background(), memory.SearchQuery{Text: "neon marmot decision", Limit: 5})
		if searchErr == nil && len(hits) == 1 {
			hit := hits[0]
			if hit.Metadata["session_id"] != sessionID || hit.Metadata["turn_id"] == "" {
				t.Fatalf("turn identity metadata = %#v", hit.Metadata)
			}
			startedAt, startErr := time.Parse(time.RFC3339Nano, hit.Metadata["started_at"])
			endedAt, endErr := time.Parse(time.RFC3339Nano, hit.Metadata["ended_at"])
			if startErr != nil || endErr != nil || startedAt.After(endedAt) {
				t.Fatalf("turn time metadata = %#v, startErr=%v, endErr=%v", hit.Metadata, startErr, endErr)
			}
			originalBytes, sizeErr := strconv.Atoi(hit.Metadata["sqlite_original_bytes"])
			if hit.Metadata["sqlite_excerpted"] != "true" || sizeErr != nil || originalBytes <= 24*1024 {
				t.Fatalf("long turn was not retained as one evidence record: %#v", hit.Metadata)
			}
			if hit.Source != "hook:episode" || hit.Metadata["paxm_episode_events"] != "2" {
				t.Fatalf("episode provenance = source %q metadata %#v", hit.Source, hit.Metadata)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn was not recalled: hits=%#v err=%v", hits, searchErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type policyStub struct{}

func (policyStub) WriteItem(Event) (tools.RememberInput, bool, error) {
	return tools.RememberInput{Text: "remember me", Profile: "default"}, true, nil
}

type operatorStub struct{}

func (operatorStub) RememberBatchToProvider(_ context.Context, provider string, input tools.RememberBatchInput) (tools.RememberResult, error) {
	return tools.RememberResult{Refs: []memory.MemoryRef{{Provider: provider, ID: "ref-1"}}}, nil
}
func (operatorStub) CleanupExpired(context.Context, int) (memory.CleanupExpiredResult, error) {
	return memory.CleanupExpiredResult{}, nil
}

type operatorNoRefStub struct {
	err error
}

func (operatorNoRefStub) RememberBatchToProvider(context.Context, string, tools.RememberBatchInput) (tools.RememberResult, error) {
	return tools.RememberResult{}, nil
}
func (operatorNoRefStub) CleanupExpired(context.Context, int) (memory.CleanupExpiredResult, error) {
	return memory.CleanupExpiredResult{}, nil
}

func TestOpenRejectsInvalidConfiguration(t *testing.T) {
	if _, err := Open(OpenOptions{}); err == nil || !strings.Contains(err.Error(), "capture policy is required") {
		t.Fatalf("err=%v", err)
	}
	cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	if _, err := Open(OpenOptions{Config: cfg, Policy: policyStub{}}); err == nil || !strings.Contains(err.Error(), "capture operator is required") {
		t.Fatalf("err=%v", err)
	}
	cfg.CaptureQueue.MaxEpisodeAge = "not-a-duration"
	if _, err := Open(OpenOptions{Config: cfg, QueuePath: filepath.Join(t.TempDir(), "queue.sqlite"), Policy: policyStub{}, Operator: operatorStub{}}); err == nil {
		t.Fatal("expected invalid duration error")
	}
}

func TestDeliveryFuncReportsProviderErrors(t *testing.T) {
	deliver := deliveryFunc(operatorNoRefStub{})
	_, err := deliver(context.Background(), "sqlite", capturequeue.Episode{})
	if err == nil || !strings.Contains(err.Error(), "returned no memory reference") {
		t.Fatalf("err=%v", err)
	}
	var providerErr = errors.New("provider unavailable")
	_, err = deliveryFunc(operatorNoRefErrorStub{err: providerErr})(context.Background(), "sqlite", capturequeue.Episode{})
	if !errors.Is(err, providerErr) {
		t.Fatalf("err=%v", err)
	}
}

type operatorNoRefErrorStub struct{ err error }

func (s operatorNoRefErrorStub) RememberBatchToProvider(context.Context, string, tools.RememberBatchInput) (tools.RememberResult, error) {
	return tools.RememberResult{}, s.err
}
func (operatorNoRefErrorStub) CleanupExpired(context.Context, int) (memory.CleanupExpiredResult, error) {
	return memory.CleanupExpiredResult{}, nil
}

func TestWorkerErrorReporterAndTruncateError(t *testing.T) {
	var recorded telemetry.Event
	workerErrorReporter(func(event telemetry.Event) { recorded = event })("worker_error", errors.New(strings.Repeat("x", 300)))
	if recorded.HookEvent != "worker_error" || len(recorded.Error) != 240 || recorded.Success {
		t.Fatalf("event=%#v", recorded)
	}
	workerErrorReporter(nil)("ignored", errors.New("ignored"))
	if truncateError(nil) != "" || truncateError(errors.New("short")) != "short" {
		t.Fatalf("unexpected truncation")
	}
}

func TestOpenOwnsDeliveryAndTelemetryWorkflow(t *testing.T) {
	cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	events := make(chan telemetry.Event, 1)
	runtime, err := Open(OpenOptions{Config: cfg, QueuePath: filepath.Join(t.TempDir(), "queue.sqlite"), Policy: policyStub{}, Operator: operatorStub{}, Record: func(event telemetry.Event) { events <- event }})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Process(context.Background(), Command{EventID: "evt", Event: Event{Target: "codex", Event: "turn_end", Metadata: map[string]string{"session_id": "s"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if !event.Success || event.Provider != "sqlite" || event.RefCount != 1 {
			t.Fatalf("event=%#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delivery telemetry not emitted")
	}
}

func TestCleanupWorkerSchedulesWithoutBlockingAndDrainsOnClose(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	worker := newCleanupWorker(func(context.Context) error { close(started); <-release; close(finished); return nil }, nil)
	scheduled := make(chan struct{})
	go func() { worker.Schedule(); close(scheduled) }()
	select {
	case <-scheduled:
	case <-time.After(time.Second):
		t.Fatal("schedule blocked")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	closed := make(chan struct{})
	go func() { worker.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("closed before cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not drain")
	}
	select {
	case <-finished:
	default:
		t.Fatal("cleanup did not finish")
	}
}
func (policyStub) BufferConfig(Event) config.HookBufferConfig {
	return config.HookBufferConfig{Enabled: true, Flush: true}
}

func TestRuntimeOwnsHookToDurableQueueOrdering(t *testing.T) {
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{Providers: func(string) []string { return []string{"sqlite"} }})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	notified := false
	runtime := NewRuntime(policyStub{}, queue, func() { notified = true }, nil)
	receipt, err := runtime.Process(context.Background(), Command{EventID: "evt-1", Event: Event{Target: "codex", Event: "turn_end", Workspace: "/tmp/project", Metadata: map[string]string{"session_id": "session-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Buffered || receipt.Flushed != 1 || !notified {
		t.Fatalf("receipt=%#v notified=%t", receipt, notified)
	}
	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingDeliveries != 1 {
		t.Fatalf("stats=%#v", stats)
	}
}
