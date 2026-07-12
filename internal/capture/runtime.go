package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pax-beehive/paxm/internal/capturequeue"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/telemetry"
	"github.com/pax-beehive/paxm/internal/tools"
)

type Command struct {
	Action  string
	EventID string
	Event   Event
}

type Receipt struct {
	Buffered bool
	Flushed  int
	Shutdown bool
}

type Policy interface {
	WriteItem(Event) (tools.RememberInput, bool, error)
	BufferConfig(Event) config.HookBufferConfig
}

// Runtime owns the durable passive-write workflow behind one Process interface.
type Runtime struct {
	policy          Policy
	queue           *capturequeue.Queue
	notifyDelivery  func()
	scheduleCleanup func()
	close           func()
}

type Operator interface {
	RememberBatchToProvider(context.Context, string, tools.RememberBatchInput) (tools.RememberResult, error)
	CleanupExpired(context.Context, int) (memory.CleanupExpiredResult, error)
}
type OpenOptions struct {
	Config    config.Config
	QueuePath string
	Policy    Policy
	Operator  Operator
	Record    func(telemetry.Event)
}

func Open(options OpenOptions) (*Runtime, error) {
	if err := validateOpenOptions(options); err != nil {
		return nil, err
	}
	maxAge, retryMin, err := queueDurations(options.Config)
	if err != nil {
		return nil, err
	}
	queue, err := capturequeue.Open(options.QueuePath, queueOptions(options, maxAge, retryMin))
	if err != nil {
		return nil, err
	}
	reportWorkerError := workerErrorReporter(options.Record)
	delivery, cleanup := startWorkers(options.Operator, queue, reportWorkerError)
	runtime := NewRuntime(options.Policy, queue, delivery.Notify, cleanup.Schedule)
	runtime.close = func() { closeWorkers(queue, delivery, cleanup, reportWorkerError) }
	return runtime, nil
}

func validateOpenOptions(options OpenOptions) error {
	if options.Policy == nil {
		return errors.New("capture policy is required")
	}
	if options.Operator == nil {
		return errors.New("capture operator is required")
	}
	return nil
}

func queueDurations(cfg config.Config) (time.Duration, time.Duration, error) {
	maxAge, err := parseOptionalDuration("capture_queue.max_episode_age", cfg.CaptureQueue.MaxEpisodeAge)
	if err != nil {
		return 0, 0, err
	}
	retryMin, err := parseOptionalDuration("capture_queue.retry_min", cfg.CaptureQueue.RetryMin)
	return maxAge, retryMin, err
}

func queueOptions(options OpenOptions, maxAge, retryMin time.Duration) capturequeue.Options {
	return capturequeue.Options{
		MaxEpisodeAge:       maxAge,
		RetryMin:            retryMin,
		MaxAttempts:         options.Config.CaptureQueue.MaxAttempts,
		Providers:           providerNames(options.Config),
		ProviderConcurrency: providerConcurrency(options.Config),
		Deliver:             deliveryFunc(options.Operator),
		OnDelivery: func(outcome capturequeue.DeliveryOutcome) {
			if options.Record != nil {
				options.Record(deliveryEvent(outcome))
			}
		},
	}
}

func providerNames(cfg config.Config) func(string) []string {
	return func(profile string) []string {
		if strings.TrimSpace(profile) == "" {
			profile = "default"
		}
		value, ok := cfg.WriteProfiles[profile]
		if !ok {
			return nil
		}
		result := make([]string, 0, len(value.Providers))
		for _, route := range value.Providers {
			result = append(result, route.Name)
		}
		return result
	}
}

func providerConcurrency(cfg config.Config) func(string) int {
	return func(provider string) int {
		if value := cfg.CaptureQueue.ProviderConcurrency[provider]; value > 0 {
			return value
		}
		return cfg.CaptureQueue.ProviderConcurrency["default"]
	}
}

func deliveryFunc(operator Operator) func(context.Context, string, capturequeue.Episode) (string, error) {
	return func(ctx context.Context, provider string, episode capturequeue.Episode) (string, error) {
		result, err := operator.RememberBatchToProvider(ctx, provider, tools.RememberBatchInput{Items: episode.IngestInputs()})
		if len(result.Refs) == 0 {
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("provider %s returned no memory reference", provider)
		}
		return result.Refs[0].ID, err
	}
}

func workerErrorReporter(record func(telemetry.Event)) func(string, error) {
	return func(kind string, workerErr error) {
		if record == nil || workerErr == nil {
			return
		}
		record(telemetry.Event{Time: time.Now().UTC(), Kind: "hook_delivery", Source: "capture_queue", Command: "hook", HookEvent: kind, Success: false, Error: truncateError(workerErr)})
	}
}

func startWorkers(operator Operator, queue *capturequeue.Queue, report func(string, error)) (*worker, *cleanupWorker) {
	delivery := newWorker(queue, func(workerErr error) { report("delivery_worker_error", workerErr) })
	cleanup := newCleanupWorker(func(ctx context.Context) error {
		_, cleanupErr := operator.CleanupExpired(ctx, 500)
		return cleanupErr
	}, func(workerErr error) { report("cleanup_worker_error", workerErr) })
	return delivery, cleanup
}

func closeWorkers(queue *capturequeue.Queue, delivery *worker, cleanup *cleanupWorker, report func(string, error)) {
	cleanup.Close()
	if delivery.Close() {
		if closeErr := queue.Close(); closeErr != nil {
			report("queue_close_error", closeErr)
		}
		return
	}
	report("delivery_shutdown_timeout", errors.New("capture delivery worker did not stop within 1s; queue close deferred"))
	go func() {
		<-delivery.done
		if closeErr := queue.Close(); closeErr != nil {
			report("queue_close_error", closeErr)
		}
	}()
}

func parseOptionalDuration(name, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s is invalid: %w", name, err)
	}
	return duration, nil
}
func (r *Runtime) Close() {
	if r.close != nil {
		r.close()
		r.close = nil
	}
}

func deliveryEvent(outcome capturequeue.DeliveryOutcome) telemetry.Event {
	hookEvent := "delivery"
	if outcome.Dead {
		hookEvent = "delivery_dead"
	}
	event := telemetry.Event{Time: time.Now().UTC(), Kind: "hook_delivery", Source: "capture_queue", Command: "hook", HookEvent: hookEvent, Success: outcome.Err == nil, DurationMS: outcome.Duration.Milliseconds(), ProviderDurationMS: outcome.Duration.Milliseconds(), PassiveWriteLatencyTotalMS: outcome.PassiveWriteLatencyTotal.Milliseconds(), PassiveWriteSamples: outcome.PassiveWriteSamples, ItemCount: 1, EpisodeID: outcome.EpisodeID, SessionKey: outcome.SessionKey, Provider: outcome.Provider}
	if outcome.Err != nil {
		event.Error = truncateError(outcome.Err)
		event.ProviderErrorDetails = []telemetry.ProviderErrorDetail{{Provider: outcome.Provider, Op: "put"}}
	}
	if outcome.Ref != "" {
		event.RefCount = 1
		event.ProviderWrites = map[string]int{outcome.Provider: 1}
		event.ProviderRefs = map[string]int{outcome.Provider: 1}
	}
	return event
}
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

type worker struct {
	queue   *capturequeue.Queue
	notify  chan struct{}
	stop    chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	onError func(error)
}

func newWorker(queue *capturequeue.Queue, onError func(error)) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	value := &worker{queue: queue, notify: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), cancel: cancel, onError: onError}
	go func() {
		defer close(value.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-value.notify:
				value.runOnce(ctx)
			case <-ticker.C:
				value.sealExpired(ctx)
			case <-value.stop:
				return
			}
		}
	}()
	return value
}

func (w *worker) runOnce(ctx context.Context) {
	if _, err := w.queue.RunOnce(ctx); err != nil {
		w.handleError(ctx, err)
	}
}

func (w *worker) sealExpired(ctx context.Context) {
	if _, err := w.queue.SealExpired(ctx); err != nil {
		w.report(err)
	}
	w.runOnce(ctx)
}

func (w *worker) handleError(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	w.report(err)
	if recoverErr := w.queue.RecoverDelivering(context.Background()); recoverErr != nil {
		w.report(fmt.Errorf("recover capture deliveries: %w", recoverErr))
	}
}

func (w *worker) report(err error) {
	if w.onError != nil {
		w.onError(err)
	}
}
func (w *worker) Notify() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}
func (w *worker) Close() bool {
	w.cancel()
	close(w.stop)
	select {
	case <-w.done:
		return true
	case <-time.After(time.Second):
		return false
	}
}

type cleanupWorker struct {
	run      func(context.Context) error
	onError  func(error)
	requests chan struct{}
	done     chan struct{}
	once     sync.Once
}

func newCleanupWorker(run func(context.Context) error, onError func(error)) *cleanupWorker {
	w := &cleanupWorker{run: run, onError: onError, requests: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for range w.requests {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := w.run(ctx); err != nil && w.onError != nil {
				w.onError(err)
			}
			cancel()
		}
	}()
	return w
}
func (w *cleanupWorker) Schedule() {
	select {
	case w.requests <- struct{}{}:
	default:
	}
}
func (w *cleanupWorker) Close() { w.once.Do(func() { close(w.requests); <-w.done }) }

func NewRuntime(policy Policy, queue *capturequeue.Queue, notifyDelivery, scheduleCleanup func()) *Runtime {
	if notifyDelivery == nil {
		notifyDelivery = func() {}
	}
	if scheduleCleanup == nil {
		scheduleCleanup = func() {}
	}
	return &Runtime{policy: policy, queue: queue, notifyDelivery: notifyDelivery, scheduleCleanup: scheduleCleanup}
}

func (r *Runtime) Process(ctx context.Context, command Command) (Receipt, error) {
	if command.Action == "flush" || command.Action == "shutdown" {
		sealed, err := r.queue.SealAll(ctx)
		if err == nil && command.Action == "flush" {
			_, err = r.queue.RunOnce(ctx)
		}
		if err != nil {
			return Receipt{}, err
		}
		r.scheduleCleanup()
		return Receipt{Flushed: sealed, Shutdown: command.Action == "shutdown"}, nil
	}
	event := command.Event
	if command.EventID != "" {
		if event.Metadata == nil {
			event.Metadata = map[string]string{}
		}
		event.Metadata["event_id"] = command.EventID
	}
	item, ok, err := r.policy.WriteItem(event)
	if err != nil || !ok {
		return Receipt{}, err
	}
	bufferCfg := r.policy.BufferConfig(event)
	_, err = r.queue.Append(ctx, capturequeue.Event{ID: strings.TrimSpace(event.Metadata["event_id"]), SessionKey: SessionKey(event), Terminal: bufferCfg.Flush, Sequence: sequence(event.Metadata, "event_sequence", "sequence"), Final: sequence(event.Metadata, "final_sequence"), Item: item})
	if err != nil {
		return Receipt{}, err
	}
	r.notifyDelivery()
	flushed := 0
	if bufferCfg.Flush {
		flushed = 1
	}
	return Receipt{Buffered: true, Flushed: flushed}, nil
}

func SessionKey(event Event) string {
	target := first(event.Target, "codex")
	workspace := first(event.Workspace, event.Metadata["cwd"], "unknown")
	if id := strings.TrimSpace(event.Metadata["session_id"]); id != "" {
		return target + "/workspace/" + workspace + "/session/" + id
	}
	if transcript := strings.TrimSpace(event.Metadata["transcript_path"]); transcript != "" {
		return target + "/workspace/" + workspace + "/transcript/" + transcript
	}
	return target + "/workspace/" + workspace + "/event/" + first(event.Metadata["event_id"], "unknown")
}

func sequence(metadata map[string]string, keys ...string) *int64 {
	for _, key := range keys {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			var parsed int64
			for _, char := range value {
				if char < '0' || char > '9' {
					parsed = 0
					break
				}
				parsed = parsed*10 + int64(char-'0')
			}
			if parsed > 0 {
				return &parsed
			}
		}
	}
	return nil
}
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
