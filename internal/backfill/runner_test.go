package backfill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/sessions"
)

type countingProvider struct {
	items                  []memory.MemoryItem
	preserveTurnBoundaries bool
}

func (p *countingProvider) Name() string { return "target" }
func (p *countingProvider) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return nil, nil
}
func (p *countingProvider) Put(_ context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	p.items = append(p.items, item)
	return memory.MemoryRef{ID: item.ID}, nil
}
func (p *countingProvider) Health(context.Context) error { return nil }
func (p *countingProvider) PreserveTurnBoundaries() bool { return p.preserveTurnBoundaries }

func TestRunnerPreservesAnUnboundedTurnWhenProviderSupportsIt(t *testing.T) {
	t.Parallel()

	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	longQuestion := strings.Repeat("question ", 4000)
	content := fmt.Sprintf(`{"type":"session_meta","timestamp":"2026-07-01T10:00:00Z","payload":{"id":"session","cwd":"/repo"}}
{"type":"event_msg","timestamp":"2026-07-01T10:01:00Z","payload":{"type":"user_message","message":%q}}
{"type":"event_msg","timestamp":"2026-07-01T10:02:00Z","payload":{"type":"agent_message","phase":"final_answer","message":"answer"}}
`, longQuestion)
	if err := os.WriteFile(sessionPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &countingProvider{preserveTurnBoundaries: true}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := Runner{Store: store, Service: facade.New(config.Config{Version: 1}, router).Tools()}
	status, err := runner.Run(context.Background(), RunOptions{
		Scope: "scope", RunID: "run", Agent: "codex", Provider: "target",
		Files:  []sessions.File{{Path: sessionPath, Size: int64(len(content))}},
		Cutoff: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Uploaded != 1 || len(provider.items) != 1 {
		t.Fatalf("turn was split: status=%#v writes=%d", status, len(provider.items))
	}
	item := provider.items[0]
	if len(item.Text) <= maxItemBytes || item.Turn == nil || item.Turn.SessionID != "session" || item.Turn.TurnID == "" {
		t.Fatalf("turn boundary was not preserved: %#v", item)
	}
}

func TestRunnerResumesWithoutUploadingSucceededTurnsAgain(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"type":"session_meta","timestamp":"2026-07-01T10:00:00Z","payload":{"id":"session","cwd":"/repo"}}
{"type":"event_msg","timestamp":"2026-07-01T10:01:00Z","payload":{"type":"user_message","message":"question"}}
{"type":"event_msg","timestamp":"2026-07-01T10:02:00Z","payload":{"type":"agent_message","phase":"final_answer","message":"answer"}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &countingProvider{}
	router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
	if err != nil {
		t.Fatal(err)
	}
	service := facade.New(config.Config{Version: 1}, router).Tools()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := Runner{Store: store, Service: service}
	options := RunOptions{
		Scope:    Scope("config", "codex", "target"),
		RunID:    "first",
		Agent:    "codex",
		Provider: "target",
		Files:    []sessions.File{{Path: sessionPath, Size: 100}},
		Cutoff:   time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
	}

	first, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.RunID = "second"
	second, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.items) != 1 || first.Uploaded != 1 || second.Uploaded != 0 || second.Skipped != 1 {
		t.Fatalf("unexpected resume result: writes=%d first=%#v second=%#v", len(provider.items), first, second)
	}
}

func TestRunnerEdgeCasesTable(t *testing.T) {
	t.Parallel()

	t.Run("not configured", func(t *testing.T) {
		_, err := (Runner{}).Run(context.Background(), RunOptions{})
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("canceled context pauses", func(t *testing.T) {
		sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		provider := &countingProvider{}
		router, err := memory.NewRouter([]memory.ProviderBinding{{Provider: provider, Write: true}})
		if err != nil {
			t.Fatal(err)
		}
		store, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		status, err := (Runner{
			Store:     store,
			Service:   facade.New(config.Config{Version: 1}, router).Tools(),
			ProcessID: func() int { return 4242 },
		}).Run(ctx, RunOptions{
			Scope:    "scope",
			RunID:    "run",
			Agent:    "codex",
			Provider: "target",
			Files:    []sessions.File{{Path: sessionPath, Size: 3}},
		})
		if err == nil || status.State != "paused" {
			t.Fatalf("Run() status=%#v err=%v, want paused error", status, err)
		}
		if status.PID != 4242 {
			t.Fatalf("Run() PID = %d, want injected PID", status.PID)
		}
	})

	t.Run("new run id", func(t *testing.T) {
		if id := NewRunID(); strings.TrimSpace(id) == "" {
			t.Fatal("NewRunID returned an empty value")
		}
	})
}

func TestTurnItemsAndSplitUTF8Table(t *testing.T) {
	t.Parallel()

	t.Run("split utf8", func(t *testing.T) {
		tests := []struct {
			name  string
			value string
			size  int
			want  []string
		}{
			{name: "short", value: "abc", size: 5, want: []string{"abc"}},
			{name: "ascii chunks", value: "abcdef", size: 3, want: []string{"abc", "def"}},
			{name: "trims chunk boundaries", value: "abc  def", size: 5, want: []string{"abc", "def"}},
			{name: "keeps multibyte runes valid", value: "你好世界", size: 7, want: []string{"你好", "世界"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := splitUTF8(tt.value, tt.size); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("splitUTF8() = %#v, want %#v", got, tt.want)
				}
			})
		}
	})

	t.Run("turn item metadata", func(t *testing.T) {
		createdAt := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
		turn := sessions.Turn{
			ID:        "turn-1",
			Agent:     "codex",
			SessionID: "session-1",
			Workspace: "/repo",
			User:      "question",
			Assistant: "answer",
			CreatedAt: createdAt,
		}
		items := turnItems(turn, false)
		if len(items) != 1 {
			t.Fatalf("turnItems() = %#v", items)
		}
		item := items[0]
		if item.ID != "turn-1" || item.Source != "backfill:codex" || !item.CreatedAt.Equal(createdAt) {
			t.Fatalf("unexpected item identity: %#v", item)
		}
		for key, want := range map[string]string{"backfill": "true", "agent": "codex", "session_id": "session-1", "workspace": "/repo"} {
			if item.Metadata[key] != want {
				t.Fatalf("metadata[%s] = %q, want %q: %#v", key, item.Metadata[key], want, item.Metadata)
			}
		}
	})

	t.Run("turn item multipart", func(t *testing.T) {
		turn := sessions.Turn{
			ID:        "large",
			Agent:     "pi",
			SessionID: "session",
			User:      strings.Repeat("界", 9000),
			Assistant: strings.Repeat("答", 9000),
		}
		items := turnItems(turn, false)
		if len(items) < 2 {
			t.Fatalf("expected multipart items, got %#v", items)
		}
		if items[0].ID != "large-part-1" || items[0].Metadata["part"] != "1" || items[0].Metadata["parts"] == "" {
			t.Fatalf("multipart metadata missing: %#v", items[0])
		}
	})

	t.Run("sqlite preserves an unbounded turn", func(t *testing.T) {
		turn := sessions.Turn{
			ID:        "large",
			Agent:     "codex",
			SessionID: "session",
			User:      strings.Repeat("question ", 4000),
			Assistant: strings.Repeat("answer ", 4000),
			CreatedAt: time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
		}
		items := turnItems(turn, true)
		if len(items) != 1 || items[0].ID != "large" {
			t.Fatalf("SQLite turn was split: %#v", items)
		}
		if items[0].Turn == nil || items[0].Turn.SessionID != "session" || items[0].Turn.TurnID != "large" {
			t.Fatalf("turn boundary missing: %#v", items[0].Turn)
		}
	})
}
