package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
