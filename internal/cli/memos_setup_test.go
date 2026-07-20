package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
)

func TestPromptMemOSProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider config.ProviderConfig
		input    string
		check    func(*testing.T, config.ProviderConfig)
	}{
		{
			name:     "self hosted",
			provider: config.ProviderConfig{Type: "memos"},
			input:    "\n\nu1\ncube-1\n",
			check: func(t *testing.T, got config.ProviderConfig) {
				if got.BaseURL != config.DefaultMemOSBaseURL() || got.UserID != "u1" || got.MemCubeID != "cube-1" {
					t.Fatalf("provider=%#v", got)
				}
			},
		},
		{
			name:     "cloud",
			provider: config.ProviderConfig{Type: "memos-cloud"},
			input:    "\nkey\nu1\n",
			check: func(t *testing.T, got config.ProviderConfig) {
				if got.BaseURL != config.DefaultMemOSCloudBaseURL() || got.APIKey != "key" || got.UserID != "u1" {
					t.Fatalf("provider=%#v", got)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig(t.TempDir() + "/config.yaml")
			name := "memos"
			if test.provider.Type == "memos-cloud" {
				name = "memos_cloud"
			}
			cfg.Providers[name] = test.provider
			var output bytes.Buffer
			prompter := newSetupPrompter(strings.NewReader(test.input), &output)
			err := promptMemOSProvider(prompter, &cfg, name)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, cfg.Providers[name])
		})
	}
}
