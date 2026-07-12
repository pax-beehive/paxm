package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/config"
	paxeval "github.com/pax-beehive/paxm/internal/eval"
	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/tools"
)

const (
	defaultVersion                = "dev"
	legacyDefaultRecallMaxResults = 8
)

type ensureZepUserFunc func(context.Context, config.ProviderConfig) (zepadapter.EnsureUserResult, error)
type shutdownHookDaemonFunc func(string) error

type Dependencies struct {
	Version            string
	EnsureZepUser      ensureZepUserFunc
	ShutdownHookDaemon shutdownHookDaemonFunc
	AgentExecutor      paxeval.AgentExecutor
}

type runner struct {
	stdin              io.Reader
	stdout             io.Writer
	stderr             io.Writer
	configPath         string
	version            string
	ensureZepUser      ensureZepUserFunc
	shutdownHookDaemon shutdownHookDaemonFunc
	agentExecutor      paxeval.AgentExecutor
}

func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return MainWithDependencies(args, stdin, stdout, stderr, Dependencies{})
}

func MainWithDependencies(args []string, stdin io.Reader, stdout, stderr io.Writer, deps Dependencies) int {
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
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	deps = deps.withDefaults()
	r := runner{
		stdin:              stdin,
		stdout:             stdout,
		stderr:             stderr,
		configPath:         configPath,
		version:            deps.Version,
		ensureZepUser:      deps.EnsureZepUser,
		shutdownHookDaemon: deps.ShutdownHookDaemon,
		agentExecutor:      deps.AgentExecutor,
	}
	if len(args) == 0 {
		r.printHelp()
		return 0
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		r.printHelp()
		return 0
	}
	if args[0] == "--version" || args[0] == "-v" {
		_, _ = fmt.Fprintln(stdout, r.versionString())
		return 0
	}
	if err := r.run(args); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func (deps Dependencies) withDefaults() Dependencies {
	if strings.TrimSpace(deps.Version) == "" {
		deps.Version = defaultVersion
	}
	if deps.EnsureZepUser == nil {
		deps.EnsureZepUser = zepadapter.EnsureUser
	}
	if deps.ShutdownHookDaemon == nil {
		deps.ShutdownHookDaemon = func(configPath string) error {
			return flushExistingHookBuffer(configPath, true)
		}
	}
	return deps
}

func (r runner) versionString() string {
	if strings.TrimSpace(r.version) != "" {
		return r.version
	}
	return defaultVersion
}

func (r runner) ensureZepUserFunc() ensureZepUserFunc {
	if r.ensureZepUser != nil {
		return r.ensureZepUser
	}
	return zepadapter.EnsureUser
}

func (r runner) shutdownHookDaemonFunc() shutdownHookDaemonFunc {
	if r.shutdownHookDaemon != nil {
		return r.shutdownHookDaemon
	}
	return func(configPath string) error {
		return flushExistingHookBuffer(configPath, true)
	}
}

func (r runner) loadRuntime() (config.Config, *paxruntime.Runtime, error) {
	rt, err := paxruntime.Load(r.configFile())
	if err != nil {
		return config.Config{}, nil, err
	}
	return rt.Config, rt, nil
}

func (r runner) loadService() (tools.Agent, error) {
	_, rt, err := r.loadRuntime()
	if err != nil {
		return nil, err
	}
	return rt.Tools, nil
}

func (r runner) configFile() string {
	return paxruntime.ConfigFile(r.configPath)
}

func (r runner) printHelp() {
	_, _ = fmt.Fprintln(r.stdout, "paxm - memory adapter CLI")
	_, _ = fmt.Fprintln(r.stdout)
	_, _ = fmt.Fprintln(r.stdout, "Usage:")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] setup [--integration paxm|codex-plugin|claude-plugin]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] uninstall [--agent AGENT] [--yes]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] recall --query TEXT [--limit N] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] remember --profile stm|ltm --text TEXT")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] history [--days N] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] logs [--tail N] [--follow] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill scan --agent AGENT [--before TIME]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill run --agent AGENT --provider NAME [--background]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] backfill status --agent AGENT --provider NAME")
	_, _ = fmt.Fprintln(r.stdout, "  paxm eval run [--suite PATH] [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm eval run locomo --dataset PATH --agent NAME --provider NAME (--max-questions N | --all)")
	_, _ = fmt.Fprintln(r.stdout, "  paxm eval retrieval locomo --dataset PATH --provider NAME [--json]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm eval cleanup (--run RUN_ID | --stale)")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] mcp serve")
	_, _ = fmt.Fprintln(r.stdout, "  paxm update [--check] [--version VERSION]")
	_, _ = fmt.Fprintln(r.stdout, "  paxm [--config PATH] config doctor")
	_, _ = fmt.Fprintln(r.stdout, "  paxm version")
}
