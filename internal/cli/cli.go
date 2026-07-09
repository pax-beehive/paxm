package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pax-beehive/memory-adaptor/internal/adapters"
	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
)

type runner struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	configPath string
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	args, configPath, err := extractConfigFlag(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	r := runner{
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		configPath: configPath,
	}
	if len(args) == 0 {
		r.printHelp()
		return 0
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		r.printHelp()
		return 0
	}
	if err := r.run(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func (r runner) run(args []string) error {
	switch args[0] {
	case "setup":
		return r.runSetup(args[1:])
	case "recall":
		return r.runRecall(args[1:])
	case "remember":
		return r.runRemember(args[1:])
	case "config":
		return r.runConfig(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (r runner) runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	force := fs.Bool("force", false, "overwrite an existing config")
	yes := fs.Bool("yes", false, "accept default setup answers")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := r.configFile()
	promptReader := bufio.NewReader(r.stdin)
	configExists := config.Exists(path)
	if configExists && !*force {
		if *yes {
			return fmt.Errorf("config already exists at %s; use --force to overwrite", path)
		}
		overwrite, err := promptBool(promptReader, r.stdout, fmt.Sprintf("Config already exists at %s. Overwrite?", path), false)
		if err != nil {
			return err
		}
		if !overwrite {
			fmt.Fprintln(r.stdout, "setup cancelled")
			return nil
		}
	}
	cfg, err := setupBaseConfig(path, configExists)
	if err != nil {
		return err
	}
	selectedProviders := defaultSelections(providerOptions(cfg), cfgProviderEnabled(cfg))
	selectedHooks := defaultSelections(hookOptions(cfg), cfgHookEnabled(cfg))
	if !*yes {
		var err error
		selectedProviders, err = promptMultiSelect(promptReader, r.stdout, "Select memory providers to enable", providerOptions(cfg), selectedProviders)
		if err != nil {
			return err
		}
		if selectedProviders["local"] {
			local := cfg.Providers["local"]
			local.Path, err = promptString(promptReader, r.stdout, "Local memory path", local.Path)
			if err != nil {
				return err
			}
			mode, err := promptSingleSelect(promptReader, r.stdout, "Local provider mode", []setupOption{
				{ID: "read_write", Label: "read and write"},
				{ID: "read_only", Label: "read only"},
				{ID: "write_only", Label: "write only"},
			}, "read_write")
			if err != nil {
				return err
			}
			switch mode {
			case "read_write":
				local.Read = true
				local.Write = true
			case "read_only":
				local.Read = true
				local.Write = false
			case "write_only":
				local.Read = false
				local.Write = true
			}
			policy, err := promptSingleSelect(promptReader, r.stdout, "Local provider failure policy", []setupOption{
				{ID: "required", Label: "required"},
				{ID: "best_effort", Label: "best effort"},
			}, "required")
			if err != nil {
				return err
			}
			local.Required = policy == "required"
			cfg.Providers["local"] = local
		}
		selectedHooks, err = promptMultiSelect(promptReader, r.stdout, "Select agent hooks to install", hookOptions(cfg), selectedHooks)
		if err != nil {
			return err
		}
	}
	if !anySelected(selectedProviders) {
		return errors.New("setup requires at least one memory provider")
	}

	for name, provider := range cfg.Providers {
		provider.Enabled = selectedProviders[name]
		cfg.Providers[name] = provider
	}
	for name, hook := range cfg.Hooks {
		enabled := selectedHooks[name]
		hook.Enabled = enabled
		for eventName, eventCfg := range hook.Events {
			eventCfg.Recall.Enabled = enabled
			hook.Events[eventName] = eventCfg
		}
		cfg.Hooks[name] = hook
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(r.stdout, "created config: %s\n", path)
	for _, name := range sortedSelected(selectedHooks) {
		if !selectedHooks[name] {
			continue
		}
		scriptPath, err := installHookShim(path, name, "user_prompt")
		if err != nil {
			return err
		}
		fmt.Fprintf(r.stdout, "installed hook shim: %s\n", scriptPath)
		if name == "codex" {
			fmt.Fprintf(r.stdout, "registered Codex global hook: %s\n", codexConfigPath())
		}
	}
	return nil
}

func setupBaseConfig(path string, useExisting bool) (config.Config, error) {
	defaultCfg := config.DefaultConfig(path)
	if !useExisting {
		return defaultCfg, nil
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	cfg = config.Normalize(cfg)
	for name, provider := range defaultCfg.Providers {
		if _, ok := cfg.Providers[name]; !ok {
			cfg.Providers[name] = provider
		}
	}
	for name, hook := range defaultCfg.Hooks {
		if _, ok := cfg.Hooks[name]; !ok {
			cfg.Hooks[name] = hook
		}
	}
	return cfg, nil
}

func (r runner) runRecall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	query := fs.String("query", "", "recall query")
	queryShort := fs.String("q", "", "recall query")
	limit := fs.Int("limit", 8, "maximum memories to return")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read query from stdin")
	hookEvent := fs.Bool("hook-event", false, "read a hook event from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hookEvent {
		var event facade.HookEvent
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(bytes, &event); err != nil {
			return fmt.Errorf("decode hook event JSON: %w", err)
		}
		return r.executeHook(event, *jsonOut)
	}
	q := firstNonEmpty(*query, *queryShort)
	if *stdin {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		q = string(bytes)
	}

	service, err := r.loadService()
	if err != nil {
		return err
	}
	result, err := service.Recall(context.Background(), facade.RecallInput{
		Query: q,
		Limit: *limit,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, result)
	}
	writeRecallMarkdown(r.stdout, result)
	return nil
}

func (r runner) runRemember(args []string) error {
	fs := flag.NewFlagSet("remember", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	text := fs.String("text", "", "memory text")
	source := fs.String("source", "cli", "memory source")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read memory text from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value := *text
	if *stdin {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		value = string(bytes)
	}

	service, err := r.loadService()
	if err != nil {
		return err
	}
	result, err := service.Ingest(context.Background(), facade.IngestInput{
		Text:   value,
		Source: *source,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, result)
	}
	for _, ref := range result.Refs {
		fmt.Fprintf(r.stdout, "stored memory: %s/%s\n", ref.Provider, ref.ID)
	}
	return nil
}

func (r runner) executeHook(event facade.HookEvent, jsonOut bool) error {
	service, err := r.loadService()
	if err != nil {
		return err
	}
	result, err := service.RunHook(context.Background(), event)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(r.stdout, result)
	}
	if result.Skipped || result.Recall == nil {
		return nil
	}
	writeRecallMarkdown(r.stdout, *result.Recall)
	return nil
}

func (r runner) runConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config command requires a subcommand: path, show, doctor")
	}
	switch args[0] {
	case "path":
		fmt.Fprintln(r.stdout, r.configFile())
		return nil
	case "show":
		cfg, err := config.Load(r.configFile())
		if err != nil {
			return err
		}
		return writeJSON(r.stdout, cfg)
	case "doctor":
		return r.runConfigDoctor(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func (r runner) runConfigDoctor(args []string) error {
	fs := flag.NewFlagSet("config doctor", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	router, err := adapters.DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		return err
	}
	statuses, err := router.Health(context.Background())
	if *jsonOut {
		if writeErr := writeJSON(r.stdout, statuses); writeErr != nil {
			return writeErr
		}
		return err
	}
	for _, status := range statuses {
		if status.OK {
			fmt.Fprintf(r.stdout, "ok: %s\n", status.Provider)
			continue
		}
		fmt.Fprintf(r.stdout, "error: %s: %s\n", status.Provider, status.Error)
	}
	return err
}

func (r runner) loadService() (*facade.Service, error) {
	cfg, err := config.Load(r.configFile())
	if err != nil {
		if errors.Is(err, config.ErrConfigMissing) {
			return nil, fmt.Errorf("%w; run `paxm --config %s setup`", err, r.configFile())
		}
		return nil, err
	}
	router, err := adapters.DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		return nil, err
	}
	return facade.New(cfg, router), nil
}

func (r runner) configFile() string {
	if r.configPath != "" {
		return config.ExpandPath(r.configPath)
	}
	return config.DefaultConfigPath()
}

func (r runner) printHelp() {
	fmt.Fprintln(r.stdout, "paxm - memory adapter CLI")
	fmt.Fprintln(r.stdout)
	fmt.Fprintln(r.stdout, "Usage:")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] setup")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] recall --query TEXT [--json]")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] remember --text TEXT")
	fmt.Fprintln(r.stdout, "  paxm [--config PATH] config doctor")
}

type setupOption struct {
	ID    string
	Label string
}

func providerOptions(cfg config.Config) []setupOption {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	options := make([]setupOption, 0, len(names))
	for _, name := range names {
		provider := cfg.Providers[name]
		label := name
		if provider.Type != "" && provider.Type != name {
			label = fmt.Sprintf("%s (%s)", name, provider.Type)
		}
		options = append(options, setupOption{ID: name, Label: label})
	}
	return options
}

func hookOptions(cfg config.Config) []setupOption {
	names := make([]string, 0, len(cfg.Hooks))
	for name := range cfg.Hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	options := make([]setupOption, 0, len(names))
	for _, name := range names {
		options = append(options, setupOption{ID: name, Label: name})
	}
	return options
}

func cfgProviderEnabled(cfg config.Config) map[string]bool {
	selected := make(map[string]bool)
	for name, provider := range cfg.Providers {
		selected[name] = provider.Enabled
	}
	return selected
}

func cfgHookEnabled(cfg config.Config) map[string]bool {
	selected := make(map[string]bool)
	for name, hook := range cfg.Hooks {
		selected[name] = hook.Enabled
	}
	return selected
}

func defaultSelections(options []setupOption, selected map[string]bool) map[string]bool {
	normalized := make(map[string]bool)
	for _, option := range options {
		normalized[option.ID] = selected[option.ID]
	}
	return normalized
}

func anySelected(selected map[string]bool) bool {
	for _, enabled := range selected {
		if enabled {
			return true
		}
	}
	return false
}

func sortedSelected(selected map[string]bool) []string {
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func promptBool(reader *bufio.Reader, writer io.Writer, question string, defaultValue bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultValue {
		suffix = " [Y/n]: "
	}
	for {
		fmt.Fprint(writer, question+suffix)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultValue, nil
		}
		switch value {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(writer, "Please answer yes or no.")
		}
		if errors.Is(err, io.EOF) {
			return defaultValue, nil
		}
	}
}

func promptSingleSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaultID string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", question)
	}
	defaultIndex := optionIndex(options, defaultID)
	if defaultIndex == -1 && len(options) > 0 {
		defaultIndex = 0
		defaultID = options[0].ID
	}
	for {
		fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if i == defaultIndex {
				marker = "[x]"
			}
			fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		fmt.Fprintf(writer, "Choose one [%d]: ", defaultIndex+1)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultID, nil
		}
		index, parseErr := strconv.Atoi(value)
		if parseErr == nil && index >= 1 && index <= len(options) {
			return options[index-1].ID, nil
		}
		for _, option := range options {
			if strings.EqualFold(value, option.ID) {
				return option.ID, nil
			}
		}
		fmt.Fprintln(writer, "Please choose one of the listed options.")
		if errors.Is(err, io.EOF) {
			return defaultID, nil
		}
	}
}

func promptMultiSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaults map[string]bool) (map[string]bool, error) {
	if len(options) == 0 {
		return map[string]bool{}, nil
	}
	defaultText := defaultSelectionText(options, defaults)
	for {
		fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if defaults[option.ID] {
				marker = "[x]"
			}
			fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		fmt.Fprintf(writer, "Choose numbers, comma-separated, or all/none [%s]: ", defaultText)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultSelections(options, defaults), nil
		}
		selected, parseErr := parseMultiSelect(value, options)
		if parseErr == nil {
			return selected, nil
		}
		fmt.Fprintf(writer, "%s\n", parseErr)
		if errors.Is(err, io.EOF) {
			return defaultSelections(options, defaults), nil
		}
	}
}

func parseMultiSelect(value string, options []setupOption) (map[string]bool, error) {
	selected := make(map[string]bool)
	for _, option := range options {
		selected[option.ID] = false
	}
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "all":
		for _, option := range options {
			selected[option.ID] = true
		}
		return selected, nil
	case "none":
		return selected, nil
	}
	parts := strings.Split(value, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 1 || index > len(options) {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		selected[options[index-1].ID] = true
	}
	return selected, nil
}

func defaultSelectionText(options []setupOption, defaults map[string]bool) string {
	var indexes []string
	for i, option := range options {
		if defaults[option.ID] {
			indexes = append(indexes, strconv.Itoa(i+1))
		}
	}
	if len(indexes) == 0 {
		return "none"
	}
	return strings.Join(indexes, ",")
}

func optionIndex(options []setupOption, id string) int {
	for i, option := range options {
		if option.ID == id {
			return i
		}
	}
	return -1
}

func promptString(reader *bufio.Reader, writer io.Writer, question, defaultValue string) (string, error) {
	fmt.Fprintf(writer, "%s [%s]: ", question, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func installHookShim(configPath, target, event string) (string, error) {
	hooksDir := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", err
	}
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	scriptPath := filepath.Join(hooksDir, target+"-"+event)
	script := "#!/bin/sh\nexec " + shellQuote(binaryPath) + " --config " + shellQuote(config.ExpandPath(configPath)) + " recall --hook-event --json\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return "", err
	}
	if target == "codex" {
		if err := installCodexGlobalHook(codexConfigPath(), scriptPath); err != nil {
			return "", err
		}
	}
	return scriptPath, nil
}

func codexConfigPath() string {
	if path := os.Getenv("PAXM_CODEX_CONFIG"); path != "" {
		return config.ExpandPath(path)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return filepath.Join(".codex", "config.toml")
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(config.ExpandPath(codexHome), "config.toml")
}

func installCodexGlobalHook(path, scriptPath string) error {
	path = config.ExpandPath(path)
	command := shellQuote(scriptPath)
	entry := `{ hooks = [{ type = "command", command = "` + escapeTomlString(command) + `", async = false, statusMessage = "Recalling paxm memory" }] }`

	contentBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := string(contentBytes)
	if strings.Contains(content, scriptPath) || strings.Contains(content, command) {
		return nil
	}

	updated := upsertCodexUserPromptHook(content, entry)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(contentBytes) > 0 {
		backupPath := path + ".paxm.bak"
		if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backupPath, contentBytes, 0o600); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func upsertCodexUserPromptHook(content, entry string) string {
	if content == "" {
		return "[hooks]\nUserPromptSubmit = [" + entry + "]\n"
	}
	lines := strings.SplitAfter(content, "\n")
	hooksStart := -1
	hooksEnd := len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[hooks]" {
			hooksStart = i
			continue
		}
		if hooksStart != -1 && i > hooksStart && strings.HasPrefix(trimmed, "[") {
			hooksEnd = i
			break
		}
	}
	if hooksStart == -1 {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		return content + "\n[hooks]\nUserPromptSubmit = [" + entry + "]\n"
	}
	for i := hooksStart + 1; i < hooksEnd; i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "UserPromptSubmit = ") {
			lines[i] = appendInlineTomlArray(line, entry)
			return strings.Join(lines, "")
		}
	}
	newLine := "UserPromptSubmit = [" + entry + "]\n"
	updated := append([]string{}, lines[:hooksStart+1]...)
	updated = append(updated, newLine)
	updated = append(updated, lines[hooksStart+1:]...)
	return strings.Join(updated, "")
}

func appendInlineTomlArray(line, entry string) string {
	newline := ""
	if strings.HasSuffix(line, "\n") {
		newline = "\n"
		line = strings.TrimSuffix(line, "\n")
	}
	index := strings.LastIndex(line, "]")
	if index == -1 {
		return line + newline
	}
	prefix := strings.TrimRight(line[:index], " ")
	suffix := line[index:]
	if strings.HasSuffix(prefix, "[") {
		return prefix + entry + suffix + newline
	}
	return prefix + ", " + entry + suffix + newline
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func escapeTomlString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(value)
}

func extractConfigFlag(args []string) ([]string, string, error) {
	var filtered []string
	var configPath string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config" || arg == "-c":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s requires a path", arg)
			}
			configPath = args[i+1]
			i++
		case strings.HasPrefix(arg, "--config="):
			configPath = strings.TrimPrefix(arg, "--config=")
		default:
			filtered = append(filtered, arg)
		}
	}
	return filtered, configPath, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeRecallMarkdown(w io.Writer, result facade.RecallResult) {
	if len(result.Hits) == 0 {
		fmt.Fprintln(w, "No memories found.")
		return
	}
	for i, hit := range result.Hits {
		fmt.Fprintf(w, "### Memory %d (%s)\n", i+1, hit.Provider)
		if hit.Source != "" {
			fmt.Fprintf(w, "Source: %s\n\n", hit.Source)
		} else {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, strings.TrimSpace(hit.Text))
		fmt.Fprintln(w)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
