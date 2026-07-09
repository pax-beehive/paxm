package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	case "__hook":
		return r.runInternalHook(args[1:])
	case "__hook-daemon":
		return r.runHookDaemon(args[1:])
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
			cfg.Providers["local"] = local
			if err := promptProviderRouting(promptReader, r.stdout, &cfg, "local", "Local"); err != nil {
				return err
			}
		}
		if selectedProviders["zep"] {
			zep := cfg.Providers["zep"]
			zep.APIKey, err = promptString(promptReader, r.stdout, "Zep API key", zep.APIKey)
			if err != nil {
				return err
			}
			if strings.TrimSpace(zep.APIKey) == "" {
				return errors.New("zep setup requires an API key")
			}
			targetDefault := "user"
			if zep.GraphID != "" {
				targetDefault = "graph"
			}
			target, err := promptSingleSelect(promptReader, r.stdout, "Zep memory target", []setupOption{
				{ID: "user", Label: "user graph"},
				{ID: "graph", Label: "named graph"},
			}, targetDefault)
			if err != nil {
				return err
			}
			if target == "user" {
				zep.UserID, err = promptString(promptReader, r.stdout, "Zep user ID", zep.UserID)
				if err != nil {
					return err
				}
				zep.GraphID = ""
				if strings.TrimSpace(zep.UserID) == "" {
					return errors.New("zep setup requires a user ID")
				}
			} else {
				zep.GraphID, err = promptString(promptReader, r.stdout, "Zep graph ID", zep.GraphID)
				if err != nil {
					return err
				}
				zep.UserID = ""
				if strings.TrimSpace(zep.GraphID) == "" {
					return errors.New("zep setup requires a graph ID")
				}
			}
			zep.SearchScope, err = promptSingleSelect(promptReader, r.stdout, "Zep search scope", []setupOption{
				{ID: "episodes", Label: "episodes"},
				{ID: "edges", Label: "edges"},
				{ID: "nodes", Label: "nodes"},
				{ID: "observations", Label: "observations"},
				{ID: "thread_summaries", Label: "thread summaries"},
				{ID: "auto", Label: "auto"},
			}, firstNonEmpty(zep.SearchScope, "episodes"))
			if err != nil {
				return err
			}
			cfg.Providers["zep"] = zep
			if err := promptProviderRouting(promptReader, r.stdout, &cfg, "zep", "Zep"); err != nil {
				return err
			}
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
		if !provider.Enabled {
			removeProviderFromDefaultProfiles(&cfg, name)
		}
	}
	for name, agent := range cfg.Agents {
		enabled := selectedHooks[name]
		agent.Enabled = true
		for eventName, eventCfg := range agent.Hooks {
			if eventName == "user_input" {
				eventCfg.Recall.Enabled = enabled
			}
			eventCfg.Write.Enabled = enabled && eventCfg.Write.Enabled
			agent.Hooks[eventName] = eventCfg
		}
		cfg.Agents[name] = agent
	}
	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(r.stdout, "created config: %s\n", path)
	for _, name := range sortedSelected(selectedHooks) {
		if !selectedHooks[name] {
			continue
		}
		if err := removeLegacyHookShim(path, name); err != nil {
			return err
		}
		for _, event := range installedHookEvents() {
			scriptPath, err := installHookShim(path, name, event.ConfigEvent)
			if err != nil {
				return err
			}
			fmt.Fprintf(r.stdout, "installed hook shim: %s\n", scriptPath)
		}
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
	for name, profile := range defaultCfg.RecallProfiles {
		if _, ok := cfg.RecallProfiles[name]; !ok {
			cfg.RecallProfiles[name] = profile
		}
	}
	for name, profile := range defaultCfg.WriteProfiles {
		if _, ok := cfg.WriteProfiles[name]; !ok {
			cfg.WriteProfiles[name] = profile
		}
	}
	for name, agent := range defaultCfg.Agents {
		existing, ok := cfg.Agents[name]
		if !ok {
			cfg.Agents[name] = agent
			continue
		}
		if existing.Hooks == nil {
			existing.Hooks = make(map[string]config.AgentHookConfig)
		}
		for eventName, eventCfg := range agent.Hooks {
			existingHook, ok := existing.Hooks[eventName]
			if !ok {
				existing.Hooks[eventName] = eventCfg
				continue
			}
			existing.Hooks[eventName] = mergeHookDefaults(existingHook, eventCfg)
		}
		cfg.Agents[name] = existing
	}
	return cfg, nil
}

func mergeHookDefaults(current, defaults config.AgentHookConfig) config.AgentHookConfig {
	if current.Write.Profile == "" {
		current.Write.Profile = defaults.Write.Profile
	}
	if current.Write.Template == "" {
		current.Write.Template = defaults.Write.Template
	}
	if current.Write.Mode == "" {
		current.Write.Mode = defaults.Write.Mode
	}
	if !current.Write.Enabled && current.Write.Template == "{{ .prompt }}" && current.Write.Mode == "prompt" && defaults.Write.Enabled {
		current.Write = defaults.Write
		return current
	}
	if current.Write.Buffer == (config.HookBufferConfig{}) {
		current.Write.Buffer = defaults.Write.Buffer
	}
	return current
}

func (r runner) runRecall(args []string) error {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	query := fs.String("query", "", "recall query")
	queryShort := fs.String("q", "", "recall query")
	profile := fs.String("profile", "", "recall profile")
	limit := fs.Int("limit", 0, "maximum memories to return")
	jsonOut := fs.Bool("json", false, "write JSON")
	stdin := fs.Bool("stdin", false, "read query from stdin")
	hookEvent := fs.Bool("hook-event", false, "read a hook event from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *hookEvent {
		bytes, err := io.ReadAll(r.stdin)
		if err != nil {
			return err
		}
		event, err := decodeHookEvent(bytes, "codex", "user_input")
		if err != nil {
			return err
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
		Query:   q,
		Profile: *profile,
		Limit:   *limit,
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
	profile := fs.String("profile", "", "write profile")
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
		Text:    value,
		Profile: *profile,
		Source:  *source,
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

type hookBufferRequest struct {
	Target string          `json:"target"`
	Event  string          `json:"event"`
	Raw    json.RawMessage `json:"raw"`
}

type hookBufferResponse struct {
	OK      bool   `json:"ok"`
	Flushed int    `json:"flushed,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (r runner) runInternalHook(args []string) error {
	fs := flag.NewFlagSet("__hook", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	target := fs.String("target", "codex", "hook target")
	eventName := fs.String("event", "", "hook event")
	jsonOut := fs.Bool("json", false, "write JSON recall output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := io.ReadAll(r.stdin)
	if err != nil {
		return err
	}
	event, err := decodeHookEvent(raw, *target, *eventName)
	if err != nil {
		return err
	}
	if err := r.sendHookToBuffer(event); err != nil {
		fmt.Fprintf(r.stderr, "paxm hook buffer skipped: %s\n", err)
	}
	if event.Event == "user_input" {
		return r.executeHook(event, *jsonOut)
	}
	return nil
}

func (r runner) runHookDaemon(args []string) error {
	fs := flag.NewFlagSet("__hook-daemon", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	socket := fs.String("socket", hookSocketPath(r.configFile()), "daemon socket")
	idleTimeout := fs.Duration("idle-timeout", 30*time.Minute, "daemon idle timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	service, err := r.loadService()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*socket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(*socket)
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(*socket)
	}()

	var buffer []facade.IngestInput
	deadline := time.NewTimer(*idleTimeout)
	defer deadline.Stop()
	for {
		type acceptResult struct {
			conn net.Conn
			err  error
		}
		accepted := make(chan acceptResult, 1)
		go func() {
			conn, err := listener.Accept()
			accepted <- acceptResult{conn: conn, err: err}
		}()
		select {
		case <-deadline.C:
			if len(buffer) > 0 {
				_, _ = service.IngestBatch(context.Background(), facade.IngestBatchInput{Items: buffer})
			}
			return nil
		case result := <-accepted:
			if result.err != nil {
				return result.err
			}
			flushed, err := handleHookBufferConn(context.Background(), service, result.conn, &buffer)
			if err != nil {
				fmt.Fprintf(r.stderr, "paxm hook buffer error: %s\n", err)
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(*idleTimeout)
			_ = flushed
		}
	}
}

func handleHookBufferConn(ctx context.Context, service *facade.Service, conn net.Conn, buffer *[]facade.IngestInput) (int, error) {
	defer conn.Close()
	var request hookBufferRequest
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, err
	}
	event, err := decodeHookEvent(request.Raw, request.Target, request.Event)
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, err
	}
	item, ok, err := service.HookWriteItem(event)
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, err
	}
	if !ok {
		_ = writeJSON(conn, hookBufferResponse{OK: true})
		return 0, nil
	}
	bufferCfg := service.HookBufferConfig(event)
	if !bufferCfg.Enabled {
		_, err := service.IngestBatch(ctx, facade.IngestBatchInput{Items: []facade.IngestInput{item}})
		if err != nil {
			_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
			return 0, err
		}
		_ = writeJSON(conn, hookBufferResponse{OK: true, Flushed: 1})
		return 1, nil
	}
	*buffer = append(*buffer, item)
	shouldFlush := bufferCfg.Flush || (bufferCfg.FlushCount > 0 && len(*buffer) >= bufferCfg.FlushCount)
	if !shouldFlush {
		_ = writeJSON(conn, hookBufferResponse{OK: true})
		return 0, nil
	}
	flushed := len(*buffer)
	_, err = service.IngestBatch(ctx, facade.IngestBatchInput{Items: *buffer})
	if err != nil {
		_ = writeJSON(conn, hookBufferResponse{OK: false, Error: err.Error()})
		return 0, err
	}
	*buffer = nil
	_ = writeJSON(conn, hookBufferResponse{OK: true, Flushed: flushed})
	return flushed, nil
}

func (r runner) sendHookToBuffer(event facade.HookEvent) error {
	socket := hookSocketPath(r.configFile())
	response, err := sendHookBufferRequest(socket, event)
	if err != nil {
		if startErr := r.startHookDaemon(socket); startErr != nil {
			return startErr
		}
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			response, err = sendHookBufferRequest(socket, event)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return err
	}
	if !response.OK && response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (r runner) startHookDaemon(socket string) error {
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	cmd := exec.Command(binaryPath, "--config", r.configFile(), "__hook-daemon", "--socket", socket)
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer devNull.Close()
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}
	detachCommand(cmd)
	return cmd.Start()
}

func sendHookBufferRequest(socket string, event facade.HookEvent) (hookBufferResponse, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return hookBufferResponse{}, err
	}
	defer conn.Close()
	raw := event.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	request := hookBufferRequest{
		Target: event.Target,
		Event:  event.Event,
		Raw:    raw,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return hookBufferResponse{}, err
	}
	var response hookBufferResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return hookBufferResponse{}, err
	}
	return response, nil
}

func decodeHookEvent(raw []byte, target, eventName string) (facade.HookEvent, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var event facade.HookEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return facade.HookEvent{}, fmt.Errorf("decode hook event JSON: %w", err)
	}
	if event.Target == "" {
		event.Target = target
	}
	if event.Target == "" {
		event.Target = "codex"
	}
	if event.Event == "" {
		event.Event = eventName
	}
	if event.Event == "" {
		event.Event = "user_input"
	}
	if event.Prompt == "" {
		event.Prompt = promptFromRawHook(raw)
	}
	enrichHookEventFromRaw(&event, raw)
	event.Raw = append(json.RawMessage(nil), raw...)
	return event, nil
}

func promptFromRawHook(raw []byte) string {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"prompt", "user_prompt", "input", "message"} {
		value, ok := object[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func enrichHookEventFromRaw(event *facade.HookEvent, raw []byte) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return
	}
	if event.Workspace == "" {
		for _, key := range []string{"workspace", "cwd", "current_dir"} {
			if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
				event.Workspace = value
				break
			}
		}
	}
	if event.Metadata == nil {
		event.Metadata = make(map[string]string)
	}
	for _, key := range []string{"session_id", "transcript_path", "cwd", "current_dir", "model", "source"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			event.Metadata[key] = value
		}
	}
}

func bytesTrimSpace(bytes []byte) []byte {
	return []byte(strings.TrimSpace(string(bytes)))
}

func hookSocketPath(configPath string) string {
	return filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", "paxm-hook.sock")
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
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
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
	for name, agent := range cfg.Agents {
		for _, hook := range agent.Hooks {
			if hook.Recall.Enabled || hook.Write.Enabled {
				selected[name] = true
				break
			}
		}
	}
	return selected
}

func promptProviderRouting(reader *bufio.Reader, writer io.Writer, cfg *config.Config, provider, label string) error {
	mode, err := promptSingleSelect(reader, writer, label+" provider mode", []setupOption{
		{ID: "read_write", Label: "read and write"},
		{ID: "read_only", Label: "read only"},
		{ID: "write_only", Label: "write only"},
	}, currentProviderMode(*cfg, provider))
	if err != nil {
		return err
	}
	policy, err := promptSingleSelect(reader, writer, label+" provider failure policy", []setupOption{
		{ID: "required", Label: "required"},
		{ID: "best_effort", Label: "best effort"},
	}, currentProviderPolicy(*cfg, provider))
	if err != nil {
		return err
	}
	setDefaultProviderMode(cfg, provider, mode, policy == "required")
	return nil
}

func currentProviderMode(cfg config.Config, provider string) string {
	canRead := recallProfileHasProvider(cfg.RecallProfiles["default"], provider)
	canWrite := writeProfileHasProvider(cfg.WriteProfiles["default"], provider)
	switch {
	case canRead && canWrite:
		return "read_write"
	case canRead:
		return "read_only"
	case canWrite:
		return "write_only"
	default:
		return "read_write"
	}
}

func currentProviderPolicy(cfg config.Config, provider string) string {
	required, ok := providerRequiredInRecallProfile(cfg.RecallProfiles["default"], provider)
	if !ok {
		required, ok = providerRequiredInWriteProfile(cfg.WriteProfiles["default"], provider)
	}
	if ok && !required {
		return "best_effort"
	}
	return "required"
}

func setDefaultProviderMode(cfg *config.Config, provider, mode string, required bool) {
	switch mode {
	case "read_only":
		upsertRecallRoute(cfg, provider, required)
		removeWriteRoute(cfg, provider)
	case "write_only":
		removeRecallRoute(cfg, provider)
		upsertWriteRoute(cfg, provider, required)
	default:
		upsertRecallRoute(cfg, provider, required)
		upsertWriteRoute(cfg, provider, required)
	}
}

func removeProviderFromDefaultProfiles(cfg *config.Config, provider string) {
	removeRecallRoute(cfg, provider)
	removeWriteRoute(cfg, provider)
}

func recallProfileHasProvider(profile config.RecallProfileConfig, provider string) bool {
	_, ok := providerRequiredInRecallProfile(profile, provider)
	return ok
}

func writeProfileHasProvider(profile config.WriteProfileConfig, provider string) bool {
	_, ok := providerRequiredInWriteProfile(profile, provider)
	return ok
}

func providerRequiredInRecallProfile(profile config.RecallProfileConfig, provider string) (bool, bool) {
	for _, route := range profile.Providers {
		if route.Name == provider {
			return route.Required, true
		}
	}
	return false, false
}

func providerRequiredInWriteProfile(profile config.WriteProfileConfig, provider string) (bool, bool) {
	for _, route := range profile.Providers {
		if route.Name == provider {
			return route.Required, true
		}
	}
	return false, false
}

func upsertRecallRoute(cfg *config.Config, provider string, required bool) {
	profile := cfg.RecallProfiles["default"]
	for i, route := range profile.Providers {
		if route.Name == provider {
			route.Required = required
			if route.Weight == 0 {
				route.Weight = 1
			}
			profile.Providers[i] = route
			cfg.RecallProfiles["default"] = profile
			return
		}
	}
	profile.Providers = append(profile.Providers, config.ProviderRouteConfig{Name: provider, Required: required, Weight: 1})
	cfg.RecallProfiles["default"] = profile
}

func removeRecallRoute(cfg *config.Config, provider string) {
	profile := cfg.RecallProfiles["default"]
	profile.Providers = filterRoutes(profile.Providers, provider)
	cfg.RecallProfiles["default"] = profile
}

func upsertWriteRoute(cfg *config.Config, provider string, required bool) {
	profile := cfg.WriteProfiles["default"]
	for i, route := range profile.Providers {
		if route.Name == provider {
			route.Required = required
			if route.Weight == 0 {
				route.Weight = 1
			}
			profile.Providers[i] = route
			cfg.WriteProfiles["default"] = profile
			return
		}
	}
	profile.Providers = append(profile.Providers, config.ProviderRouteConfig{Name: provider, Required: required, Weight: 1})
	cfg.WriteProfiles["default"] = profile
}

func removeWriteRoute(cfg *config.Config, provider string) {
	profile := cfg.WriteProfiles["default"]
	profile.Providers = filterRoutes(profile.Providers, provider)
	cfg.WriteProfiles["default"] = profile
}

func filterRoutes(routes []config.ProviderRouteConfig, provider string) []config.ProviderRouteConfig {
	filtered := routes[:0]
	for _, route := range routes {
		if route.Name != provider {
			filtered = append(filtered, route)
		}
	}
	return filtered
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

func removeLegacyHookShim(configPath, target string) error {
	legacyPath := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks", target+"-user_prompt")
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type hookInstallEvent struct {
	ConfigEvent string
	CodexEvent  string
	Matcher     string
	Status      string
}

func installedHookEvents() []hookInstallEvent {
	return []hookInstallEvent{
		{
			ConfigEvent: "session_start",
			CodexEvent:  "SessionStart",
			Matcher:     "startup|resume|clear|compact",
			Status:      "Buffering paxm session memory",
		},
		{
			ConfigEvent: "user_input",
			CodexEvent:  "UserPromptSubmit",
			Status:      "Recalling paxm memory",
		},
		{
			ConfigEvent: "turn_end",
			CodexEvent:  "Stop",
			Status:      "Buffering paxm turn memory",
		},
	}
}

func hookInstallEventByConfig(configEvent string) (hookInstallEvent, bool) {
	for _, event := range installedHookEvents() {
		if event.ConfigEvent == configEvent {
			return event, true
		}
	}
	return hookInstallEvent{}, false
}

func installHookShim(configPath, target, event string) (string, error) {
	hooksDir := filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", err
	}
	installEvent, ok := hookInstallEventByConfig(event)
	if !ok {
		return "", fmt.Errorf("unsupported hook event %q", event)
	}
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	scriptPath := filepath.Join(hooksDir, target+"-"+event)
	script := "#!/bin/sh\nexec " + shellQuote(binaryPath) + " --config " + shellQuote(config.ExpandPath(configPath)) + " __hook --target " + shellQuote(target) + " --event " + shellQuote(event) + " --json\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return "", err
	}
	if target == "codex" {
		if err := installCodexGlobalHook(codexConfigPath(), scriptPath, installEvent.ConfigEvent); err != nil {
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

func installCodexGlobalHook(path, scriptPath, configEvent string) error {
	path = config.ExpandPath(path)
	installEvent, ok := hookInstallEventByConfig(configEvent)
	if !ok {
		return fmt.Errorf("unsupported Codex hook event %q", configEvent)
	}
	command := shellQuote(scriptPath)
	commandHook := `{ type = "command", command = "` + escapeTomlString(command) + `", async = false, statusMessage = "` + escapeTomlString(installEvent.Status) + `" }`
	entry := `{ hooks = [` + commandHook + `] }`
	if installEvent.Matcher != "" {
		entry = `{ matcher = "` + escapeTomlString(installEvent.Matcher) + `", hooks = [` + commandHook + `] }`
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content, prunedLegacy := pruneLegacyCodexUserPromptHook(string(contentBytes))
	if strings.Contains(content, scriptPath) || strings.Contains(content, command) {
		if prunedLegacy {
			return writeCodexConfig(path, contentBytes, content)
		}
		return nil
	}

	updated := upsertCodexHook(content, installEvent.CodexEvent, entry)
	return writeCodexConfig(path, contentBytes, updated)
}

func writeCodexConfig(path string, original []byte, updated string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(original) > 0 {
		backupPath := path + ".paxm.bak"
		if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backupPath, original, 0o600); err != nil {
				return err
			}
		}
	}
	return os.WriteFile(path, []byte(updated), 0o600)
}

func pruneLegacyCodexUserPromptHook(content string) (string, bool) {
	if !strings.Contains(content, "codex-user_prompt") {
		return content, false
	}
	lines := strings.SplitAfter(content, "\n")
	changed := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "UserPromptSubmit = ") {
			next := removeInlineTomlArrayEntries(line, "codex-user_prompt")
			if next != line {
				lines[i] = next
				changed = true
			}
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, ""), true
}

func upsertCodexHook(content, eventName, entry string) string {
	if content == "" {
		return "[hooks]\n" + eventName + " = [" + entry + "]\n"
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
		return content + "\n[hooks]\n" + eventName + " = [" + entry + "]\n"
	}
	for i := hooksStart + 1; i < hooksEnd; i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), eventName+" = ") {
			lines[i] = appendInlineTomlArray(line, entry)
			return strings.Join(lines, "")
		}
	}
	newLine := eventName + " = [" + entry + "]\n"
	updated := append([]string{}, lines[:hooksStart+1]...)
	updated = append(updated, newLine)
	updated = append(updated, lines[hooksStart+1:]...)
	return strings.Join(updated, "")
}

func removeInlineTomlArrayEntries(line, marker string) string {
	newline := ""
	if strings.HasSuffix(line, "\n") {
		newline = "\n"
		line = strings.TrimSuffix(line, "\n")
	}
	start := strings.Index(line, "[")
	end := strings.LastIndex(line, "]")
	if start == -1 || end <= start {
		return line + newline
	}
	prefix := line[:start+1]
	body := line[start+1 : end]
	suffix := line[end:]
	entries := splitTopLevelInlineEntries(body)
	filtered := entries[:0]
	changed := false
	for _, entry := range entries {
		if strings.Contains(entry, marker) {
			changed = true
			continue
		}
		filtered = append(filtered, entry)
	}
	if !changed {
		return line + newline
	}
	return prefix + strings.Join(filtered, ", ") + suffix + newline
}

func splitTopLevelInlineEntries(body string) []string {
	var entries []string
	start := 0
	depth := 0
	inString := false
	escaped := false
	for i, char := range body {
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				entries = append(entries, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(body[start:]) != "" {
		entries = append(entries, strings.TrimSpace(body[start:]))
	}
	return entries
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
