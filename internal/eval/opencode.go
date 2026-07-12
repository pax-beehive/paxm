package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/opencodeplugin"
)

type OpenCodeCommandRunner func(context.Context, string, []string, string, ...string) ([]byte, error)

type OpenCodeExecutor struct {
	Binary     string
	PaxmBinary string
	Model      string
	Timeout    time.Duration
	RunCommand OpenCodeCommandRunner
}

func (e OpenCodeExecutor) Execute(ctx context.Context, request AgentRequest) (AgentResponse, error) {
	if strings.TrimSpace(e.Binary) == "" {
		return AgentResponse{}, errors.New("OpenCode binary is required")
	}
	if request.Arm != AgentArmControl && strings.TrimSpace(e.PaxmBinary) == "" {
		return AgentResponse{}, errors.New("paxm binary is required for memory-assisted OpenCode arms")
	}
	configDir := filepath.Join(filepath.Dir(request.PaxmConfigPath), "opencode", string(request.Arm), sanitizeScopeID(request.QuestionID))
	if err := prepareOpenCodeConfig(configDir, request, e.PaxmBinary); err != nil {
		return AgentResponse{}, err
	}
	env := replaceOpenCodeEnv(os.Environ(), []string{
		"OPENCODE_CONFIG_DIR=" + configDir,
		"OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DISABLE_CLAUDE_CODE=true",
		"OPENCODE_DISABLE_LSP_DOWNLOAD=true",
		"OPENCODE_CLIENT=paxm-eval",
		"PAXM_BINARY=" + e.PaxmBinary,
		"PAXM_CONFIG=" + request.PaxmConfigPath,
		"PAXM_OPENCODE_RECALL_MARKER=" + filepath.Join(configDir, "recall-used"),
		"PAXM_OPENCODE_RECALL=" + boolEnv(request.RecallEnabled),
		"PAXM_OPENCODE_WRITE=" + boolEnv(request.WriteEnabled),
	})
	args := []string{"run", "--format", "json", "--title", "paxm-eval-" + request.QuestionID, "--dir", request.Workspace}
	if strings.TrimSpace(e.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(e.Model))
	}
	args = append(args, request.Prompt)
	runner := e.RunCommand
	if runner == nil {
		runner = runOpenCodeCommand
	}
	commandCtx := ctx
	var cancel context.CancelFunc
	if e.Timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	started := time.Now()
	output, err := runner(commandCtx, request.Workspace, env, e.Binary, args...)
	response, parseErr := parseOpenCodeOutput(output)
	response.Model = strings.TrimSpace(e.Model)
	response.DurationMS = time.Since(started).Milliseconds()
	if bytes.Contains(output, []byte("paxm_recall")) || bytes.Contains(output, []byte("paxm-paxm_recall")) {
		response.RecallUsed = true
	}
	if _, statErr := os.Stat(filepath.Join(configDir, "recall-used")); statErr == nil {
		response.RecallUsed = true
	}
	if commandCtx.Err() != nil {
		return response, commandCtx.Err()
	}
	if err != nil {
		return response, fmt.Errorf("OpenCode run: %w: %s", err, truncateOutput(output, 2000))
	}
	if parseErr != nil {
		return response, parseErr
	}
	return response, nil
}

func (e OpenCodeExecutor) FlushWrites(ctx context.Context, configPath string) error {
	_, err := runOpenCodeCommand(ctx, filepath.Dir(configPath), os.Environ(), e.PaxmBinary, "--config", configPath, "__hook-control", "--shutdown")
	return err
}

func prepareOpenCodeConfig(configDir string, request AgentRequest, paxmBinary string) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	permissions := map[string]string{"*": "deny"}
	tools := map[string]bool{"*": false}
	configValue := map[string]any{"$schema": "https://opencode.ai/config.json", "permission": permissions, "tools": tools}
	if request.Arm == AgentArmActive {
		configValue["mcp"] = map[string]any{
			"paxm": map[string]any{
				"type": "local", "enabled": true, "timeout": 10000,
				"command": []string{paxmBinary, "--config", request.PaxmConfigPath, "mcp", "serve"},
			},
		}
		tools["paxm*"] = true
		permissions["paxm*"] = "allow"
	}
	data, err := json.MarshalIndent(configValue, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if !request.RecallEnabled && !request.WriteEnabled {
		return nil
	}
	pluginDir := filepath.Join(configDir, "plugins")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pluginDir, "paxm.js"), []byte(opencodeplugin.Source), 0o600)
}

func boolEnv(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func runOpenCodeCommand(ctx context.Context, dir string, env []string, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func parseOpenCodeOutput(output []byte) (AgentResponse, error) {
	var response AgentResponse
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionID"`
			Part      struct {
				Text   string  `json:"text"`
				Cost   float64 `json:"cost"`
				Tokens struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"tokens"`
			} `json:"part"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.SessionID != "" {
			response.SessionID = event.SessionID
		}
		switch event.Type {
		case "text":
			if strings.TrimSpace(event.Part.Text) != "" {
				if response.Text != "" {
					response.Text += "\n"
				}
				response.Text += strings.TrimSpace(event.Part.Text)
			}
		case "step_finish":
			response.InputTokens += event.Part.Tokens.Input
			response.OutputTokens += event.Part.Tokens.Output
			response.Cost += event.Part.Cost
		}
	}
	if err := scanner.Err(); err != nil {
		return response, err
	}
	if strings.TrimSpace(response.Text) == "" {
		return response, fmt.Errorf("OpenCode returned no text: %s", truncateOutput(output, 2000))
	}
	return response, nil
}

func replaceOpenCodeEnv(base, overrides []string) []string {
	values := make(map[string]string)
	var order []string
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "OPENCODE_CONFIG" || key == "OPENCODE_CONFIG_CONTENT" || key == "OPENCODE_CONFIG_DIR" {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}
	for _, item := range overrides {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, values[key])
	}
	return result
}

func truncateOutput(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
