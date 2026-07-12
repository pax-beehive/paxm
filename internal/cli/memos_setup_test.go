package cli

import (
	"bufio"
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
			input:    "\n\nu1\ncube-1\n3\n3\n",
			check: func(t *testing.T, got config.ProviderConfig) {
				if got.BaseURL != config.DefaultMemOSBaseURL() || got.UserID != "u1" || got.MemCubeID != "cube-1" || got.SearchMode != "mixture" {
					t.Fatalf("provider=%#v", got)
				}
			},
		},
		{
			name:     "cloud",
			provider: config.ProviderConfig{Type: "memos-cloud"},
			input:    "\nkey\nu1\nopencode\n3\n",
			check: func(t *testing.T, got config.ProviderConfig) {
				if got.BaseURL != config.DefaultMemOSCloudBaseURL() || got.APIKey != "key" || got.UserID != "u1" || got.AgentID != "opencode" {
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
			err := promptMemOSProvider(bufio.NewReader(strings.NewReader(test.input)), &output, &cfg, name)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, cfg.Providers[name])
		})
	}
}
