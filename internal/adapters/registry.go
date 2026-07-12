package adapters

import (
	"fmt"
	"sort"

	jsonrpcadapter "github.com/pax-beehive/paxm/internal/adapters/jsonrpc"
	mem0adapter "github.com/pax-beehive/paxm/internal/adapters/mem0"
	sqliteadapter "github.com/pax-beehive/paxm/internal/adapters/sqlite"
	zepadapter "github.com/pax-beehive/paxm/internal/adapters/zep"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
)

type Factory func(name string, cfg config.ProviderConfig) (memory.Provider, error)

type Registry struct {
	factories map[string]Factory
}

func DefaultRegistry() Registry {
	registry := Registry{factories: make(map[string]Factory)}
	registry.Register("sqlite", func(name string, cfg config.ProviderConfig) (memory.Provider, error) {
		return sqliteadapter.New(name, cfg.Path)
	})
	registry.Register("zep", func(name string, cfg config.ProviderConfig) (memory.Provider, error) {
		return zepadapter.New(name, cfg)
	})
	registry.Register("mem0", func(name string, cfg config.ProviderConfig) (memory.Provider, error) {
		return mem0adapter.New(name, cfg)
	})
	registry.Register("jsonrpc", func(name string, cfg config.ProviderConfig) (memory.Provider, error) {
		return jsonrpcadapter.New(name, cfg)
	})
	return registry
}

func (r Registry) Register(providerType string, factory Factory) {
	r.factories[providerType] = factory
}

func (r Registry) BuildProvider(name string, cfg config.ProviderConfig) (memory.Provider, error) {
	factory, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("provider %q uses unsupported type %q", name, cfg.Type)
	}
	provider, err := factory(name, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	return provider, nil
}

func (r Registry) BuildRouter(cfg config.Config) (*memory.Router, error) {
	var names []string
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var bindings []memory.ProviderBinding
	for _, name := range names {
		providerCfg := cfg.Providers[name]
		if !providerCfg.Enabled {
			continue
		}
		provider, err := r.BuildProvider(name, providerCfg)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, memory.ProviderBinding{
			Provider: provider,
			Read:     true,
			Write:    true,
			Required: providerRequiredByAnyProfile(cfg, name),
			Weight:   1,
		})
	}
	return memory.NewRouter(bindings)
}

func providerRequiredByAnyProfile(cfg config.Config, providerName string) bool {
	for _, profile := range cfg.RecallProfiles {
		for _, route := range profile.Providers {
			if route.Name == providerName && route.Required {
				return true
			}
		}
	}
	for _, profile := range cfg.WriteProfiles {
		for _, route := range profile.Providers {
			if route.Name == providerName && route.Required {
				return true
			}
		}
	}
	return false
}
