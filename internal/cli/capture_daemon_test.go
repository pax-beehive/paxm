package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/adapters"
	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
)

func TestHookDaemonLockAllowsOnlyOneOwnerAndRecoversAfterRelease(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	release, err := acquireHookDaemonLock(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireHookDaemonLock(configPath); err == nil {
		t.Fatal("second daemon unexpectedly acquired the same lock")
	}
	release()
	releaseAgain, err := acquireHookDaemonLock(configPath)
	if err != nil {
		t.Fatalf("lock was not reusable after release: %v", err)
	}
	releaseAgain()
}

func TestCaptureSessionKeyIncludesTargetWorkspaceAndSession(t *testing.T) {
	first := capture.SessionKey(capture.Event{Target: "codex", Workspace: "/workspace/a", Metadata: map[string]string{"session_id": "same"}})
	second := capture.SessionKey(capture.Event{Target: "codex", Workspace: "/workspace/b", Metadata: map[string]string{"session_id": "same"}})
	third := capture.SessionKey(capture.Event{Target: "claude", Workspace: "/workspace/a", Metadata: map[string]string{"session_id": "same"}})
	if first == second || first == third || second == third {
		t.Fatalf("capture partitions collided: %q %q %q", first, second, third)
	}
	unknownA := capture.SessionKey(capture.Event{Target: "codex", Workspace: "/workspace/a", Metadata: map[string]string{"event_id": "a"}})
	unknownB := capture.SessionKey(capture.Event{Target: "codex", Workspace: "/workspace/a", Metadata: map[string]string{"event_id": "b"}})
	if unknownA == unknownB {
		t.Fatalf("unidentified sessions were collapsed: %q", unknownA)
	}
}

func TestCaptureQueueHookAcknowledgesDurableTerminalWithoutWaitingForProvider(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(configPath)
	provider := cfg.Providers["sqlite"]
	provider.Path = filepath.Join(t.TempDir(), "memory.sqlite")
	cfg.Providers["sqlite"] = provider
	router, err := adapters.DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := facade.New(cfg, router)
	rt := &paxruntime.Runtime{Config: cfg, Tools: service.Tools(), Capture: capture.New(service)}
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"sqlite"} },
		Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
			close(providerStarted)
			<-releaseProvider
			return "ref", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	notify := func() { go func() { _, _ = queue.RunOnce(context.Background()) }() }
	captureRuntime := capture.NewRuntime(rt.Capture, queue, notify, func() {})
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() {
		_, _, err := handleCaptureQueueConn(context.Background(), captureRuntime, server)
		done <- err
	}()
	raw := json.RawMessage(`{"session_id":"session-a","last_assistant_message":"done"}`)
	if err := json.NewEncoder(client).Encode(hookBufferRequest{Target: "codex", Event: "turn_end", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	var response hookBufferResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.Buffered || response.Flushed != 1 {
		t.Fatalf("unexpected response: %#v", response)
	}
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("delivery worker did not start")
	}
	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingDeliveries != 1 {
		t.Fatalf("terminal was not durably queued: %#v", stats)
	}
	close(releaseProvider)
}

func TestCaptureQueueShutdownSealsWithoutWaitingForProvider(t *testing.T) {
	queue, err := capturequeue.Open(filepath.Join(t.TempDir(), "capture.sqlite"), capturequeue.Options{
		Providers: func(string) []string { return []string{"slow"} },
		Deliver: func(context.Context, string, capturequeue.Episode) (string, error) {
			t.Fatal("shutdown attempted provider delivery")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Append(context.Background(), capturequeue.Event{
		SessionKey: "session-a",
		Item:       facade.IngestInput{Text: "durable before update"},
	}); err != nil {
		t.Fatal(err)
	}

	server, client := net.Pipe()
	captureRuntime := capture.NewRuntime(nil, queue, func() {}, func() {})
	done := make(chan struct {
		flushed  int
		shutdown bool
		err      error
	}, 1)
	go func() {
		flushed, shutdown, err := handleCaptureQueueConn(context.Background(), captureRuntime, server)
		done <- struct {
			flushed  int
			shutdown bool
			err      error
		}{flushed, shutdown, err}
	}()
	if err := json.NewEncoder(client).Encode(hookBufferRequest{Action: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	var response hookBufferResponse
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	client.Close()
	result := <-done
	if result.err != nil || !result.shutdown || result.flushed != 1 || !response.OK {
		t.Fatalf("unexpected shutdown result: result=%#v response=%#v", result, response)
	}
	stats, err := queue.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingDeliveries != 1 {
		t.Fatalf("sealed episode was not retained for the next daemon: %#v", stats)
	}
}

func TestWaitForHookDaemonStopWaitsForSocketAndLock(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	paths := []string{hookSocketPath(configPath), hookDaemonLockPath(configPath)}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("present"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = os.Remove(paths[0])
		time.Sleep(40 * time.Millisecond)
		_ = os.Remove(paths[1])
	}()
	if err := waitForHookDaemonStop(configPath, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestFlushExistingHookBufferReportsStaleSocket(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	socketPath := hookSocketPath(configPath)
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := flushExistingHookBuffer(configPath, true); err == nil {
		t.Fatal("stale daemon socket was silently ignored")
	}
}

func TestFlushExistingHookBufferReportsLockWithoutReadySocket(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	lockPath := hookDaemonLockPath(configPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	err := flushExistingHookBuffer(configPath, true)
	if err == nil || !strings.Contains(err.Error(), "socket did not become ready") {
		t.Fatalf("unexpected lock-only result: %v", err)
	}
	if time.Since(startedAt) < time.Second {
		t.Fatal("did not wait for daemon startup window")
	}
}
