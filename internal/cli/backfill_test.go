package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/backfill"
	"github.com/pax-beehive/paxm/internal/config"
)

func TestCLIBackfillForegroundReportsProgressAndResumes(t *testing.T) {
	configPath, sessionDir := writeBackfillFixture(t, true)
	t.Setenv("PAXM_CODEX_SESSIONS", sessionDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	args := []string{"--config", configPath, "backfill", "run", "--agent", "codex", "--provider", "archive", "--rate", "1000/s"}
	if code := Main(args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("first backfill failed with code %d: %s", code, stderr.String())
	}
	for _, expected := range []string{"Backfill progress", "speed=", "ETA=", "Backfill complete: uploaded=1 skipped=0"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("foreground output missing %q: %s", expected, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main(args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("resumed backfill failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Backfill complete: uploaded=0 skipped=1") {
		t.Fatalf("resumed run uploaded the turn again: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"--config", configPath, "backfill", "status", "--agent", "codex", "--provider", "archive"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("status failed with code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "state=completed") || !strings.Contains(stdout.String(), "skipped=1") {
		t.Fatalf("unexpected status: %s", stdout.String())
	}
}

func TestCLIBackfillRequiresExplicitCutoffForExistingAgentConfig(t *testing.T) {
	configPath, sessionDir := writeBackfillFixture(t, false)
	t.Setenv("PAXM_CODEX_SESSIONS", sessionDir)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Main([]string{"--config", configPath, "backfill", "scan", "--agent", "codex"}, nil, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "requires --before") {
		t.Fatalf("missing cutoff was not rejected: code=%d stderr=%s", code, stderr.String())
	}
}

func TestBackfillAgentAndCutoffHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("agents", func(t *testing.T) {
		tests := []struct {
			name    string
			input   string
			want    string
			wantErr string
		}{
			{name: "codex lower", input: "codex", want: "codex"},
			{name: "claude alias", input: "Claude-Code", want: "claude"},
			{name: "pi trimmed", input: " pi ", want: "pi"},
			{name: "unsupported", input: "other", wantErr: "unsupported agent"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := validateBackfillAgent(tt.input)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("validateBackfillAgent() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("validateBackfillAgent() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("validateBackfillAgent() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("cutoffs", func(t *testing.T) {
		fallback := config.AgentConfig{PassiveWriteStartedAt: "2026-07-02T03:04:05-07:00"}
		tests := []struct {
			name    string
			value   string
			agent   config.AgentConfig
			want    time.Time
			wantErr string
		}{
			{name: "rfc3339 nano", value: "2026-07-01T01:02:03.000000004-07:00", want: time.Date(2026, 7, 1, 8, 2, 3, 4, time.UTC)},
			{name: "date", value: "2026-07-01", want: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
			{name: "agent integration fallback", agent: fallback, want: time.Date(2026, 7, 2, 10, 4, 5, 0, time.UTC)},
			{name: "missing", wantErr: "requires --before"},
			{name: "invalid", value: "July 1", wantErr: "invalid --before"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := backfillCutoff(tt.value, tt.agent)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("backfillCutoff() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("backfillCutoff() error = %v", err)
				}
				if !got.Equal(tt.want) {
					t.Fatalf("backfillCutoff() = %s, want %s", got, tt.want)
				}
			})
		}
	})
}

func TestBackfillFormattingAndRateHelpersTable(t *testing.T) {
	t.Parallel()

	t.Run("rates", func(t *testing.T) {
		tests := []struct {
			value   string
			want    time.Duration
			wantErr string
		}{
			{value: "30/m", want: 2 * time.Second},
			{value: "2/s", want: 500 * time.Millisecond},
			{value: "4/hour", want: 15 * time.Minute},
			{value: "bad", wantErr: "use a value"},
			{value: "0/m", wantErr: "count must be positive"},
			{value: "1/day", wantErr: "unit must be s, m, or h"},
		}
		for _, tt := range tests {
			t.Run(tt.value, func(t *testing.T) {
				got, err := parseBackfillRate(tt.value)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("parseBackfillRate() error = %v, want %q", err, tt.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("parseBackfillRate() error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("parseBackfillRate() = %s, want %s", got, tt.want)
				}
			})
		}
	})

	t.Run("bytes", func(t *testing.T) {
		tests := []struct {
			value int64
			want  string
		}{
			{value: 0, want: "0 B"},
			{value: 1023, want: "1023 B"},
			{value: 1024, want: "1.0 KiB"},
			{value: 1536, want: "1.5 KiB"},
			{value: 1024 * 1024, want: "1.0 MiB"},
		}
		for _, tt := range tests {
			t.Run(tt.want, func(t *testing.T) {
				if got := formatBytes(tt.value); got != tt.want {
					t.Fatalf("formatBytes() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("error text", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
			want string
		}{
			{name: "nil", want: ""},
			{name: "error", err: errors.New("boom"), want: "boom"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := errorText(tt.err); got != tt.want {
					t.Fatalf("errorText() = %q, want %q", got, tt.want)
				}
			})
		}
	})
}

func TestBackfillStateAndStatusHelpersTable(t *testing.T) {
	t.Run("state dirs", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state-home"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config-home"))
		customConfig := filepath.Join(dir, "project", "config.yaml")
		tests := []struct {
			name       string
			cfg        config.Config
			configPath string
			want       string
		}{
			{
				name:       "telemetry dir wins",
				cfg:        config.Config{Telemetry: config.TelemetryConfig{Dir: filepath.Join(dir, "telemetry")}},
				configPath: customConfig,
				want:       filepath.Join(dir, "telemetry", "backfill"),
			},
			{
				name:       "custom config uses sibling state",
				cfg:        config.Config{},
				configPath: customConfig,
				want:       filepath.Join(filepath.Dir(customConfig), "state", "backfill"),
			},
			{
				name:       "default config uses default state home",
				cfg:        config.Config{},
				configPath: config.DefaultConfigPath(),
				want:       filepath.Join(dir, "state-home", "paxm", "backfill"),
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := backfillStateDir(tt.cfg, tt.configPath); got != tt.want {
					t.Fatalf("backfillStateDir() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("status output", func(t *testing.T) {
		tests := []struct {
			name     string
			status   backfill.Status
			progress bool
			want     []string
		}{
			{
				name:     "progress with bytes and eta",
				progress: true,
				status:   backfill.Status{State: "running", TotalBytes: 100, ProcessedBytes: 25, TotalFiles: 4, ProcessedFiles: 1, Uploaded: 2, Skipped: 1, Failed: 1, ItemsPerSecond: 1.25, ETASeconds: 30},
				want:     []string{"Backfill progress", "25.0%", "files=1/4", "uploaded=2", "ETA=30s"},
			},
			{
				name:   "status defaults to complete percent",
				status: backfill.Status{State: "completed"},
				want:   []string{"Backfill status", "100.0%", "ETA=--"},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var out bytes.Buffer
				writeBackfillStatus(&out, tt.status, tt.progress)
				for _, want := range tt.want {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("status output missing %q: %s", want, out.String())
					}
				}
			})
		}
	})
}

func TestBackfillWorkerStartResultHelpersTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    func(t *testing.T) string
		result  backfillStartResult
		wantNil bool
	}{
		{
			name:    "blank path is ignored",
			path:    func(t *testing.T) string { return "" },
			result:  backfillStartResult{Started: true, RunID: "run"},
			wantNil: true,
		},
		{
			name: "result is written",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "start.json")
			},
			result: backfillStartResult{Started: true, RunID: "run"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.path(t)
			if err := writeBackfillStartResult(path, tt.result); err != nil {
				t.Fatalf("writeBackfillStartResult() error = %v", err)
			}
			if tt.wantNil {
				return
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got backfillStartResult
			if err := json.Unmarshal(content, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.result) {
				t.Fatalf("written result = %#v, want %#v", got, tt.result)
			}
		})
	}

	t.Run("finish writes error and returns original error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "start.json")
		wantErr := errors.New("worker failed")
		r := runner{}
		if err := r.finishBackfillWorkerStart(path, wantErr); !errors.Is(err, wantErr) {
			t.Fatalf("finishBackfillWorkerStart() error = %v, want %v", err, wantErr)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got backfillStartResult
		if err := json.Unmarshal(content, &got); err != nil {
			t.Fatal(err)
		}
		if got.Error != "worker failed" {
			t.Fatalf("written error = %#v", got)
		}
	})
}

func writeBackfillFixture(t *testing.T, withIntegrationTime bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := config.DefaultConfig(configPath)
	cfg.Providers = map[string]config.ProviderConfig{
		"archive": {Type: "sqlite", Enabled: true, Path: filepath.Join(dir, "archive.sqlite")},
	}
	agent := cfg.Agents["codex"]
	if withIntegrationTime {
		agent.PassiveWriteStartedAt = "2026-07-02T00:00:00Z"
	}
	cfg.Agents["codex"] = agent
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session_meta","timestamp":"2026-07-01T10:00:00Z","payload":{"id":"session","cwd":"/repo"}}
{"type":"event_msg","timestamp":"2026-07-01T10:01:00Z","payload":{"type":"user_message","message":"historical question"}}
{"type":"event_msg","timestamp":"2026-07-01T10:02:00Z","payload":{"type":"agent_message","phase":"final_answer","message":"historical answer"}}
`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, sessionDir
}
