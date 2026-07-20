package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
)

func TestPromptOpenVikingProvider(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig(t.TempDir() + "/config.yaml")
	var output bytes.Buffer
	prompter := newSetupPrompter(strings.NewReader("\nsecret\n"), &output)
	if err := promptOpenVikingProvider(prompter, &cfg, "openviking"); err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["openviking"]
	if provider.BaseURL != config.DefaultOpenVikingBaseURL() || provider.APIKey != "secret" {
		t.Fatalf("provider = %#v", provider)
	}
}

func TestApplySetupSelectionsRoutesNewlyEnabledProvider(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig(t.TempDir() + "/config.yaml")
	selectedProviders := defaultSelections(providerOptions(cfg), map[string]bool{"sqlite": true, "openviking": true})
	selectedHooks := defaultSelections(hookOptions(cfg), nil)
	applySetupSelections(&cfg, selectedProviders, selectedHooks)

	if !recallProfileHasProvider(cfg.RecallProfiles["default"], "openviking") || !writeProfileHasProvider(cfg.WriteProfiles["default"], "openviking") {
		t.Fatalf("newly enabled provider was not routed for read/write: %#v", cfg.RecallProfiles["default"].Providers)
	}
	if required, _ := config.ProviderRouteRequired(cfg.RecallProfiles["default"].Providers, "openviking"); !required {
		t.Fatalf("newly enabled provider should default to required routing")
	}

	// Re-applying selections must not clobber custom routing of an
	// already-enabled provider.
	removeRecallRoute(&cfg, "openviking")
	applySetupSelections(&cfg, selectedProviders, selectedHooks)
	if recallProfileHasProvider(cfg.RecallProfiles["default"], "openviking") {
		t.Fatalf("custom routing of an enabled provider should be preserved")
	}
}
