package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/backfill"
	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/sessions"
)

type backfillStartResult struct {
	Started bool   `json:"started"`
	RunID   string `json:"run_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

type backfillRunArgs struct {
	agentName        string
	providerName     string
	before           string
	rate             string
	maxDuration      time.Duration
	backgroundMode   bool
	backgroundWorker bool
	runID            string
	startResultPath  string
}

func (r runner) runBackfill(args []string) error {
	if len(args) == 0 {
		return errors.New("backfill requires scan, run, or status")
	}
	switch args[0] {
	case "scan":
		return r.runBackfillScan(args[1:])
	case "run":
		return r.runBackfillRun(args[1:])
	case "status":
		return r.runBackfillStatus(args[1:])
	default:
		return fmt.Errorf("unknown backfill subcommand %q", args[0])
	}
}

func (r runner) runBackfillScan(args []string) error {
	flags := flag.NewFlagSet("backfill scan", flag.ContinueOnError)
	flags.SetOutput(r.stderr)
	agentName := flags.String("agent", "", "agent session source")
	before := flags.String("before", "", "only include turns before this RFC3339 timestamp or date")
	jsonOut := flags.Bool("json", false, "write JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("backfill scan does not accept positional arguments")
	}
	agent, err := validateBackfillAgent(*agentName)
	if err != nil {
		return err
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	cutoff, err := backfillCutoff(*before, cfg.Agents[agent])
	if err != nil {
		return err
	}
	files, err := sessions.Discover(agent)
	if err != nil {
		return err
	}
	result := struct {
		Agent  string    `json:"agent"`
		Root   string    `json:"root"`
		Before time.Time `json:"before"`
		Files  int       `json:"files"`
		Bytes  int64     `json:"bytes"`
		Turns  int       `json:"turns"`
	}{Agent: agent, Before: cutoff, Files: len(files)}
	result.Root, _ = sessions.Root(agent)
	for _, file := range files {
		result.Bytes += file.Size
		turns, readErr := sessions.ReadFile(agent, file.Path, cutoff)
		if readErr != nil {
			return readErr
		}
		result.Turns += len(turns)
	}
	if *jsonOut {
		return writeJSON(r.stdout, result)
	}
	fmt.Fprintf(r.stdout, "Backfill scan: agent=%s files=%d turns=%d size=%s before=%s\n", result.Agent, result.Files, result.Turns, formatBytes(result.Bytes), cutoff.Format(time.RFC3339))
	return nil
}

func (r runner) runBackfillRun(args []string) error {
	runArgs, err := r.parseBackfillRunArgs(args)
	if err != nil {
		return err
	}
	agent, err := validateBackfillAgent(runArgs.agentName)
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	provider := strings.TrimSpace(runArgs.providerName)
	if provider == "" {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, errors.New("backfill run requires --provider"))
	}
	interval, err := parseBackfillRate(runArgs.rate)
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	cfg, service, err := r.loadRuntime()
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	providerCfg, ok := cfg.Providers[provider]
	if !ok || !providerCfg.Enabled {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, fmt.Errorf("provider %q is not enabled", provider))
	}
	cutoff, err := backfillCutoff(runArgs.before, cfg.Agents[agent])
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	if runArgs.backgroundMode && !runArgs.backgroundWorker {
		return r.startBackgroundBackfill(agent, provider, cutoff, runArgs.rate, runArgs.maxDuration)
	}
	files, err := sessions.Discover(agent)
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	store, err := backfill.Open(backfillStateDir(cfg, r.configFile()))
	if err != nil {
		return r.finishBackfillWorkerStart(runArgs.startResultPath, err)
	}
	defer store.Close()
	options := r.backfillRunOptions(runArgs, agent, provider, files, cutoff, interval)
	ctx, cleanup := backfillRunContext(runArgs.maxDuration)
	defer cleanup()
	status, runErr := (backfill.Runner{Store: store, Service: service}).Run(ctx, options)
	return r.finishBackfillRun(runArgs, status, runErr)
}

func (r runner) parseBackfillRunArgs(args []string) (backfillRunArgs, error) {
	flags := flag.NewFlagSet("backfill run", flag.ContinueOnError)
	flags.SetOutput(r.stderr)
	agentName := flags.String("agent", "", "agent session source")
	providerName := flags.String("provider", "", "exact target provider instance")
	before := flags.String("before", "", "only include turns before this RFC3339 timestamp or date")
	rate := flags.String("rate", "30/m", "maximum upload rate, for example 30/m or 1/s")
	maxDuration := flags.Duration("max-duration", 0, "pause after this duration")
	backgroundMode := flags.Bool("background", false, "run silently in the background")
	backgroundWorker := flags.Bool("background-worker", false, "internal background worker")
	runID := flags.String("run-id", "", "internal run id")
	startResultPath := flags.String("start-result", "", "internal background startup result")
	if err := flags.Parse(args); err != nil {
		return backfillRunArgs{}, err
	}
	if flags.NArg() != 0 {
		return backfillRunArgs{}, errors.New("backfill run does not accept positional arguments")
	}
	return backfillRunArgs{
		agentName:        *agentName,
		providerName:     *providerName,
		before:           *before,
		rate:             *rate,
		maxDuration:      *maxDuration,
		backgroundMode:   *backgroundMode,
		backgroundWorker: *backgroundWorker,
		runID:            *runID,
		startResultPath:  *startResultPath,
	}, nil
}

func backfillRunContext(maxDuration time.Duration) (context.Context, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	if maxDuration <= 0 {
		return ctx, stop
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, maxDuration)
	return timeoutCtx, func() {
		cancel()
		stop()
	}
}

func (r runner) backfillRunOptions(args backfillRunArgs, agent, provider string, files []sessions.File, cutoff time.Time, interval time.Duration) backfill.RunOptions {
	runID := args.runID
	if runID == "" {
		runID = backfill.NewRunID()
	}
	mode := "foreground"
	if args.backgroundWorker {
		mode = "background"
	}
	options := backfill.RunOptions{
		Scope:        backfill.Scope(r.configFile(), agent, provider),
		RunID:        runID,
		Mode:         mode,
		Agent:        agent,
		Provider:     provider,
		Files:        files,
		Cutoff:       cutoff,
		RateInterval: interval,
	}
	if args.backgroundWorker {
		options.Started = func(status backfill.Status) {
			_ = writeBackfillStartResult(args.startResultPath, backfillStartResult{Started: true, RunID: status.RunID})
		}
	} else {
		options.Progress = newBackfillProgressWriter(r.stdout)
	}
	return options
}

func (r runner) finishBackfillRun(args backfillRunArgs, status backfill.Status, runErr error) error {
	if args.backgroundWorker && status.StartedAt.IsZero() {
		_ = writeBackfillStartResult(args.startResultPath, backfillStartResult{Error: errorText(runErr)})
	}
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		if !args.backgroundWorker {
			fmt.Fprintf(r.stdout, "Backfill paused: uploaded=%d skipped=%d failed=%d\n", status.Uploaded, status.Skipped, status.Failed)
		}
		return nil
	}
	if runErr != nil {
		return runErr
	}
	if !args.backgroundWorker {
		fmt.Fprintf(r.stdout, "Backfill complete: uploaded=%d skipped=%d failed=%d\n", status.Uploaded, status.Skipped, status.Failed)
	}
	return nil
}

func (r runner) runBackfillStatus(args []string) error {
	flags := flag.NewFlagSet("backfill status", flag.ContinueOnError)
	flags.SetOutput(r.stderr)
	agentName := flags.String("agent", "", "agent session source")
	providerName := flags.String("provider", "", "target provider instance")
	jsonOut := flags.Bool("json", false, "write JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	agent, err := validateBackfillAgent(*agentName)
	if err != nil {
		return err
	}
	provider := strings.TrimSpace(*providerName)
	if provider == "" {
		return errors.New("backfill status requires --provider")
	}
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	store, err := backfill.Open(backfillStateDir(cfg, r.configFile()))
	if err != nil {
		return err
	}
	defer store.Close()
	status, err := store.ReadStatus(backfill.Scope(r.configFile(), agent, provider))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no backfill status found for this agent and provider")
		}
		return err
	}
	if *jsonOut {
		return writeJSON(r.stdout, status)
	}
	writeBackfillStatus(r.stdout, status, false)
	return nil
}

func (r runner) startBackgroundBackfill(agent, provider string, cutoff time.Time, rate string, maxDuration time.Duration) error {
	cfg, err := config.Load(r.configFile())
	if err != nil {
		return err
	}
	stateDir := backfillStateDir(cfg, r.configFile())
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	runID := backfill.NewRunID()
	scope := backfill.Scope(r.configFile(), agent, provider)
	startResultPath := filepath.Join(stateDir, "start-"+runID+".json")
	logPath := filepath.Join(stateDir, "backfill-"+scope+".log")
	binaryPath, err := os.Executable()
	if err != nil || binaryPath == "" {
		binaryPath = "paxm"
	}
	commandArgs := []string{
		"--config", r.configFile(), "backfill", "run",
		"--agent", agent,
		"--provider", provider,
		"--before", cutoff.Format(time.RFC3339Nano),
		"--rate", rate,
		"--background-worker",
		"--run-id", runID,
		"--start-result", startResultPath,
	}
	if maxDuration > 0 {
		commandArgs = append(commandArgs, "--max-duration", maxDuration.String())
	}
	command := exec.Command(binaryPath, commandArgs...)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	detachCommand(command)
	if err := command.Start(); err != nil {
		return err
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resultBytes, readErr := os.ReadFile(startResultPath)
		if readErr == nil {
			var result backfillStartResult
			if json.Unmarshal(resultBytes, &result) == nil {
				_ = os.Remove(startResultPath)
				_ = command.Process.Release()
				if result.Error != "" {
					if strings.Contains(result.Error, backfill.ErrAlreadyRunning.Error()) {
						fmt.Fprintf(r.stdout, "Backfill already running for agent=%s provider=%s\n", agent, provider)
						return nil
					}
					return errors.New(result.Error)
				}
				fmt.Fprintf(r.stdout, "Background backfill started: run=%s agent=%s provider=%s\n", result.RunID, agent, provider)
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Release()
	return fmt.Errorf("background backfill did not report startup; see %s", logPath)
}

func (r runner) finishBackfillWorkerStart(path string, err error) error {
	if strings.TrimSpace(path) != "" {
		_ = writeBackfillStartResult(path, backfillStartResult{Error: errorText(err)})
	}
	return err
}

func writeBackfillStartResult(path string, result backfillStartResult) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	bytes, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(bytes, '\n'), 0o600)
}

func validateBackfillAgent(value string) (string, error) {
	agent := normalizeAgentName(value)
	for _, supported := range supportedPassiveAgents {
		if agent == supported {
			return agent, nil
		}
	}
	return "", fmt.Errorf("unsupported agent %q; expected codex, claude, or pi", value)
}

func backfillCutoff(value string, agent config.AgentConfig) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(agent.PassiveWriteStartedAt)
		if value == "" {
			return time.Time{}, errors.New("backfill requires --before because this agent has no recorded integration time")
		}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid --before %q; use RFC3339 or YYYY-MM-DD", value)
}

func parseBackfillRate(value string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid --rate %q; use a value such as 30/m or 1/s", value)
	}
	count, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || count <= 0 {
		return 0, fmt.Errorf("invalid --rate %q; count must be positive", value)
	}
	var period time.Duration
	switch strings.ToLower(parts[1]) {
	case "s", "sec", "second":
		period = time.Second
	case "m", "min", "minute":
		period = time.Minute
	case "h", "hour":
		period = time.Hour
	default:
		return 0, fmt.Errorf("invalid --rate %q; unit must be s, m, or h", value)
	}
	return time.Duration(float64(period) / count), nil
}

func backfillStateDir(cfg config.Config, configPath string) string {
	stateDir := strings.TrimSpace(cfg.Telemetry.Dir)
	if stateDir == "" {
		if config.ExpandPath(configPath) == config.DefaultConfigPath() {
			stateDir = config.DefaultStateDir()
		} else {
			stateDir = filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "state")
		}
	}
	return filepath.Join(stateDir, "backfill")
}

func newBackfillProgressWriter(writer io.Writer) func(backfill.Status) {
	lastWrite := time.Time{}
	return func(status backfill.Status) {
		finished := status.State != "running"
		if !finished && !lastWrite.IsZero() && time.Since(lastWrite) < 250*time.Millisecond {
			return
		}
		writeBackfillStatus(writer, status, true)
		lastWrite = time.Now()
	}
}

func writeBackfillStatus(writer io.Writer, status backfill.Status, progress bool) {
	percent := 100.0
	if status.TotalBytes > 0 {
		percent = float64(status.ProcessedBytes) * 100 / float64(status.TotalBytes)
	}
	eta := "--"
	if status.ETASeconds > 0 {
		eta = (time.Duration(status.ETASeconds) * time.Second).Round(time.Second).String()
	}
	prefix := "Backfill status"
	if progress {
		prefix = "Backfill progress"
	}
	fmt.Fprintf(writer, "%s: state=%s %.1f%% files=%d/%d uploaded=%d skipped=%d failed=%d speed=%.2f items/s ETA=%s\n",
		prefix, status.State, percent, status.ProcessedFiles, status.TotalFiles, status.Uploaded, status.Skipped, status.Failed, status.ItemsPerSecond, eta)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for amount := value / unit; amount >= unit; amount /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
