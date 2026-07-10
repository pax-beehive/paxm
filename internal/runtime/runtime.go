package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/pax-beehive/memory-adaptor/internal/adapters"
	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/facade"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

type Runtime struct {
	ConfigPath string
	Config     config.Config
	Service    *facade.Service
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
	return &Runtime{
		ConfigPath: path,
		Config:     cfg,
		Service:    facade.New(cfg, router),
		router:     router,
	}, nil
}

func (r *Runtime) Health(ctx context.Context) ([]memory.ProviderHealth, error) {
	return r.router.Health(ctx)
}
