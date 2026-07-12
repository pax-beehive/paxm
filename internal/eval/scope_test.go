package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type deletingEvalProvider struct{ deleted []string }

func (p *deletingEvalProvider) Name() string { return "primary" }
func (p *deletingEvalProvider) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return nil, nil
}
func (p *deletingEvalProvider) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	return memory.MemoryRef{}, nil
}
func (p *deletingEvalProvider) Health(context.Context) error { return nil }
func (p *deletingEvalProvider) Delete(_ context.Context, ref memory.MemoryRef) error {
	p.deleted = append(p.deleted, ref.ID)
	return nil
}

func TestPrepareProviderScopeIsolatesMem0AndPersistsManifest(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["primary"] = config.ProviderConfig{
		Type:    "mem0",
		Enabled: true,
		BaseURL: "http://localhost:8888",
		UserID:  "real-user",
	}

	scope, err := PrepareProviderScope(cfg, "primary", ScopeOptions{
		RunID:       "locomo-run-1",
		ManifestDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := scope.Config.Providers["primary"]
	if provider.UserID != "real-user" || provider.RunID != "paxm-eval-locomo-run-1" || provider.Infer == nil || *provider.Infer {
		t.Fatalf("isolated mem0 provider = %#v", provider)
	}
	if len(scope.Config.Providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(scope.Config.Providers))
	}
	if err := scope.RecordRefs([]memory.MemoryRef{{Provider: "primary", ID: "memory-1"}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEvalManifest(scope.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != EvalStatusRunning || len(loaded.Refs) != 1 || loaded.Refs[0].ID != "memory-1" {
		t.Fatalf("manifest = %#v", loaded)
	}
	if _, err := os.Stat(scope.ManifestPath); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareProviderScopeIsolatesMem0Cloud(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["cloud"] = config.ProviderConfig{Type: "mem0-cloud", Enabled: true, APIKey: "test-key", UserID: "user"}
	scope, err := PrepareProviderScope(cfg, "cloud", ScopeOptions{RunID: "cloud-run", ManifestDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	provider := scope.Config.Providers["cloud"]
	if provider.UserID != "" || provider.AgentID != "" || provider.RunID != "paxm-eval-cloud-run" || provider.Infer == nil || *provider.Infer {
		t.Fatalf("isolated mem0 cloud provider = %#v", provider)
	}
}

func TestPrepareProviderScopeUsesDisposableSQLitePath(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	scope, err := PrepareProviderScope(cfg, "sqlite", ScopeOptions{RunID: "run-2", ManifestDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := scope.Config.Providers["sqlite"].Path
	want := filepath.Join(dir, "run-2", "memory.sqlite")
	if got != want {
		t.Fatalf("sqlite path = %q, want %q", got, want)
	}
}

func TestPrepareProviderScopeDoesNotMutateSourceConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["rpc"] = config.ProviderConfig{Type: "jsonrpc", Enabled: true, Command: "provider", Env: map[string]string{"EXISTING": "value"}}
	_, err := PrepareProviderScope(cfg, "rpc", ScopeOptions{RunID: "run-copy", ManifestDir: dir, KeepMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["rpc"].Env["PAXM_EVAL_SCOPE"] != "" {
		t.Fatal("source provider env was mutated")
	}
	if got := cfg.RecallProfiles["default"].Providers[0].Name; got != "sqlite" {
		t.Fatalf("source recall profile provider = %q, want sqlite", got)
	}
}

func TestPrepareProviderScopeRejectsJSONRPCWithoutCleanup(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["rpc"] = config.ProviderConfig{Type: "jsonrpc", Enabled: true, Command: "provider"}
	_, err := PrepareProviderScope(cfg, "rpc", ScopeOptions{RunID: "run-3", ManifestDir: dir})
	if err == nil {
		t.Fatal("expected unsafe provider scope to be rejected")
	}
}

func TestPrepareProviderScopeRequiresKeepMemoryForMemOS(t *testing.T) {
	for _, providerType := range []string{"memos", "memos-cloud"} {
		t.Run(providerType, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
			cfg.Providers["mos"] = config.ProviderConfig{Type: providerType, Enabled: true, UserID: "real-user"}
			if _, err := PrepareProviderScope(cfg, "mos", ScopeOptions{RunID: "unsafe", ManifestDir: dir}); err == nil {
				t.Fatal("expected cleanup safety rejection")
			}
			scope, err := PrepareProviderScope(cfg, "mos", ScopeOptions{RunID: "kept", ManifestDir: dir, KeepMemory: true})
			if err != nil {
				t.Fatal(err)
			}
			if got := scope.Config.Providers["mos"].UserID; got != "paxm-eval-kept" {
				t.Fatalf("isolated user_id = %q", got)
			}
			restored, err := RestoreProviderScope(cfg, scope.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := restored.Config.Providers["mos"].UserID; got != "paxm-eval-kept" {
				t.Fatalf("restored user_id = %q", got)
			}
		})
	}
}

func TestPrepareProviderScopeRejectsDuplicateRunID(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	if _, err := PrepareProviderScope(cfg, "sqlite", ScopeOptions{RunID: "duplicate", ManifestDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareProviderScope(cfg, "sqlite", ScopeOptions{RunID: "duplicate", ManifestDir: dir}); err == nil {
		t.Fatal("expected duplicate run id to be rejected")
	}
}

func TestCleanupProviderScopeDeletesManifestRefsAndMarksCleaned(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig(filepath.Join(dir, "config.yaml"))
	cfg.Providers["primary"] = config.ProviderConfig{Type: "mem0", Enabled: true, UserID: "user"}
	scope, err := PrepareProviderScope(cfg, "primary", ScopeOptions{RunID: "cleanup-1", ManifestDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.RecordRefs([]memory.MemoryRef{{Provider: "primary", ID: "one"}, {Provider: "primary", ID: "two"}}); err != nil {
		t.Fatal(err)
	}
	provider := &deletingEvalProvider{}
	if err := CleanupProviderScope(context.Background(), scope, provider); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(provider.deleted, ","); got != "two,one" {
		t.Fatalf("deleted = %q, want reverse creation order", got)
	}
	manifest, err := LoadEvalManifest(scope.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != EvalStatusCleaned {
		t.Fatalf("status = %q, want cleaned", manifest.Status)
	}
}
