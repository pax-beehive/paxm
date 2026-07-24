package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadCodexTurns(t *testing.T) {
	path := writeSessionFile(t, `{"type":"session_meta","timestamp":"2026-07-01T10:00:00Z","payload":{"id":"codex-session","cwd":"/repo"}}
{"type":"event_msg","timestamp":"2026-07-01T10:01:00Z","payload":{"type":"user_message","message":"find the bug"}}
{"type":"event_msg","timestamp":"2026-07-01T10:01:10Z","payload":{"type":"agent_message","phase":"commentary","message":"checking"}}
{"type":"event_msg","timestamp":"2026-07-01T10:02:00Z","payload":{"type":"agent_message","phase":"final_answer","message":"fixed it"}}
{"type":"response_item","timestamp":"2026-07-01T10:02:00Z","payload":{"type":"message","role":"assistant","content":"duplicate"}}
`)

	turns, err := ReadFile("codex", path, TimeRange{})
	if err != nil {
		t.Fatal(err)
	}
	assertTurn(t, turns, "codex-session", "/repo", "find the bug", "fixed it")
}

func TestReadClaudeTurnsExcludesSidechainsAndToolResults(t *testing.T) {
	path := writeSessionFile(t, `{"type":"user","uuid":"u1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-01T10:01:00Z","message":{"role":"user","content":"implement it"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-01T10:02:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"done"},{"type":"tool_use","name":"Read"}]}}
{"type":"user","uuid":"tool","parentUuid":"a1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-01T10:02:10Z","message":{"role":"user","content":[{"type":"tool_result","content":"large output"}]}}
{"type":"assistant","uuid":"side","parentUuid":"u1","sessionId":"claude-session","cwd":"/repo","isSidechain":true,"timestamp":"2026-07-01T10:03:00Z","message":{"role":"assistant","content":[{"type":"text","text":"ignore me"}]}}
`)

	turns, err := ReadFile("claude", path, TimeRange{})
	if err != nil {
		t.Fatal(err)
	}
	assertTurn(t, turns, "claude-session", "/repo", "implement it", "done")
}

func TestReadClaudeTurnsAssignsStableSequenceForMatchingTimestamps(t *testing.T) {
	path := writeSessionFile(t, `{"type":"user","uuid":"u1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-03T03:55:16.243Z","message":{"role":"user","content":"first"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-03T03:55:16.243Z","message":{"role":"assistant","content":"answer one"}}
{"type":"user","uuid":"u2","parentUuid":"a1","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-03T03:55:16.243Z","message":{"role":"user","content":"second"}}
{"type":"assistant","uuid":"a2","parentUuid":"u2","sessionId":"claude-session","cwd":"/repo","timestamp":"2026-07-03T03:55:16.243Z","message":{"role":"assistant","content":"answer two"}}
`)

	turns, err := ReadFile("claude", path, TimeRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	if !turns[0].CreatedAt.Equal(turns[1].CreatedAt) {
		t.Fatalf("fixture timestamps differ: %s != %s", turns[0].CreatedAt, turns[1].CreatedAt)
	}
	if turns[0].Sequence != 1 || turns[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", turns[0].Sequence, turns[1].Sequence)
	}
}

func TestReadPiTurnsAndTimeRange(t *testing.T) {
	path := writeSessionFile(t, `{"type":"session","id":"pi-session","cwd":"/repo","timestamp":"2026-07-01T10:00:00Z"}
{"type":"message","id":"u1","parentId":null,"timestamp":"2026-07-01T10:01:00Z","message":{"role":"user","content":"first"}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-07-01T10:02:00Z","message":{"role":"assistant","content":[{"type":"text","text":"answer one"},{"type":"thinking","thinking":"hidden"}]}}
{"type":"message","id":"u2","parentId":"a1","timestamp":"2026-07-02T10:01:00Z","message":{"role":"user","content":"too late"}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-07-02T10:02:00Z","message":{"role":"assistant","content":[{"type":"text","text":"answer two"}]}}
{"type":"message","id":"u3","parentId":"a2","timestamp":"2026-07-03T10:01:00Z","message":{"role":"user","content":"third"}}
{"type":"message","id":"a3","parentId":"u3","timestamp":"2026-07-03T10:02:00Z","message":{"role":"assistant","content":[{"type":"text","text":"answer three"}]}}
`)

	window := TimeRange{
		After:  time.Date(2026, 7, 2, 10, 1, 0, 0, time.UTC),
		Before: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),
	}
	turns, err := ReadFile("pi", path, window)
	if err != nil {
		t.Fatal(err)
	}
	assertTurn(t, turns, "pi-session", "/repo", "too late", "answer two")
	if turns[0].Sequence != 2 {
		t.Fatalf("sequence = %d, want stable source sequence 2", turns[0].Sequence)
	}
}

func TestRootAndDiscoverTables(t *testing.T) {
	t.Run("root environment overrides", func(t *testing.T) {
		base := t.TempDir()
		tests := []struct {
			name    string
			agent   string
			env     map[string]string
			want    string
			wantErr string
		}{
			{name: "codex explicit sessions", agent: "codex", env: map[string]string{"PAXM_CODEX_SESSIONS": filepath.Join(base, "codex-sessions")}, want: filepath.Join(base, "codex-sessions")},
			{name: "codex home", agent: "codex", env: map[string]string{"CODEX_HOME": filepath.Join(base, "codex-home")}, want: filepath.Join(base, "codex-home", "sessions")},
			{name: "claude explicit sessions", agent: "claude", env: map[string]string{"PAXM_CLAUDE_SESSIONS": filepath.Join(base, "claude-sessions")}, want: filepath.Join(base, "claude-sessions")},
			{name: "claude config dir", agent: "claude", env: map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(base, "claude")}, want: filepath.Join(base, "claude", "projects")},
			{name: "pi explicit sessions", agent: "pi", env: map[string]string{"PAXM_PI_SESSIONS": filepath.Join(base, "pi-sessions")}, want: filepath.Join(base, "pi-sessions")},
			{name: "pi session dir", agent: "pi", env: map[string]string{"PI_CODING_AGENT_SESSION_DIR": filepath.Join(base, "pi-session-dir")}, want: filepath.Join(base, "pi-session-dir")},
			{name: "pi agent dir", agent: "pi", env: map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(base, "pi-agent")}, want: filepath.Join(base, "pi-agent", "sessions")},
			{name: "unsupported", agent: "other", wantErr: "unsupported session agent"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				clearSessionEnv(t)
				for key, value := range tt.env {
					t.Setenv(key, value)
				}
				got, err := Root(tt.agent)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("Root() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("Root() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("Root() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("discover jsonl files", func(t *testing.T) {
		clearSessionEnv(t)
		root := t.TempDir()
		t.Setenv("PAXM_CODEX_SESSIONS", root)
		if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("ignore"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "a.jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if err := os.MkdirAll(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "c.JSONL"), []byte("{}\n{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		files, err := Discover("codex")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 2 {
			t.Fatalf("Discover() files = %#v", files)
		}
		if files[0].Path != filepath.Join(root, "a.jsonl") || files[1].Path != filepath.Join(nested, "c.JSONL") {
			t.Fatalf("files were not sorted or filtered: %#v", files)
		}
		if files[0].Size != 3 || files[1].Size != 6 {
			t.Fatalf("file sizes were not recorded: %#v", files)
		}
	})

	t.Run("discover missing root is empty", func(t *testing.T) {
		clearSessionEnv(t)
		t.Setenv("PAXM_CODEX_SESSIONS", filepath.Join(t.TempDir(), "missing"))
		files, err := Discover("codex")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 0 {
			t.Fatalf("Discover() = %#v, want empty", files)
		}
	})
}

func assertTurn(t *testing.T, turns []Turn, sessionID, workspace, user, assistant string) {
	t.Helper()
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want 1: %#v", len(turns), turns)
	}
	turn := turns[0]
	if turn.SessionID != sessionID || turn.Workspace != workspace || turn.User != user || turn.Assistant != assistant {
		t.Fatalf("unexpected turn: %#v", turn)
	}
	if turn.ID == "" || turn.CreatedAt.IsZero() {
		t.Fatalf("turn identity/time missing: %#v", turn)
	}
}

func writeSessionFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearSessionEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PAXM_CODEX_SESSIONS",
		"CODEX_HOME",
		"PAXM_CLAUDE_SESSIONS",
		"CLAUDE_CONFIG_DIR",
		"PAXM_PI_SESSIONS",
		"PI_CODING_AGENT_SESSION_DIR",
		"PI_CODING_AGENT_DIR",
	} {
		t.Setenv(key, "")
	}
}
