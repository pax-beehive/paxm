package team

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

type blockingProvisioner struct {
	mu        sync.Mutex
	calls     int
	provision paxlclient.AgentProvision
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
	return p.provision, nil
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

func TestTeamProviderHelper(t *testing.T) {
	if os.Getenv("PAXM_TEAM_PROVIDER_HELPER") != "1" {
		return
	}
	mode := os.Getenv("PAXM_TEAM_HELPER_MODE")
	if mode == "always-unauthorized" ||
		(mode == "rotate" && os.Getenv("TEAM_MEMORY_API_KEY") == "tm_key_stale") {
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
