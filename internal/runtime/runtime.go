package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/pax-beehive/paxm/internal/adapters"
	"github.com/pax-beehive/paxm/internal/capture"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/operator"
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
	path := ConfigFile(configPath)
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigMissing) {
			return nil, fmt.Errorf("%w; run `paxm --config %s setup`", err, path)
		}
		return nil, err
	}
	router, err := adapters.DefaultRegistry().BuildRouter(cfg)
	if err != nil {
		return nil, err
	}
	core := facade.New(cfg, router)
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

func (r *Runtime) Health(ctx context.Context) ([]memory.ProviderHealth, error) {
	return r.router.Health(ctx)
}
