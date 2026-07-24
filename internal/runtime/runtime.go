package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pax-beehive/paxm/internal/adapters"
	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/operator"
	"github.com/pax-beehive/paxm/internal/sessionsequence"
	"github.com/pax-beehive/paxm/internal/tools"
)

type Runtime struct {
	ConfigPath string
	Config     config.Config
	Tools      tools.Agent
	Capture    *capture.Service
	Operator   *operator.Service
	router     *memory.Router
}

func ConfigFile(configPath string) string {
	if configPath != "" {
		return config.ExpandPath(configPath)
	}
	return config.DefaultConfigPath()
}

func Load(configPath string) (*Runtime, error) {
	return LoadWithClock(configPath, nil)
}

// LoadWithClock is Load with an injectable clock threaded through the router,
// facade, and tools engine.
func LoadWithClock(configPath string, clock memory.Clock) (*Runtime, error) {
	path := ConfigFile(configPath)
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigMissing) {
			return nil, fmt.Errorf("%w; run `paxm --config %s setup`", err, path)
		}
		return nil, err
	}
	sequenceStore, err := sessionsequence.Open(sessionSequenceStorePath(path, cfg))
	if err != nil {
		return nil, err
	}
	router, err := adapters.DefaultRegistry().BuildRouterWithClock(cfg, clock, memory.WithSequenceAllocator(sequenceStore))
	if err != nil {
		_ = sequenceStore.Close()
		return nil, err
	}
	core := facade.NewWithClock(cfg, router, clock)
	engine := core.Tools()
	return &Runtime{
		ConfigPath: path,
		Config:     cfg,
		Tools:      engine,
		Capture:    capture.New(core),
		Operator:   operator.New(path, cfg, engine, router),
		router:     router,
	}, nil
}

func sessionSequenceStorePath(configPath string, cfg config.Config) string {
	stateDir := strings.TrimSpace(cfg.Telemetry.Dir)
	if stateDir == "" {
		if config.ExpandPath(configPath) == config.DefaultConfigPath() {
			stateDir = config.DefaultStateDir()
		} else {
			stateDir = filepath.Join(filepath.Dir(config.ExpandPath(configPath)), "state")
		}
	}
	return filepath.Join(config.ExpandPath(stateDir), "session-sequences.sqlite")
}

func (r *Runtime) Health(ctx context.Context) ([]memory.ProviderHealth, error) {
	return r.router.Health(ctx)
}

func (r *Runtime) Close() error {
	return r.router.Close()
}
