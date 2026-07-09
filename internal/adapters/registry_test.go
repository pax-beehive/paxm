package adapters

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pax-beehive/memory-adaptor/internal/config"
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
	write := cfg.WriteProfiles["default"]
	write.Providers[0].Required = false
	cfg.WriteProfiles["default"] = write

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
