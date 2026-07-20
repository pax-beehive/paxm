package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupPrompterSecretKeepsExistingOnBlank(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	prompter := newSetupPrompter(strings.NewReader("\n"), &output)
	got, err := prompter.secret("API key", "existing-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "existing-key" {
		t.Fatalf("blank secret should keep existing value, got %q", got)
	}

	output.Reset()
	prompter = newSetupPrompter(strings.NewReader("new-key\n"), &output)
	got, err = prompter.secret("API key", "existing-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-key" {
		t.Fatalf("secret should return the typed value, got %q", got)
	}
}

func TestDetectInstalledAgentsFromConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	detected := detectInstalledAgents()
	if !detected["claude"] {
		t.Fatalf("claude should be detected from ~/.claude: %#v", detected)
	}
}

func TestWriteSetupNextStepsPlainOutput(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	writeSetupNextSteps(&output, false)
	for _, expected := range []string{"Next steps", "paxm remember", "paxm recall", "paxm config doctor"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("next steps missing %q: %s", expected, output.String())
		}
	}
}
