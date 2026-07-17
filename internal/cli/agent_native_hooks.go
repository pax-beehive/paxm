package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const clineManagedMarker = "# managed by paxm"

type cursorHookCommand struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type kiroHookCommand struct {
	Command   string `json:"command"`
	Matcher   string `json:"matcher,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type zcodeHookCommand struct {
	Async   bool   `json:"async"`
	Command string `json:"command"`
	Type    string `json:"type"`
}

type zcodeHookGroup struct {
	Hooks   []zcodeHookCommand `json:"hooks"`
	Matcher string             `json:"matcher,omitempty"`
}

func installRequestedNativeHooks(name string, scripts map[string]string) (string, error) {
	switch name {
	case "cursor":
		path := cursorHooksPath()
		return path, installCursorHooks(path, scripts)
	case "trae":
		path := traeHooksPath()
		return path, installClaudeGlobalHooks(path, scripts)
	case "trae-cn":
		path := traeCNHooksPath()
		return path, installClaudeGlobalHooks(path, scripts)
	case "kimi":
		path := kimiConfigPath()
		return path, installKimiHooks(path, scripts)
	case "zcode":
		path := zcodeConfigPath()
		return path, installZCodeHooks(path, scripts)
	case "kiro":
		path := kiroAgentPath()
		return path, installKiroAgent(path, scripts)
	case "cline":
		path := clineHooksDir()
		return path, installClineHooks(path, scripts)
	default:
		return "", nil
	}
}

func uninstallRequestedNativeHooks(name, marker string) error {
	switch name {
	case "cursor":
		return removeCursorHooks(cursorHooksPath(), marker)
	case "trae":
		return removeClaudeGlobalHooks(traeHooksPath(), marker)
	case "trae-cn":
		return removeClaudeGlobalHooks(traeCNHooksPath(), marker)
	case "kimi":
		return removeKimiHooks(kimiConfigPath())
	case "zcode":
		return removeZCodeHooks(zcodeConfigPath(), marker)
	case "kiro":
		return removeOwnedFile(kiroAgentPath(), marker)
	case "cline":
		return removeClineHooks(clineHooksDir(), marker)
	default:
		return nil
	}
}

func installCursorHooks(path string, scripts map[string]string) error {
	root, original, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	hooks, err := rawObject(root["hooks"])
	if err != nil {
		return fmt.Errorf("decode Cursor hooks %s: %w", path, err)
	}
	for _, binding := range nativeHookBindings("cursor") {
		scriptPath := strings.TrimSpace(scripts[binding.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		commands, decodeErr := rawArray(hooks[binding.NativeEvent])
		if decodeErr != nil {
			return fmt.Errorf("decode Cursor %s hooks: %w", binding.NativeEvent, decodeErr)
		}
		if rawArrayContains(commands, scriptPath) {
			continue
		}
		command, marshalErr := json.Marshal(cursorHookCommand{Command: shellQuote(scriptPath), Timeout: 60})
		if marshalErr != nil {
			return marshalErr
		}
		commands = append(commands, command)
		hooks[binding.NativeEvent], err = json.Marshal(commands)
		if err != nil {
			return err
		}
	}
	hooksJSON, err := json.Marshal(hooks)
	if err != nil {
		return err
	}
	root["hooks"] = hooksJSON
	if _, ok := root["version"]; !ok {
		root["version"] = json.RawMessage("1")
	}
	return writeJSONConfig(path, original, root)
}

func removeCursorHooks(path, marker string) error {
	root, original, err := readJSONConfig(path)
	if err != nil || len(original) == 0 {
		return err
	}
	hooks, err := rawObject(root["hooks"])
	if err != nil {
		return err
	}
	changed := false
	for event, raw := range hooks {
		commands, decodeErr := rawArray(raw)
		if decodeErr != nil {
			return decodeErr
		}
		filtered := removeRawContaining(commands, marker)
		if len(filtered) == len(commands) {
			continue
		}
		changed = true
		if len(filtered) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event], err = json.Marshal(filtered)
		if err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	root["hooks"], err = json.Marshal(hooks)
	if err != nil {
		return err
	}
	return writeJSONConfig(path, original, root)
}

func installZCodeHooks(path string, scripts map[string]string) error {
	root, original, err := readJSONConfig(path)
	if err != nil {
		return err
	}
	hooks, err := rawObject(root["hooks"])
	if err != nil {
		return fmt.Errorf("decode ZCode hooks %s: %w", path, err)
	}
	events, err := rawObject(hooks["events"])
	if err != nil {
		return fmt.Errorf("decode ZCode hook events %s: %w", path, err)
	}
	for _, binding := range nativeHookBindings("zcode") {
		scriptPath := strings.TrimSpace(scripts[binding.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		groups, decodeErr := rawArray(events[binding.NativeEvent])
		if decodeErr != nil {
			return decodeErr
		}
		if rawArrayContains(groups, scriptPath) {
			continue
		}
		group := zcodeHookGroup{
			Matcher: binding.Matcher,
			Hooks: []zcodeHookCommand{{
				Command: shellQuote(scriptPath),
				Type:    "command",
			}},
		}
		encoded, marshalErr := json.Marshal(group)
		if marshalErr != nil {
			return marshalErr
		}
		groups = append(groups, encoded)
		events[binding.NativeEvent], err = json.Marshal(groups)
		if err != nil {
			return err
		}
	}
	hooks["enabled"] = json.RawMessage("true")
	hooks["events"], err = json.Marshal(events)
	if err != nil {
		return err
	}
	root["hooks"], err = json.Marshal(hooks)
	if err != nil {
		return err
	}
	return writeJSONConfig(path, original, root)
}

func removeZCodeHooks(path, marker string) error {
	root, original, err := readJSONConfig(path)
	if err != nil || len(original) == 0 {
		return err
	}
	hooks, err := rawObject(root["hooks"])
	if err != nil {
		return err
	}
	events, err := rawObject(hooks["events"])
	if err != nil {
		return err
	}
	changed := false
	for event, raw := range events {
		groups, decodeErr := rawArray(raw)
		if decodeErr != nil {
			return decodeErr
		}
		filtered := removeRawContaining(groups, marker)
		if len(filtered) == len(groups) {
			continue
		}
		changed = true
		if len(filtered) == 0 {
			delete(events, event)
			continue
		}
		events[event], err = json.Marshal(filtered)
		if err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	hooks["events"], err = json.Marshal(events)
	if err != nil {
		return err
	}
	root["hooks"], err = json.Marshal(hooks)
	if err != nil {
		return err
	}
	return writeJSONConfig(path, original, root)
}

func installKiroAgent(path string, scripts map[string]string) error {
	if original, err := os.ReadFile(path); err == nil && !bytes.Contains(original, []byte("paxm-")) {
		return fmt.Errorf("Kiro agent %s already exists and is not managed by paxm", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	hooks := make(map[string][]kiroHookCommand)
	for _, binding := range nativeHookBindings("kiro") {
		scriptPath := strings.TrimSpace(scripts[binding.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		hooks[binding.NativeEvent] = []kiroHookCommand{{
			Command:   shellQuote(scriptPath),
			Matcher:   binding.Matcher,
			TimeoutMS: 60_000,
		}}
	}
	payload := struct {
		Name           string                       `json:"name"`
		Description    string                       `json:"description"`
		Prompt         string                       `json:"prompt"`
		IncludeMCPJSON bool                         `json:"includeMcpJson"`
		Hooks          map[string][]kiroHookCommand `json:"hooks"`
	}{
		Name:           "paxm",
		Description:    "Kiro agent with paxm active recall and passive memory hooks.",
		Prompt:         "Use the paxm MCP tools when durable memory would help the task.",
		IncludeMCPJSON: true,
		Hooks:          hooks,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}

func installClineHooks(dir string, scripts map[string]string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, binding := range nativeHookBindings("cline") {
		scriptPath := strings.TrimSpace(scripts[binding.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		path := filepath.Join(dir, binding.NativeEvent)
		if original, err := os.ReadFile(path); err == nil && !bytes.Contains(original, []byte(clineManagedMarker)) {
			return fmt.Errorf("Cline hook %s already exists; paxm will not overwrite it", path)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		script := "#!/bin/sh\n" + clineManagedMarker + "\nexec " + shellQuote(scriptPath) + "\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func removeClineHooks(dir, marker string) error {
	var errs []error
	for _, binding := range nativeHookBindings("cline") {
		path := filepath.Join(dir, binding.NativeEvent)
		content, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !bytes.Contains(content, []byte(clineManagedMarker)) || !bytes.Contains(content, []byte(marker)) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func installKimiHooks(path string, scripts map[string]string) error {
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	content, _ := removeKimiManagedBlock(string(original))
	var block strings.Builder
	block.WriteString("\n# >>> paxm managed kimi hooks >>>\n")
	for _, binding := range nativeHookBindings("kimi") {
		scriptPath := strings.TrimSpace(scripts[binding.ConfigEvent])
		if scriptPath == "" {
			continue
		}
		block.WriteString("[[hooks]]\n")
		block.WriteString("event = \"")
		block.WriteString(escapeTomlString(binding.NativeEvent))
		block.WriteString("\"\n")
		if binding.Matcher != "" {
			block.WriteString("matcher = \"")
			block.WriteString(escapeTomlString(binding.Matcher))
			block.WriteString("\"\n")
		}
		block.WriteString("command = \"")
		block.WriteString(escapeTomlString(shellQuote(scriptPath)))
		block.WriteString("\"\n")
		block.WriteString("timeout = 60\n\n")
	}
	block.WriteString("# <<< paxm managed kimi hooks <<<\n")
	updated := strings.TrimRight(content, "\n") + block.String()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func removeKimiHooks(path string) error {
	original, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	updated, changed := removeKimiManagedBlock(string(original))
	if !changed {
		return nil
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func removeKimiManagedBlock(content string) (string, bool) {
	const start = "# >>> paxm managed kimi hooks >>>"
	const end = "# <<< paxm managed kimi hooks <<<"
	startIndex := strings.Index(content, start)
	if startIndex < 0 {
		return content, false
	}
	endIndex := strings.Index(content[startIndex:], end)
	if endIndex < 0 {
		return content, false
	}
	endIndex += startIndex + len(end)
	if endIndex < len(content) && content[endIndex] == '\n' {
		endIndex++
	}
	return strings.TrimRight(content[:startIndex], "\n") + content[endIndex:], true
}

func removeOwnedFile(path, marker string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Contains(content, []byte(marker)) {
		return nil
	}
	return os.Remove(path)
}

func rawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func rawArrayContains(values []json.RawMessage, marker string) bool {
	for _, value := range values {
		if bytes.Contains(value, []byte(marker)) {
			return true
		}
	}
	return false
}

func removeRawContaining(values []json.RawMessage, marker string) []json.RawMessage {
	filtered := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		if !bytes.Contains(value, []byte(marker)) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
