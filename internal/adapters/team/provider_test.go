package team

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/paxlclient"
)

type provisionerStub struct {
	calls      int
	provisions []paxlclient.AgentProvision
	err        error
}

type provisionerFunc func(context.Context, string) (paxlclient.AgentProvision, error)

func (f provisionerFunc) ProvisionAgent(ctx context.Context, agentID string) (paxlclient.AgentProvision, error) {
	return f(ctx, agentID)
}

type typedProvisionCall struct {
	agentID   string
	agentType string
}

type typedProvisionerStub struct {
	calls []typedProvisionCall
}

func (p *typedProvisionerStub) ProvisionAgent(
	_ context.Context,
	agentID string,
) (paxlclient.AgentProvision, error) {
	return p.provision(agentID, "")
}

func (p *typedProvisionerStub) ProvisionAgentWithType(
	_ context.Context,
	agentID string,
	agentType string,
) (paxlclient.AgentProvision, error) {
	return p.provision(agentID, agentType)
}

func (p *typedProvisionerStub) provision(
	agentID string,
	agentType string,
) (paxlclient.AgentProvision, error) {
	p.calls = append(p.calls, typedProvisionCall{agentID: agentID, agentType: agentType})
	return paxlclient.AgentProvision{
		URL: "https://memory.internal", APIKey: "tm_key_" + agentID,
		AgentID: agentID, UserID: "usr-1", CredentialID: agentID + "-fresh",
	}, nil
}

type blockingProvisioner struct {
	mu        sync.Mutex
	calls     int
	provision paxlclient.AgentProvision
	err       error
	started   chan struct{}
	release   <-chan struct{}
	once      sync.Once
}

func (p *blockingProvisioner) ProvisionAgent(_ context.Context, _ string) (paxlclient.AgentProvision, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.once.Do(func() { close(p.started) })
	<-p.release
	return p.provision, p.err
}

func (p *blockingProvisioner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *provisionerStub) ProvisionAgent(_ context.Context, _ string) (paxlclient.AgentProvision, error) {
	p.calls++
	if p.err != nil {
		return paxlclient.AgentProvision{}, p.err
	}
	provision := p.provisions[0]
	p.provisions = p.provisions[1:]
	return provision, nil
}

func TestProviderDiscoversAndCachesPaxlDeviceCredential(t *testing.T) {
	cacheDir := t.TempDir()
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_discovered",
		AgentID: "paxm-todd", UserID: "usr-1", CredentialID: "cred-1",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_USER_ID":              "stale-user",
			"TEAM_MEMORY_BASE_URL":      "https://stale.invalid",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_discovered",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "device credentials"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Provider != "team" || hits[0].ID != "hit-1" {
		t.Fatalf("hits = %#v", hits)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want 1", provisioner.calls)
	}
	cacheFiles, err := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cacheFiles) != 1 {
		t.Fatalf("cache files = %#v", cacheFiles)
	}
	info, err := os.Stat(cacheFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %o, want 600", info.Mode().Perm())
	}
}

func TestProviderPreservesExplicitAPIKeyWithoutProvisioning(t *testing.T) {
	provisioner := &provisionerStub{err: context.Canceled}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"TEAM_MEMORY_API_KEY":       "tm_key_explicit",
			"TEAM_MEMORY_BASE_URL":      "https://memory.internal",
			"PAXM_USER_ID":              "usr-1",
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_explicit",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "explicit"}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 0 {
		t.Fatalf("provision calls = %d, want 0", provisioner.calls)
	}
}

func TestProviderExplicitAPIKeyKeepsSingleBatchForMixedOrigins(t *testing.T) {
	cacheDir := t.TempDir()
	provisioner := &provisionerStub{err: errors.New("must not provision")}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"TEAM_MEMORY_API_KEY":       "tm_key_explicit",
			"TEAM_MEMORY_BASE_URL":      "https://memory.internal",
			"PAXM_USER_ID":              "usr-1",
			"PAXM_AGENT_ID":             "personal-codex",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "explicit-route",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := provider.PutBatch(context.Background(), []memory.MemoryItem{
		{ID: "codex-1", Text: "codex", Origin: memory.MemoryOrigin{AgentID: "personal-codex"}},
		{ID: "claude-1", Text: "claude", Origin: memory.MemoryOrigin{AgentID: "personal-claude"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %#v, want both items in explicit-key batch", refs)
	}
	if provisioner.calls != 0 {
		t.Fatalf("provision calls = %d, want 0", provisioner.calls)
	}
	cacheFiles, err := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cacheFiles) != 0 {
		t.Fatalf("explicit key wrote credential caches: %#v", cacheFiles)
	}
}

func TestProviderRoutesPutBatchByOriginAgentCredential(t *testing.T) {
	cacheDir := t.TempDir()
	var provisionedAgents []string
	provisioner := provisionerFunc(func(_ context.Context, agentID string) (paxlclient.AgentProvision, error) {
		provisionedAgents = append(provisionedAgents, agentID)
		return paxlclient.AgentProvision{
			URL: "https://memory.internal", APIKey: "tm_key_" + agentID,
			AgentID: agentID, UserID: "usr-1",
		}, nil
	})
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "personal-codex",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "route",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	refs, err := provider.PutBatch(context.Background(), []memory.MemoryItem{
		{
			ID: "codex-1", Text: "codex first", Source: "hook:episode",
			Metadata: map[string]string{"hook_target": "codex"},
			Origin:   memory.MemoryOrigin{AgentID: "personal-codex"},
		},
		{
			ID: "claude-1", Text: "claude", Source: "hook:episode",
			Metadata: map[string]string{"hook_target": "claude"},
			Origin:   memory.MemoryOrigin{AgentID: "personal-claude"},
		},
		{
			ID: "codex-2", Text: "codex second", Source: "hook:episode",
			Metadata: map[string]string{"hook_target": "codex"},
			Origin:   memory.MemoryOrigin{AgentID: "personal-codex"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs = %#v, want one per item", refs)
	}
	if refs[0].ID != "codex-1" || refs[1].ID != "claude-1" || refs[2].ID != "codex-2" {
		t.Fatalf("refs lost input order: %#v", refs)
	}
	if got := strings.Join(provisionedAgents, ","); got != "personal-codex,personal-claude" {
		t.Fatalf("provisioned agents = %q, want independent credentials", got)
	}
	for _, agentID := range []string{"personal-codex", "personal-claude"} {
		if _, err := os.Stat(filepath.Join(cacheDir, "team-"+agentID+".json")); err != nil {
			t.Fatalf("credential cache for %s: %v", agentID, err)
		}
	}
}

func TestProviderPassesCaptureAgentTypeForDefaultAgentID(t *testing.T) {
	cacheDir := t.TempDir()
	writeCachedCredential(t, cacheDir, cachedCredential{
		URL: "https://memory.internal", APIKey: "tm_key_paxm-todd-codex",
		AgentID: "paxm-todd-codex", UserID: "usr-1",
	})
	provisioner := &typedProvisionerStub{}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd-codex",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "route",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.PutBatch(context.Background(), []memory.MemoryItem{{
		ID: "claude-1", Text: "claude capture", Source: "hook:episode",
		Metadata: map[string]string{"hook_target": "claude"},
		Origin:   memory.MemoryOrigin{AgentID: "claude-todd"},
	}}); err != nil {
		t.Fatal(err)
	}
	want := []typedProvisionCall{{agentID: "claude-todd", agentType: "claude"}}
	if !reflect.DeepEqual(provisioner.calls, want) {
		t.Fatalf("provision calls = %#v, want %#v", provisioner.calls, want)
	}
}

func TestProviderMissingOriginFallsBackToDefaultCredential(t *testing.T) {
	cacheDir := t.TempDir()
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_personal-codex",
		AgentID: "personal-codex", UserID: "usr-1",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "personal-codex",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "route-fallback",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PutBatch(context.Background(), []memory.MemoryItem{{
		ID: "legacy-1", Text: "legacy item without origin",
	}}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want only default credential", provisioner.calls)
	}
}

func TestProviderReprovisionsOnlyRejectedOriginAgentCredential(t *testing.T) {
	cacheDir := t.TempDir()
	writeCachedCredential(t, cacheDir, cachedCredential{
		URL: "https://memory.internal", APIKey: "tm_key_paxm-todd-codex",
		AgentID: "paxm-todd-codex", UserID: "usr-1", CredentialID: "recall-stable",
	})
	writeCachedCredential(t, cacheDir, cachedCredential{
		URL: "https://memory.internal", APIKey: "tm_key_codex-todd",
		AgentID: "codex-todd", UserID: "usr-1", CredentialID: "codex-stable",
	})
	writeCachedCredential(t, cacheDir, cachedCredential{
		URL: "https://memory.internal", APIKey: "tm_key_stale",
		AgentID: "claude-todd", UserID: "usr-1", CredentialID: "claude-stale",
	})
	provisioner := &typedProvisionerStub{}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd-codex",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "route-rotate",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	_, err = provider.PutBatch(context.Background(), []memory.MemoryItem{
		{
			ID: "codex-1", Text: "codex", Source: "hook:episode",
			Metadata: map[string]string{"hook_target": "codex"},
			Origin:   memory.MemoryOrigin{AgentID: "codex-todd"},
		},
		{
			ID: "claude-1", Text: "claude", Source: "hook:episode",
			Metadata: map[string]string{"hook_target": "claude"},
			Origin:   memory.MemoryOrigin{AgentID: "claude-todd"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []typedProvisionCall{{agentID: "claude-todd", agentType: "claude"}}
	if !reflect.DeepEqual(provisioner.calls, wantCalls) {
		t.Fatalf("provision calls = %#v, want only rejected claude agent", provisioner.calls)
	}
	codex := readCachedCredential(t, filepath.Join(cacheDir, "team-codex-todd.json"))
	if codex.CredentialID != "codex-stable" {
		t.Fatalf("codex credential changed to %#v", codex)
	}
	claude := readCachedCredential(t, filepath.Join(cacheDir, "team-claude-todd.json"))
	if claude.CredentialID != "claude-todd-fresh" {
		t.Fatalf("claude credential = %#v, want refreshed", claude)
	}
}

func TestProviderPreservesProcessAPIKeyWithoutProvisioning(t *testing.T) {
	t.Setenv("TEAM_MEMORY_API_KEY", "tm_key_process")
	provisioner := &provisionerStub{err: context.Canceled}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"TEAM_MEMORY_BASE_URL":      "https://memory.internal",
			"PAXM_USER_ID":              "usr-1",
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_process",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "process key"}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 0 {
		t.Fatalf("provision calls = %d, want 0", provisioner.calls)
	}
}

func TestProviderReusesCachedCredential(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(cacheDir, "team-paxm-todd.json"),
		[]byte(`{"url":"https://memory.internal","api_key":"tm_key_discovered","agent_id":"paxm-todd","user_id":"usr-1"}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	provisioner := &provisionerStub{err: errors.New("must not provision")}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_discovered",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "cached"}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 0 {
		t.Fatalf("provision calls = %d, want 0", provisioner.calls)
	}
	info, err := os.Stat(filepath.Join(cacheDir, "team-paxm-todd.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %o, want 600", info.Mode().Perm())
	}
}

func TestProviderReplacesCredentialCachedForDifferentAgent(t *testing.T) {
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "team-personal-claude.json")
	if err := os.WriteFile(
		cachePath,
		[]byte(`{
			"url":"https://memory.internal",
			"api_key":"tm_key_personal-codex",
			"agent_id":"personal-codex",
			"user_id":"usr-1"
		}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_personal-claude",
		AgentID: "personal-claude", UserID: "usr-1",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "personal-claude",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "route",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.PutBatch(context.Background(), []memory.MemoryItem{{
		ID: "claude-1", Text: "claude",
		Origin: memory.MemoryOrigin{AgentID: "personal-claude"},
	}}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want mismatched cache replaced", provisioner.calls)
	}
	credential := readCachedCredential(t, cachePath)
	if credential.AgentID != "personal-claude" {
		t.Fatalf("cached credential agent = %q, want personal-claude", credential.AgentID)
	}
}

func TestProviderRejectsProvisionedCredentialForDifferentAgent(t *testing.T) {
	cacheDir := t.TempDir()
	_, err := New("team", config.ProviderConfig{
		Type:      "team-memory",
		Transport: "stdio",
		Command:   "paxm-team-memory-provider",
		Env:       map[string]string{"PAXM_AGENT_ID": "personal-claude"},
	}, Dependencies{
		CredentialDir: cacheDir,
		Provisioner: &provisionerStub{provisions: []paxlclient.AgentProvision{{
			URL: "https://memory.internal", APIKey: "tm_key_personal-codex",
			AgentID: "personal-codex", UserID: "usr-1",
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), `want "personal-claude"`) {
		t.Fatalf("error = %v, want credential principal mismatch", err)
	}
	cacheFiles, globErr := filepath.Glob(filepath.Join(cacheDir, "*.json"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(cacheFiles) != 0 {
		t.Fatalf("mismatched credential was cached: %#v", cacheFiles)
	}
}

func TestReplaceCredentialFileRestoresExistingCacheAfterInstallFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team-paxm-todd.json")
	tempPath := filepath.Join(dir, "new.tmp")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	rename := func(oldPath, newPath string) error {
		calls++
		switch calls {
		case 1:
			return errors.New("destination exists")
		case 3:
			return errors.New("install failed")
		default:
			return os.Rename(oldPath, newPath)
		}
	}

	err := replaceCredentialFile(tempPath, path, rename)
	if err == nil || !strings.Contains(err.Error(), "install replacement") {
		t.Fatalf("error = %v, want replacement failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("credential cache = %q, want old value restored", got)
	}
}

func TestProviderGuidesDeviceConnectWhenProvisioningUnavailable(t *testing.T) {
	_, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: "paxm-team-memory-provider",
		Env: map[string]string{"PAXM_AGENT_ID": "paxm-todd"},
	}, Dependencies{
		Provisioner:   &provisionerStub{err: errors.New("paxl executable not found")},
		CredentialDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "paxl device connect onprem") {
		t.Fatalf("error = %v, want device connect guidance", err)
	}
}

func TestProviderBoundsInitialProvisioning(t *testing.T) {
	started := time.Now()
	_, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: "paxm-team-memory-provider",
		Env: map[string]string{"PAXM_AGENT_ID": "paxm-todd"},
	}, Dependencies{
		Provisioner: provisionerFunc(func(ctx context.Context, _ string) (paxlclient.AgentProvision, error) {
			<-ctx.Done()
			return paxlclient.AgentProvision{}, ctx.Err()
		}),
		CredentialDir:    t.TempDir(),
		ProvisionTimeout: 20 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded provision took %s", elapsed)
	}
}

func TestProviderReplacesIncompleteCachedCredential(t *testing.T) {
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "team-paxm-todd.json")
	if err := os.WriteFile(cachePath, []byte(`{"api_key":"tm_key_incomplete"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_discovered",
		AgentID: "paxm-todd", UserID: "usr-1",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_discovered",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "recovered"}); err != nil {
		t.Fatal(err)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want 1", provisioner.calls)
	}
}

func TestProviderReprovisionsOnceAfterUnauthorized(t *testing.T) {
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "team-paxm-todd.json")
	if err := os.WriteFile(cachePath, []byte(`{
		"url":"https://memory.internal",
		"api_key":"tm_key_stale",
		"agent_id":"paxm-todd",
		"user_id":"usr-1",
		"credential_id":"cred-stale"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_fresh",
		AgentID: "paxm-todd", UserID: "usr-1", CredentialID: "cred-fresh",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "rotate",
			"PAXM_TEAM_EXPECTED_KEY":    "tm_key_fresh",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	hits, err := provider.Search(context.Background(), memory.SearchQuery{Text: "rotated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "hit-1" {
		t.Fatalf("hits = %#v", hits)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want 1", provisioner.calls)
	}
	cached, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(cached) || !containsJSONValue(cached, "api_key", "tm_key_fresh") {
		t.Fatalf("credential cache was not refreshed")
	}
}

func TestProviderReturnsSecondUnauthorizedWithoutRetryLoop(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(cacheDir, "team-paxm-todd.json"),
		[]byte(`{"url":"https://memory.internal","api_key":"tm_key_stale","agent_id":"paxm-todd","user_id":"usr-1"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	provisioner := &provisionerStub{provisions: []paxlclient.AgentProvision{{
		URL: "https://memory.internal", APIKey: "tm_key_fresh",
		AgentID: "paxm-todd", UserID: "usr-1",
	}}}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":             "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER": "1",
			"PAXM_TEAM_HELPER_MODE":     "always-unauthorized",
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Search(context.Background(), memory.SearchQuery{Text: "still rejected"}); err == nil {
		t.Fatal("expected second unauthorized error")
	}
	if provisioner.calls != 1 {
		t.Fatalf("provision calls = %d, want exactly 1", provisioner.calls)
	}
}

func TestProviderConcurrentUnauthorizedReprovisionsOnce(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(cacheDir, "team-paxm-todd.json"),
		[]byte(`{"url":"https://memory.internal","api_key":"tm_key_stale","agent_id":"paxm-todd","user_id":"usr-1"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	signalPath := filepath.Join(t.TempDir(), "unauthorized.log")
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	provisioner := &blockingProvisioner{
		provision: paxlclient.AgentProvision{
			URL: "https://memory.internal", APIKey: "tm_key_fresh",
			AgentID: "paxm-todd", UserID: "usr-1",
		},
		started: make(chan struct{}),
		release: release,
	}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":                      "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER":          "1",
			"PAXM_TEAM_HELPER_MODE":              "rotate",
			"PAXM_TEAM_EXPECTED_KEY":             "tm_key_fresh",
			"PAXM_TEAM_UNAUTHORIZED_SIGNAL_FILE": signalPath,
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, searchErr := provider.Search(context.Background(), memory.SearchQuery{Text: "concurrent"})
			results <- searchErr
		}()
	}
	close(start)
	select {
	case <-provisioner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh did not start")
	}
	waitForSignalLines(t, signalPath, 2)
	close(release)
	released = true
	for range 2 {
		if searchErr := <-results; searchErr != nil {
			t.Fatal(searchErr)
		}
	}
	if got := provisioner.callCount(); got != 1 {
		t.Fatalf("provision calls = %d, want 1", got)
	}
}

func TestProviderConcurrentUnauthorizedSharesProvisionFailure(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(cacheDir, "team-paxm-todd.json"),
		[]byte(`{"url":"https://memory.internal","api_key":"tm_key_stale","agent_id":"paxm-todd","user_id":"usr-1"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	signalPath := filepath.Join(t.TempDir(), "unauthorized.log")
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	provisioner := &blockingProvisioner{
		err:     errors.New("device unavailable"),
		started: make(chan struct{}),
		release: release,
	}
	provider, err := New("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestTeamProviderHelper", "--"},
		Env: map[string]string{
			"PAXM_AGENT_ID":                      "paxm-todd",
			"PAXM_TEAM_PROVIDER_HELPER":          "1",
			"PAXM_TEAM_HELPER_MODE":              "always-unauthorized",
			"PAXM_TEAM_UNAUTHORIZED_SIGNAL_FILE": signalPath,
		},
		Timeout: "5s",
	}, Dependencies{Provisioner: provisioner, CredentialDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, searchErr := provider.Search(context.Background(), memory.SearchQuery{Text: "concurrent failure"})
			results <- searchErr
		}()
	}
	close(start)
	select {
	case <-provisioner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first refresh did not start")
	}
	waitForSignalLines(t, signalPath, 2)
	close(release)
	released = true
	for range 2 {
		searchErr := <-results
		if searchErr == nil || !strings.Contains(searchErr.Error(), "re-provision failed") {
			t.Fatalf("search error = %v, want shared provision failure", searchErr)
		}
	}
	if got := provisioner.callCount(); got != 1 {
		t.Fatalf("provision calls = %d, want 1", got)
	}
}

func TestTeamProviderHelper(t *testing.T) {
	if os.Getenv("PAXM_TEAM_PROVIDER_HELPER") != "1" {
		return
	}
	mode := os.Getenv("PAXM_TEAM_HELPER_MODE")
	if mode == "always-unauthorized" ||
		((mode == "rotate" || mode == "route-rotate") &&
			os.Getenv("TEAM_MEMORY_API_KEY") == "tm_key_stale") {
		if signalPath := os.Getenv("PAXM_TEAM_UNAUTHORIZED_SIGNAL_FILE"); signalPath != "" {
			file, err := os.OpenFile(signalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("unauthorized\n"); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = os.Stderr.WriteString("team memory returned 401: unauthorized\n")
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"error":   map[string]any{"code": -32000, "message": "request failed"},
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode == "explicit-route" {
		if os.Getenv("TEAM_MEMORY_API_KEY") != "tm_key_explicit" ||
			os.Getenv("PAXM_AGENT_ID") != "personal-codex" {
			t.Fatalf("explicit team provider environment changed")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Items []memory.MemoryItem `json:"items"`
			} `json:"params"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "paxm.putBatch" || len(request.Params.Items) != 2 {
			t.Fatalf("explicit batch request = %#v", request)
		}
		writeHelperRefs(t, request.ID, request.Params.Items)
		return
	}
	if mode == "route" || mode == "route-rotate" || mode == "route-fallback" {
		agentID := os.Getenv("PAXM_AGENT_ID")
		if os.Getenv("TEAM_MEMORY_API_KEY") != "tm_key_"+agentID ||
			os.Getenv("TEAM_MEMORY_BASE_URL") != "https://memory.internal" ||
			os.Getenv("PAXM_USER_ID") != "usr-1" {
			t.Fatalf("unexpected routed team provider environment")
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Items []memory.MemoryItem `json:"items"`
			} `json:"params"`
		}
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "paxm.putBatch" {
			t.Fatalf("method = %q, want paxm.putBatch", request.Method)
		}
		for _, item := range request.Params.Items {
			if item.Origin.AgentID != agentID &&
				!(mode == "route-fallback" && item.Origin.AgentID == "") {
				t.Fatalf("item agent = %q, credential agent = %q", item.Origin.AgentID, agentID)
			}
		}
		writeHelperRefs(t, request.ID, request.Params.Items)
		return
	}
	if os.Getenv("TEAM_MEMORY_API_KEY") != os.Getenv("PAXM_TEAM_EXPECTED_KEY") ||
		os.Getenv("TEAM_MEMORY_BASE_URL") != "https://memory.internal" ||
		os.Getenv("PAXM_USER_ID") != "usr-1" ||
		os.Getenv("PAXM_AGENT_ID") != "paxm-todd" {
		t.Fatalf("unexpected team provider environment")
	}
	var request struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"hits":[{"id":"hit-1","text":"discovered","relevance":1,"score":1}]}`)
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      request.ID,
		"result":  result,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeHelperRefs(t *testing.T, requestID json.RawMessage, items []memory.MemoryItem) {
	t.Helper()
	refs := make([]memory.MemoryRef, 0, len(items))
	for _, item := range items {
		refs = append(refs, memory.MemoryRef{ID: item.ID})
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"result":  map[string]any{"refs": refs},
	}); err != nil {
		t.Fatal(err)
	}
}

func writeCachedCredential(t *testing.T, dir string, credential cachedCredential) {
	t.Helper()
	data, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "team-"+config.SlugID(credential.AgentID)+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCachedCredential(t *testing.T, path string) cachedCredential {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var credential cachedCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		t.Fatal(err)
	}
	return credential
}

func waitForSignalLines(t *testing.T, path string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Count(string(data), "\n") >= count {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d unauthorized helper calls", count)
}

func containsJSONValue(data []byte, key, value string) bool {
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) != nil {
		return false
	}
	return decoded[key] == value
}
