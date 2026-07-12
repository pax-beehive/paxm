package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeExecutorBuildsPassivePluginAndParsesJSONStream(t *testing.T) {
	dir := t.TempDir()
	var capturedEnv []string
	executor := OpenCodeExecutor{
		Binary:     "/bin/opencode",
		PaxmBinary: "/bin/paxm",
		RunCommand: func(_ context.Context, _ string, env []string, _ string, _ ...string) ([]byte, error) {
			capturedEnv = append([]string(nil), env...)
			return []byte("{\"type\":\"text\",\"sessionID\":\"ses_1\",\"part\":{\"text\":\"A dog\"}}\n" +
				"{\"type\":\"step_finish\",\"sessionID\":\"ses_1\",\"part\":{\"tokens\":{\"input\":120,\"output\":3},\"cost\":0.0025}}\n"), nil
		},
	}
	response, err := executor.Execute(context.Background(), AgentRequest{
		AgentName: "opencode", Arm: AgentArmPassive, QuestionID: "q1", Prompt: "question",
		Workspace: dir, PaxmConfigPath: filepath.Join(dir, "paxm", "config.yaml"), RecallEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "A dog" || response.SessionID != "ses_1" || response.InputTokens != 120 || response.OutputTokens != 3 || response.Cost != 0.0025 {
		t.Fatalf("response = %#v", response)
	}
	configDir := envValue(capturedEnv, "OPENCODE_CONFIG_DIR")
	if configDir == "" {
		t.Fatal("missing isolated OpenCode config dir")
	}
	configData, err := os.ReadFile(filepath.Join(configDir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configData), `"*": false`) {
		t.Fatalf("OpenCode config did not disable all built-in tools: %s", configData)
	}
	plugin, err := os.ReadFile(filepath.Join(configDir, "plugins", "paxm.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plugin), "PAXM_BINARY") || !strings.Contains(string(plugin), "session.idle") || !strings.Contains(string(plugin), "__hook") {
		t.Fatalf("plugin = %s", plugin)
	}
	if envValue(capturedEnv, "PAXM_BINARY") != "/bin/paxm" || envValue(capturedEnv, "PAXM_OPENCODE_RECALL") != "1" {
		t.Fatalf("plugin environment = %#v", capturedEnv)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
