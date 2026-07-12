package operator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/telemetry"
)

func TestServiceConfigurationAndObservationSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.DefaultConfig(path)
	enabled := true
	cfg.Telemetry.Enabled = &enabled
	cfg.Telemetry.Dir = filepath.Join(t.TempDir(), "telemetry")

	service := New(path, cfg, nil, nil)
	if service.Config().Version == 0 {
		t.Fatal("Config() returned an unnormalized config")
	}
	if _, err := service.History(7); err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if events, err := service.TailEvents(10); err != nil || len(events) != 0 {
		t.Fatalf("TailEvents() = %#v, err=%v", events, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.FollowEvents(ctx, 0, time.Millisecond, func(telemetry.Event) error { return nil }); err != nil {
		t.Fatalf("FollowEvents() error = %v", err)
	}
}
