package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

const (
	EvalStatusRunning  = "running"
	EvalStatusComplete = "complete"
	EvalStatusCleaned  = "cleaned"
	EvalStatusFailed   = "failed"
)

type ScopeOptions struct {
	RunID       string
	ManifestDir string
	KeepMemory  bool
}

type EvalManifest struct {
	Version      int                `json:"version"`
	RunID        string             `json:"run_id"`
	Provider     string             `json:"provider"`
	ProviderType string             `json:"provider_type"`
	RemoteScope  string             `json:"remote_scope,omitempty"`
	Status       string             `json:"status"`
	KeepMemory   bool               `json:"keep_memory,omitempty"`
	Refs         []memory.MemoryRef `json:"refs,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	CleanupError string             `json:"cleanup_error,omitempty"`
	ResultPath   string             `json:"result_path,omitempty"`
}

type ProviderScope struct {
	Config       config.Config
	Manifest     EvalManifest
	ManifestPath string
}

func PrepareProviderScope(cfg config.Config, providerName string, opts ScopeOptions) (*ProviderScope, error) {
	runID := sanitizeScopeID(opts.RunID)
	if runID == "" {
		return nil, errors.New("eval run id is required")
	}
	manifestDir := strings.TrimSpace(opts.ManifestDir)
	if manifestDir == "" {
		return nil, errors.New("eval manifest directory is required")
	}
	runDir := filepath.Join(manifestDir, runID)
	if _, err := os.Stat(runDir); err == nil {
		return nil, fmt.Errorf("eval run %q already exists; clean it or choose a new run id", runID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	provider, ok := cfg.Providers[providerName]
	if !ok || !provider.Enabled {
		return nil, fmt.Errorf("eval provider %q is not enabled", providerName)
	}
	remoteScope := "paxm-eval-" + runID
	provider.Env = copyStringMap(provider.Env)
	switch provider.Type {
	case "sqlite":
		provider.Path = filepath.Join(manifestDir, runID, "memory.sqlite")
	case "mem0":
		provider.RunID = remoteScope
		infer := false
		provider.Infer = &infer
	case "zep":
		provider.UserID = ""
		provider.GraphID = remoteScope
		provider.SearchScope = "episodes"
	case "jsonrpc":
		if !opts.KeepMemory {
			return nil, errors.New("jsonrpc provider does not advertise reliable eval cleanup; rerun with --keep-memory only if intentional")
		}
		if provider.Env == nil {
			provider.Env = make(map[string]string)
		}
		provider.Env["PAXM_EVAL_SCOPE"] = remoteScope
	default:
		return nil, fmt.Errorf("provider type %q does not support eval isolation", provider.Type)
	}

	isolated := cloneEvalConfig(cfg)
	isolated.Providers = map[string]config.ProviderConfig{providerName: provider}
	restrictProfiles(&isolated, providerName)
	now := time.Now().UTC()
	manifestPath := filepath.Join(runDir, "manifest.json")
	scope := &ProviderScope{
		Config: isolated,
		Manifest: EvalManifest{
			Version:      1,
			RunID:        runID,
			Provider:     providerName,
			ProviderType: provider.Type,
			RemoteScope:  remoteScope,
			Status:       EvalStatusRunning,
			KeepMemory:   opts.KeepMemory,
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		ManifestPath: manifestPath,
	}
	if err := scope.saveManifest(); err != nil {
		return nil, err
	}
	return scope, nil
}

func RestoreProviderScope(cfg config.Config, manifestPath string) (*ProviderScope, error) {
	manifest, err := LoadEvalManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	provider, ok := cfg.Providers[manifest.Provider]
	if !ok || !provider.Enabled {
		return nil, fmt.Errorf("eval provider %q is not enabled", manifest.Provider)
	}
	if provider.Type != manifest.ProviderType {
		return nil, fmt.Errorf("eval provider %q type changed from %q to %q; refusing cleanup", manifest.Provider, manifest.ProviderType, provider.Type)
	}
	provider.Env = copyStringMap(provider.Env)
	switch manifest.ProviderType {
	case "sqlite":
		provider.Path = filepath.Join(filepath.Dir(manifestPath), "memory.sqlite")
	case "mem0":
		provider.RunID = manifest.RemoteScope
	case "zep":
		provider.UserID = ""
		provider.GraphID = manifest.RemoteScope
	case "jsonrpc":
		if provider.Env == nil {
			provider.Env = make(map[string]string)
		}
		provider.Env["PAXM_EVAL_SCOPE"] = manifest.RemoteScope
	default:
		return nil, fmt.Errorf("provider type %q does not support eval cleanup", manifest.ProviderType)
	}
	isolated := cloneEvalConfig(cfg)
	isolated.Providers = map[string]config.ProviderConfig{manifest.Provider: provider}
	restrictProfiles(&isolated, manifest.Provider)
	return &ProviderScope{Config: isolated, Manifest: manifest, ManifestPath: manifestPath}, nil
}

func (s *ProviderScope) RecordRefs(refs []memory.MemoryRef) error {
	if s == nil || len(refs) == 0 {
		return nil
	}
	s.Manifest.Refs = append(s.Manifest.Refs, refs...)
	return s.saveManifest()
}

func (s *ProviderScope) SetStatus(status string, cleanupErr error) error {
	if s == nil {
		return errors.New("eval provider scope is nil")
	}
	s.Manifest.Status = status
	if cleanupErr != nil {
		s.Manifest.CleanupError = cleanupErr.Error()
	} else {
		s.Manifest.CleanupError = ""
	}
	return s.saveManifest()
}

func CleanupProviderScope(ctx context.Context, scope *ProviderScope, provider memory.Provider) error {
	if scope == nil {
		return errors.New("eval provider scope is nil")
	}
	if scope.Manifest.KeepMemory {
		return scope.SetStatus(EvalStatusComplete, nil)
	}
	var cleanupErr error
	if scope.Manifest.ProviderType == "sqlite" {
		cleanupErr = os.RemoveAll(filepath.Dir(scope.ManifestPath))
		if cleanupErr == nil {
			// The manifest lives inside the directory being removed, so there is
			// intentionally no cleaned marker for disposable SQLite scopes.
			return nil
		}
	} else if cleaner, ok := provider.(memory.EvalScopeCleaner); ok {
		cleanupErr = cleaner.CleanupEvalScope(ctx)
	} else if deleter, ok := provider.(memory.DeleteProvider); ok {
		for i := len(scope.Manifest.Refs) - 1; i >= 0; i-- {
			if err := deleter.Delete(ctx, scope.Manifest.Refs[i]); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	} else if len(scope.Manifest.Refs) > 0 {
		cleanupErr = fmt.Errorf("provider %q cannot clean eval writes", scope.Manifest.Provider)
	}
	if cleanupErr != nil {
		_ = scope.SetStatus(EvalStatusFailed, cleanupErr)
		return cleanupErr
	}
	return scope.SetStatus(EvalStatusCleaned, nil)
}

func (s *ProviderScope) saveManifest() error {
	s.Manifest.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(s.Manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.ManifestPath), 0o700); err != nil {
		return err
	}
	tmp := s.ManifestPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.ManifestPath)
}

func LoadEvalManifest(path string) (EvalManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EvalManifest{}, err
	}
	var manifest EvalManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return EvalManifest{}, fmt.Errorf("decode eval manifest: %w", err)
	}
	return manifest, nil
}

func restrictProfiles(cfg *config.Config, providerName string) {
	for name, profile := range cfg.RecallProfiles {
		profile.Providers = []config.ProviderRouteConfig{{Name: providerName, Required: true, Weight: 1}}
		cfg.RecallProfiles[name] = profile
	}
	for name, profile := range cfg.WriteProfiles {
		profile.Providers = []config.ProviderRouteConfig{{Name: providerName, Required: true, Weight: 1}}
		cfg.WriteProfiles[name] = profile
	}
}

func cloneEvalConfig(cfg config.Config) config.Config {
	cloned := cfg
	cloned.RecallProfiles = make(map[string]config.RecallProfileConfig, len(cfg.RecallProfiles))
	for name, profile := range cfg.RecallProfiles {
		profile.Providers = append([]config.ProviderRouteConfig(nil), profile.Providers...)
		cloned.RecallProfiles[name] = profile
	}
	cloned.WriteProfiles = make(map[string]config.WriteProfileConfig, len(cfg.WriteProfiles))
	for name, profile := range cfg.WriteProfiles {
		profile.Providers = append([]config.ProviderRouteConfig(nil), profile.Providers...)
		cloned.WriteProfiles[name] = profile
	}
	return cloned
}

func copyStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func sanitizeScopeID(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
