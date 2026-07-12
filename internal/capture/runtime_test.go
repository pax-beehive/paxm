package capture

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

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
	worker := newCleanupWorker(func(context.Context) { close(started); <-release; close(finished) })
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
