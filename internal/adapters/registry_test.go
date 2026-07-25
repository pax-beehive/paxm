package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	teamadapter "github.com/pax-beehive/paxm/internal/adapters/team"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

func TestBuildRouterUsesProfileRequiredForHealth(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig(filepath.Join(t.TempDir(), "config.yaml"))
	recall := cfg.RecallProfiles["default"]
	recall.Providers[0].Required = false
	cfg.RecallProfiles["default"] = recall
	passive := cfg.RecallProfiles["passive"]
	passive.Providers[0].Required = false
	cfg.RecallProfiles["passive"] = passive
	passiveInitial := cfg.RecallProfiles["passive_initial"]
	passiveInitial.Providers[0].Required = false
	cfg.RecallProfiles["passive_initial"] = passiveInitial
	for name, write := range cfg.WriteProfiles {
		write.Providers[0].Required = false
		cfg.WriteProfiles[name] = write
	}

	router, err := DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := router.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Required {
		t.Fatalf("expected best-effort health status, got %#v", statuses)
	}
}

func TestDefaultRegistryBuildsZepProvider(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Version: 1,
		Providers: map[string]config.ProviderConfig{
			"zep": {
				Type:        "zep",
				Enabled:     true,
				APIKey:      "key",
				UserID:      "user-1",
				SearchScope: "episodes",
			},
		},
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "zep", Required: false, Weight: 1}},
			},
		},
		WriteProfiles: map[string]config.WriteProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "zep", Required: false}},
			},
		},
	}

	router, err := DefaultRegistry().BuildRouter(config.Normalize(cfg))
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := router.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Provider != "zep" {
		t.Fatalf("unexpected statuses: %#v", statuses)
	}
}

func TestDefaultRegistryBuildsMem0Provider(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
	}))
	defer server.Close()

	cfg := config.Config{
		Version: 1,
		Providers: map[string]config.ProviderConfig{
			"mem0": {
				Type:    "mem0",
				Enabled: true,
				BaseURL: server.URL,
				UserID:  "user-1",
			},
		},
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "mem0", Required: false, Weight: 1}},
			},
		},
		WriteProfiles: map[string]config.WriteProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{{Name: "mem0", Required: false}},
			},
		},
	}

	router, err := DefaultRegistry().BuildRouter(config.Normalize(cfg))
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := router.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Provider != "mem0" {
		t.Fatalf("unexpected statuses: %#v", statuses)
	}
}

func TestDefaultRegistryBuildsMem0CloudProvider(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/memories/" || r.Header.Get("Authorization") != "Token test-key" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer server.Close()
	cfg := config.Config{
		Version:        1,
		Providers:      map[string]config.ProviderConfig{"cloud": {Type: "mem0-cloud", Enabled: true, BaseURL: server.URL, APIKey: "test-key", UserID: "user"}},
		RecallProfiles: map[string]config.RecallProfileConfig{"default": {Providers: []config.ProviderRouteConfig{{Name: "cloud"}}}},
	}
	router, err := DefaultRegistry().BuildRouter(config.Normalize(cfg))
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := router.Health(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].Provider != "cloud" {
		t.Fatalf("statuses = %#v, err = %v", statuses, err)
	}
}

func TestRegistryAllowsMultipleInstancesOfOneProviderType(t *testing.T) {
	t.Parallel()

	registry := Registry{factories: make(map[string]Factory)}
	registry.Register("capture", func(name string, _ config.ProviderConfig) (memory.Provider, error) {
		return captureProvider{name: name}, nil
	})
	cfg := config.Config{
		Version: 1,
		Providers: map[string]config.ProviderConfig{
			"personal": {Type: "capture", Enabled: true},
			"team":     {Type: "capture", Enabled: true},
		},
		RecallProfiles: map[string]config.RecallProfileConfig{
			"default": {
				Providers: []config.ProviderRouteConfig{
					{Name: "personal", Required: false, Weight: 1},
					{Name: "team", Required: true, Weight: 1},
				},
			},
		},
	}

	router, err := registry.BuildRouter(config.Normalize(cfg))
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := router.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].Provider != "personal" || statuses[1].Provider != "team" || !statuses[1].Required {
		t.Fatalf("unexpected statuses: %#v", statuses)
	}
}

func TestDefaultRegistryBuildsMemOSProviderTypes(t *testing.T) {
	registry := DefaultRegistry()
	tests := []struct {
		name string
		cfg  config.ProviderConfig
	}{
		{"local", config.ProviderConfig{Type: "memos", BaseURL: "http://memos.test", UserID: "u", MemCubeID: "c"}},
		{"cloud", config.ProviderConfig{Type: "memos-cloud", BaseURL: "https://memos.test", APIKey: "key", UserID: "u"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := registry.BuildProvider(test.name, test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if provider.Name() != test.name {
				t.Fatalf("name = %q", provider.Name())
			}
		})
	}
}

func TestDefaultRegistryBuildsOpenVikingProvider(t *testing.T) {
	t.Parallel()

	provider, err := DefaultRegistry().BuildProvider("private", config.ProviderConfig{
		Type: "openviking", BaseURL: "http://openviking.test", APIKey: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "private" {
		t.Fatalf("name = %q, want private", provider.Name())
	}
}

func TestDefaultRegistryBuildsTeamMemoryProviderWithExplicitCredentials(t *testing.T) {
	t.Parallel()

	provider, err := DefaultRegistry().BuildProvider("team", config.ProviderConfig{
		Type: "team-memory", Transport: "stdio", Command: "paxm-team-memory-provider",
		Env: map[string]string{"TEAM_MEMORY_API_KEY": "explicit-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "team" {
		t.Fatalf("name = %q, want team", provider.Name())
	}
}

func TestDefaultRegistryRecognizesExistingTeamMemoryJSONRPCConfig(t *testing.T) {
	t.Parallel()

	provider, err := DefaultRegistry().BuildProvider("workstation", config.ProviderConfig{
		Type: "jsonrpc", Transport: "stdio",
		Command: "/usr/local/bin/paxm-team-memory-provider",
		Env:     map[string]string{"TEAM_MEMORY_API_KEY": "explicit-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "workstation" {
		t.Fatalf("name = %q, want workstation", provider.Name())
	}
	if _, ok := provider.(*teamadapter.Provider); !ok {
		t.Fatalf("provider type = %T, want Team Memory adapter", provider)
	}
}

type captureProvider struct {
	name string
}

func (p captureProvider) Name() string {
	return p.name
}

func (p captureProvider) Search(context.Context, memory.SearchQuery) ([]memory.MemoryHit, error) {
	return nil, errors.New("not implemented")
}

func (p captureProvider) Put(context.Context, memory.MemoryItem) (memory.MemoryRef, error) {
	return memory.MemoryRef{}, errors.New("not implemented")
}

func (p captureProvider) Health(context.Context) error {
	return nil
}
