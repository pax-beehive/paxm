package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

const paxmMCPServerName = "paxm"

type nativeHookBinding struct {
	ConfigEvent string
	NativeEvent string
	Matcher     string
}

func requestedAgentNames() []string {
	return []string{"cursor", "trae", "trae-cn", "kimi", "zcode", "kiro", "cline"}
}

func isRequestedAgent(name string) bool {
	for _, candidate := range requestedAgentNames() {
		if name == candidate {
			return true
		}
	}
	return false
}

func nativeHookBindings(name string) []nativeHookBinding {
	switch name {
	case "cursor":
		return []nativeHookBinding{
			{ConfigEvent: "session_start", NativeEvent: "sessionStart"},
			{ConfigEvent: "user_input", NativeEvent: "beforeSubmitPrompt"},
			{ConfigEvent: "turn_end", NativeEvent: "afterAgentResponse"},
		}
	case "kiro":
		return []nativeHookBinding{
			{ConfigEvent: "session_start", NativeEvent: "agentSpawn"},
			{ConfigEvent: "user_input", NativeEvent: "userPromptSubmit"},
			{ConfigEvent: "turn_end", NativeEvent: "stop"},
		}
	case "cline":
		return []nativeHookBinding{
			{ConfigEvent: "session_start", NativeEvent: "TaskStart"},
			{ConfigEvent: "session_start", NativeEvent: "TaskResume"},
			{ConfigEvent: "user_input", NativeEvent: "UserPromptSubmit"},
			{ConfigEvent: "turn_end", NativeEvent: "TaskComplete"},
			{ConfigEvent: "turn_end", NativeEvent: "TaskCancel"},
		}
	case "trae", "trae-cn", "kimi", "zcode":
		return []nativeHookBinding{
			{ConfigEvent: "session_start", NativeEvent: "SessionStart", Matcher: "startup|resume"},
			{ConfigEvent: "user_input", NativeEvent: "UserPromptSubmit"},
			{ConfigEvent: "turn_end", NativeEvent: "Stop"},
		}
	default:
		bindings := make([]nativeHookBinding, 0, len(installedHookEvents()))
		for _, event := range installedHookEvents() {
			bindings = append(bindings, nativeHookBinding{
				ConfigEvent: event.ConfigEvent,
				NativeEvent: event.NativeEvent,
				Matcher:     event.Matcher,
			})
		}
		return bindings
	}
}

func hookInstallEventsForNamedAgent(name string, agent config.AgentConfig) []hookInstallEvent {
	bindings := nativeHookBindings(name)
	events := make([]hookInstallEvent, 0, len(bindings))
	for _, binding := range bindings {
		hook, ok := agent.Hooks[binding.ConfigEvent]
		if binding.ConfigEvent == "session_start" && agent.Enabled {
			// Session identity/local-time bootstrapping is useful even for agents
			// whose older config predates an explicit session_start hook.
		} else if !ok || (!hook.Recall.Enabled && !hook.Write.Enabled) {
			continue
		}
		status := "Running paxm memory hook"
		if base, found := hookInstallEventByConfig(binding.ConfigEvent); found {
			status = base.Status
		}
		events = append(events, hookInstallEvent{
			ConfigEvent: binding.ConfigEvent,
			NativeEvent: binding.NativeEvent,
			Matcher:     binding.Matcher,
			Status:      status,
		})
	}
	return events
}

func integrationPath(envName string, fallback ...string) string {
	if path := strings.TrimSpace(os.Getenv(envName)); path != "" {
		return config.ExpandPath(path)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(fallback...)
	}
	parts := append([]string{home}, fallback...)
	return filepath.Join(parts...)
}

func cursorHooksPath() string { return integrationPath("PAXM_CURSOR_HOOKS", ".cursor", "hooks.json") }
func cursorMCPPath() string   { return integrationPath("PAXM_CURSOR_MCP", ".cursor", "mcp.json") }
func traeHooksPath() string   { return integrationPath("PAXM_TRAE_HOOKS", ".trae", "hooks.json") }
func traeCNHooksPath() string { return integrationPath("PAXM_TRAE_CN_HOOKS", ".trae-cn", "hooks.json") }
func kimiConfigPath() string  { return kimiPath("PAXM_KIMI_CONFIG", "config.toml") }
func kimiMCPPath() string     { return kimiPath("PAXM_KIMI_MCP", "mcp.json") }
func kiroAgentPath() string {
	return integrationPath("PAXM_KIRO_AGENT", ".kiro", "agents", "paxm.json")
}
func kiroMCPPath() string { return integrationPath("PAXM_KIRO_MCP", ".kiro", "settings", "mcp.json") }
func clineHooksDir() string {
	if path := strings.TrimSpace(os.Getenv("PAXM_CLINE_HOOKS_DIR")); path != "" {
		return config.ExpandPath(path)
	}
	if path := strings.TrimSpace(os.Getenv("CLINE_HOOKS_DIR")); path != "" {
		return config.ExpandPath(path)
	}
	return integrationPath("PAXM_CLINE_HOOKS_DIR", ".cline", "hooks")
}

func kimiPath(override, filename string) string {
	if path := strings.TrimSpace(os.Getenv(override)); path != "" {
		return config.ExpandPath(path)
	}
	if home := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); home != "" {
		return filepath.Join(config.ExpandPath(home), filename)
	}
	return integrationPath(override, ".kimi-code", filename)
}

func clineMCPPath() string {
	if path := strings.TrimSpace(os.Getenv("PAXM_CLINE_MCP")); path != "" {
		return config.ExpandPath(path)
	}
	if dir := strings.TrimSpace(os.Getenv("CLINE_DATA_DIR")); dir != "" {
		return filepath.Join(config.ExpandPath(dir), "settings", "cline_mcp_settings.json")
	}
	return integrationPath("PAXM_CLINE_MCP", ".cline", "data", "settings", "cline_mcp_settings.json")
}

func zcodeConfigPath() string {
	return integrationPath("PAXM_ZCODE_CONFIG", ".zcode", "cli", "config.json")
}

func traeMCPPath() string {
	if path := strings.TrimSpace(os.Getenv("PAXM_TRAE_MCP")); path != "" {
		return config.ExpandPath(path)
	}
	return desktopApplicationConfig("Trae", "User", "mcp.json")
}

func traeCNMCPPath() string {
	if path := strings.TrimSpace(os.Getenv("PAXM_TRAE_CN_MCP")); path != "" {
		return config.ExpandPath(path)
	}
	return desktopApplicationConfig("Trae CN", "User", "mcp.json")
}

func desktopApplicationConfig(app string, parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(append([]string{app}, parts...)...)
	}
	base := filepath.Join(home, ".config", app)
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support", app)
	case "windows":
		if value := strings.TrimSpace(os.Getenv("APPDATA")); value != "" {
			base = filepath.Join(value, app)
		}
	}
	return filepath.Join(append([]string{base}, parts...)...)
}

type mcpServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

func paxmMCPServer(configPath, agent string) mcpServerConfig {
	binaryPath, err := os.Executable()
	if err != nil || strings.TrimSpace(binaryPath) == "" {
		binaryPath = "paxm"
	}
	return mcpServerConfig{
		Command: binaryPath,
		Args: []string{
			"--config", config.ExpandPath(configPath),
			"mcp", "serve", "--agent", agent,
		},
	}
}

func installAgentMCP(configPath, agent string) (string, error) {
	path := agentMCPPath(agent)
	if path == "" {
		return "", nil
	}
	if agent == "zcode" {
		return path, updateZCodeMCP(path, configPath, agent, false)
	}
	return path, updateStandardMCP(path, configPath, agent, false)
}

func uninstallAgentMCP(configPath, agent string) error {
	path := agentMCPPath(agent)
	if path == "" {
		return nil
	}
	if agent == "zcode" {
		return updateZCodeMCP(path, configPath, agent, true)
	}
	return updateStandardMCP(path, configPath, agent, true)
}

func preflightAgentMCP(agent string) error {
	path := agentMCPPath(agent)
	if path == "" {
		return nil
	}
	root, _, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	if agent == "zcode" {
		mcp, decodeErr := rawObject(root["mcp"])
		if decodeErr != nil {
			return fmt.Errorf("decode ZCode MCP config %s: %w", path, decodeErr)
		}
		if _, decodeErr = rawObject(mcp["servers"]); decodeErr != nil {
			return fmt.Errorf("decode ZCode MCP servers %s: %w", path, decodeErr)
		}
		return nil
	}
	if _, err := rawObject(root["mcpServers"]); err != nil {
		return fmt.Errorf("decode MCP servers %s: %w", path, err)
	}
	return nil
}

func agentMCPPath(agent string) string {
	switch agent {
	case "cursor":
		return cursorMCPPath()
	case "trae":
		return traeMCPPath()
	case "trae-cn":
		return traeCNMCPPath()
	case "kimi":
		return kimiMCPPath()
	case "zcode":
		return zcodeConfigPath()
	case "kiro":
		return kiroMCPPath()
	case "cline":
		return clineMCPPath()
	default:
		return ""
	}
}

func updateStandardMCP(path, configPath, agent string, remove bool) error {
	root, original, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	servers, err := rawObject(root["mcpServers"])
	if err != nil {
		return fmt.Errorf("decode MCP servers %s: %w", path, err)
	}
	if remove {
		if _, ok := servers[paxmMCPServerName]; !ok {
			return nil
		}
		delete(servers, paxmMCPServerName)
	} else {
		entry, marshalErr := json.Marshal(paxmMCPServer(configPath, agent))
		if marshalErr != nil {
			return marshalErr
		}
		servers[paxmMCPServerName] = entry
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	root["mcpServers"] = encoded
	return writeJSONConfig(path, original, root)
}

func updateZCodeMCP(path, configPath, agent string, remove bool) error {
	root, original, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	mcp, err := rawObject(root["mcp"])
	if err != nil {
		return fmt.Errorf("decode ZCode MCP config %s: %w", path, err)
	}
	servers, err := rawObject(mcp["servers"])
	if err != nil {
		return fmt.Errorf("decode ZCode MCP servers %s: %w", path, err)
	}
	if remove {
		if _, ok := servers[paxmMCPServerName]; !ok {
			return nil
		}
		delete(servers, paxmMCPServerName)
	} else {
		entry, marshalErr := json.Marshal(paxmMCPServer(configPath, agent))
		if marshalErr != nil {
			return marshalErr
		}
		servers[paxmMCPServerName] = entry
	}
	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	mcp["servers"] = serversJSON
	mcpJSON, err := json.Marshal(mcp)
	if err != nil {
		return err
	}
	root["mcp"] = mcpJSON
	return writeJSONConfig(path, original, root)
}

func readJSONConfig(path string) (map[string]json.RawMessage, []byte, error) {
	path = config.ExpandPath(path)
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	root := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(original)) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return nil, nil, fmt.Errorf("decode JSON config %s: %w", path, err)
		}
	}
	return root, original, nil
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	object := make(map[string]json.RawMessage)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return object, nil
	}
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func writeJSONConfig(path string, original []byte, root map[string]json.RawMessage) error {
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if bytes.Equal(bytes.TrimSpace(original), bytes.TrimSpace(encoded)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}
