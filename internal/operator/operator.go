package operator

import (
	"context"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

// Service is the human/operator interface shared by CLI and the future desktop
// adapter. It is intentionally separate from least-privilege agent tools.
type Service struct {
	configPath string
	cfg        config.Config
	engine     *tools.Engine
	router     *memory.Router
}

func New(configPath string, cfg config.Config, engine *tools.Engine, router *memory.Router) *Service {
	return &Service{configPath: configPath, cfg: cfg, engine: engine, router: router}
}
func (s *Service) Config() config.Config { return s.cfg }
func (s *Service) Health(ctx context.Context) ([]memory.ProviderHealth, error) {
	return s.router.Health(ctx)
}
func (s *Service) RememberBatchToProvider(ctx context.Context, provider string, input tools.RememberBatchInput) (tools.RememberResult, error) {
	return s.engine.RememberBatchToProvider(ctx, provider, input)
}
func (s *Service) RememberBatch(ctx context.Context, input tools.RememberBatchInput) (tools.RememberResult, error) {
	return s.engine.RememberBatch(ctx, input)
}
func (s *Service) CleanupExpired(ctx context.Context, limit int) (memory.CleanupExpiredResult, error) {
	return s.engine.CleanupExpired(ctx, limit)
}
func (s *Service) History(days int) (telemetry.HistorySummary, error) {
	return telemetry.NewRecorder(s.cfg.Telemetry, s.configPath).History(days)
}
func (s *Service) TailEvents(limit int) ([]telemetry.Event, error) {
	return telemetry.NewRecorder(s.cfg.Telemetry, s.configPath).TailEvents(limit)
}
func (s *Service) FollowEvents(ctx context.Context, limit int, interval time.Duration, emit func(telemetry.Event) error) error {
	return telemetry.NewRecorder(s.cfg.Telemetry, s.configPath).FollowEvents(ctx, limit, interval, emit)
}
