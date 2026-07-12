package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestLoadTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		wantErr   error
		wantSetup bool
		wantOK    bool
	}{
		{
			name: "missing config explains setup command",
			setup: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "missing.yaml")
			},
			wantErr:   config.ErrConfigMissing,
			wantSetup: true,
		},
		{
			name: "valid sqlite config builds service and health",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yaml")
				cfg := config.DefaultConfig(path)
				cfg.Providers = map[string]config.ProviderConfig{
					"sqlite": {Type: "sqlite", Enabled: true, Path: filepath.Join(dir, "memory.sqlite")},
				}
				if err := config.Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			rt, err := Load(path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Load() error = %v, want %v", err, tt.wantErr)
				}
				if tt.wantSetup && !strings.Contains(err.Error(), "run `paxm --config "+path+" setup`") {
					t.Fatalf("missing setup hint in error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if rt.ConfigPath != path || rt.Tools == nil || rt.Capture == nil || rt.Operator == nil {
				t.Fatalf("unexpected runtime: %#v", rt)
			}
			health, err := rt.Health(context.Background())
			if err != nil {
				t.Fatalf("Health() error = %v", err)
			}
			if tt.wantOK && (len(health) != 1 || health[0].Provider != "sqlite" || !health[0].OK) {
				t.Fatalf("unexpected health: %#v", health)
			}
		})
	}
}

func TestConfigFileTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "explicit path", path: filepath.Join(home, "paxm.yaml"), want: filepath.Join(home, "paxm.yaml")},
		{name: "home path expands", path: "~/paxm.yaml", want: filepath.Join(home, "paxm.yaml")},
		{name: "default path", path: "", want: filepath.Join(home, ".config", "paxm", "config.yaml")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConfigFile(tt.path); got != tt.want {
				t.Fatalf("ConfigFile(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTelemetryProfileAndRouteHelpersTable(t *testing.T) {
	cfg := telemetryTestConfig()

	t.Run("effective recall profile", func(t *testing.T) {
		tests := []struct {
			name    string
			profile string
			cfg     config.Config
			want    string
		}{
			{name: "explicit profile wins", profile: "custom", cfg: cfg, want: "custom"},
			{name: "codex active profile is default fallback", cfg: cfg, want: "codex-active"},
			{name: "missing codex active profile falls back to default", cfg: config.Config{}, want: "default"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := EffectiveRecallProfile(tt.cfg, tt.profile); got != tt.want {
					t.Fatalf("EffectiveRecallProfile() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("provider route counting", func(t *testing.T) {
		tests := []struct {
			name string
			got  map[string]int
			want map[string]int
		}{
			{name: "recall skipped suppresses routes", got: RecallProviderRoutes(cfg, "custom", true), want: nil},
			{name: "recall explicit profile counts duplicates", got: RecallProviderRoutes(cfg, "custom", false), want: map[string]int{"sqlite": 2, "zep": 1}},
			{name: "recall missing profile is nil", got: RecallProviderRoutes(cfg, "missing", false), want: nil},
			{name: "write blank profile defaults", got: WriteProviderRoutes(cfg, ""), want: map[string]int{"sqlite": 1}},
			{name: "write explicit profile", got: WriteProviderRoutes(cfg, "archive"), want: map[string]int{"mem0": 1}},
			{name: "write missing profile is nil", got: WriteProviderRoutes(cfg, "missing"), want: nil},
			{
				name: "write routes aggregate items",
				got: WriteProviderRoutesForItems(cfg, []facade.IngestInput{
					{Text: "one"},
					{Text: "two", Profile: "archive"},
					{Text: "three", Profile: "archive"},
					{Text: "unknown", Profile: "missing"},
				}),
				want: map[string]int{"sqlite": 1, "mem0": 2},
			},
			{name: "write routes for empty items is nil", got: WriteProviderRoutesForItems(cfg, nil), want: nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if !reflect.DeepEqual(tt.got, tt.want) {
					t.Fatalf("routes = %#v, want %#v", tt.got, tt.want)
				}
			})
		}
	})
}

func TestTelemetryEventFieldsTable(t *testing.T) {
	cfg := telemetryTestConfig()
	rawScore := 0.42
	longErr := errors.New(strings.Repeat("x", 300))

	tests := []struct {
		name      string
		event     func() telemetryEventView
		want      telemetryEventView
		wantError int
	}{
		{
			name: "recall",
			event: func() telemetryEventView {
				event := RecallTelemetryEvent(cfg, RecallTelemetryInput{
					Kind:      "hook_recall",
					Source:    "hook",
					Target:    "codex",
					HookEvent: "user_input",
					Profile:   "custom",
					Duration:  1500 * time.Millisecond,
					Err:       longErr,
					Result: facade.RecallResult{
						Hits: []memory.MemoryHit{{
							Provider:     "sqlite",
							ID:           "hit-1",
							RawScore:     &rawScore,
							RawScoreKind: "distance",
						}},
						ProviderErrors: []memory.ProviderError{{Provider: "zep", Error: "offline"}},
					},
				})
				return telemetryEventView{
					Kind:             event.Kind,
					Source:           event.Source,
					Command:          event.Command,
					Profile:          event.Profile,
					Success:          event.Success,
					DurationMS:       event.DurationMS,
					HitCount:         event.HitCount,
					InsertedCount:    event.InsertedCount,
					ProviderRecalls:  event.ProviderRecalls,
					ProviderHits:     event.ProviderHits,
					ProviderErrItems: len(event.ProviderErrorDetails),
					Error:            event.Error,
				}
			},
			want: telemetryEventView{
				Kind:             "hook_recall",
				Source:           "hook",
				Command:          "hook",
				Profile:          "custom",
				DurationMS:       1500,
				HitCount:         1,
				InsertedCount:    1,
				ProviderRecalls:  map[string]int{"sqlite": 2, "zep": 1},
				ProviderHits:     map[string]int{"sqlite": 1},
				ProviderErrItems: 1,
			},
			wantError: 240,
		},
		{
			name: "remember",
			event: func() telemetryEventView {
				event := RememberTelemetryEvent(cfg, RememberTelemetryInput{
					Kind:      "remember",
					Source:    "cli",
					Profile:   "archive",
					ItemCount: 2,
					Duration:  2 * time.Second,
					Result: facade.IngestResult{
						Refs: []memory.MemoryRef{{Provider: "mem0", ID: "ref-1"}},
					},
				})
				return telemetryEventView{
					Kind:           event.Kind,
					Source:         event.Source,
					Command:        event.Command,
					Profile:        event.Profile,
					Success:        event.Success,
					DurationMS:     event.DurationMS,
					ItemCount:      event.ItemCount,
					RefCount:       event.RefCount,
					ProviderWrites: event.ProviderWrites,
					ProviderRefs:   event.ProviderRefs,
					Error:          event.Error,
				}
			},
			want: telemetryEventView{
				Kind:           "remember",
				Source:         "cli",
				Command:        "remember",
				Profile:        "archive",
				Success:        true,
				DurationMS:     2000,
				ItemCount:      2,
				RefCount:       1,
				ProviderWrites: map[string]int{"mem0": 1},
				ProviderRefs:   map[string]int{"mem0": 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.event()
			if tt.wantError > 0 {
				if len(got.Error) != tt.wantError {
					t.Fatalf("error length = %d, want %d", len(got.Error), tt.wantError)
				}
				got.Error = ""
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("event = %#v, want %#v", got, tt.want)
			}
		})
	}
}

type telemetryEventView struct {
	Kind             string
	Source           string
	Command          string
	Profile          string
	Success          bool
	DurationMS       int64
	HitCount         int
	InsertedCount    int
	ItemCount        int
	RefCount         int
	ProviderRecalls  map[string]int
	ProviderWrites   map[string]int
	ProviderHits     map[string]int
	ProviderRefs     map[string]int
	ProviderErrItems int
	Error            string
}

func telemetryTestConfig() config.Config {
	return config.Config{
		RecallProfiles: map[string]config.RecallProfileConfig{
			"codex-active": {
				Providers: []config.ProviderRouteConfig{{Name: "sqlite"}},
			},
			"custom": {
				Providers: []config.ProviderRouteConfig{
					{Name: "sqlite"},
					{Name: ""},
					{Name: "zep"},
					{Name: "sqlite"},
				},
			},
		},
		WriteProfiles: map[string]config.WriteProfileConfig{
			"default": {Providers: []config.ProviderRouteConfig{{Name: "sqlite"}}},
			"archive": {Providers: []config.ProviderRouteConfig{{Name: "mem0"}}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				ActiveRecall: config.ActiveRecallConfig{Enabled: true, Profile: "codex-active"},
			},
		},
	}
}
