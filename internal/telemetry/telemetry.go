package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pax-beehive/memory-adaptor/internal/config"
	"github.com/pax-beehive/memory-adaptor/internal/memory"
)

const metricsVersion = 1

type Event struct {
	Time                       time.Time             `json:"time"`
	Kind                       string                `json:"kind"`
	Source                     string                `json:"source,omitempty"`
	Command                    string                `json:"command,omitempty"`
	Target                     string                `json:"target,omitempty"`
	HookEvent                  string                `json:"hook_event,omitempty"`
	Profile                    string                `json:"profile,omitempty"`
	Success                    bool                  `json:"success"`
	Skipped                    bool                  `json:"skipped,omitempty"`
	DurationMS                 int64                 `json:"duration_ms,omitempty"`
	ProviderDurationMS         int64                 `json:"provider_duration_ms,omitempty"`
	PassiveWriteLatencyTotalMS int64                 `json:"passive_write_latency_total_ms,omitempty"`
	PassiveWriteSamples        int                   `json:"passive_write_samples,omitempty"`
	QueryHash                  string                `json:"query_hash,omitempty"`
	QueryLength                int                   `json:"query_length,omitempty"`
	QueryPreview               string                `json:"query_preview,omitempty"`
	HitCount                   int                   `json:"hit_count,omitempty"`
	InsertedCount              int                   `json:"inserted_count,omitempty"`
	ItemCount                  int                   `json:"item_count,omitempty"`
	RefCount                   int                   `json:"ref_count,omitempty"`
	Flushed                    int                   `json:"flushed,omitempty"`
	ProviderRecalls            map[string]int        `json:"provider_recalls,omitempty"`
	ProviderWrites             map[string]int        `json:"provider_writes,omitempty"`
	ProviderHits               map[string]int        `json:"provider_hits,omitempty"`
	ProviderRefs               map[string]int        `json:"provider_refs,omitempty"`
	ProviderErrorDetails       []ProviderErrorDetail `json:"provider_errors,omitempty"`
	Error                      string                `json:"error,omitempty"`
	EpisodeID                  string                `json:"episode_id,omitempty"`
	SessionKey                 string                `json:"session_key,omitempty"`
	Provider                   string                `json:"provider,omitempty"`
}

type ProviderErrorDetail struct {
	Provider string `json:"provider"`
	Op       string `json:"op,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Metrics struct {
	Version    int                    `json:"version"`
	UpdatedAt  time.Time              `json:"updated_at"`
	FirstSeen  time.Time              `json:"first_seen,omitempty"`
	Totals     Counter                `json:"totals"`
	Daily      map[string]DailyMetric `json:"daily,omitempty"`
	Agents     map[string]Counter     `json:"agents,omitempty"`
	Profiles   map[string]Counter     `json:"profiles,omitempty"`
	HookEvents map[string]Counter     `json:"hook_events,omitempty"`
	Providers  map[string]Counter     `json:"providers,omitempty"`
}

type DailyMetric struct {
	Counter    Counter            `json:"counter"`
	Agents     map[string]Counter `json:"agents,omitempty"`
	Profiles   map[string]Counter `json:"profiles,omitempty"`
	HookEvents map[string]Counter `json:"hook_events,omitempty"`
	Providers  map[string]Counter `json:"providers,omitempty"`
}

type Counter struct {
	Events                     int   `json:"events"`
	Successes                  int   `json:"successes,omitempty"`
	Errors                     int   `json:"errors,omitempty"`
	Skipped                    int   `json:"skipped,omitempty"`
	Recalls                    int   `json:"recalls,omitempty"`
	Hits                       int   `json:"hits,omitempty"`
	Inserted                   int   `json:"inserted,omitempty"`
	Writes                     int   `json:"writes,omitempty"`
	Items                      int   `json:"items,omitempty"`
	Refs                       int   `json:"refs,omitempty"`
	Flushes                    int   `json:"flushes,omitempty"`
	ProviderErrors             int   `json:"provider_errors,omitempty"`
	DurationMS                 int64 `json:"duration_ms,omitempty"`
	ProviderWriteSamples       int   `json:"provider_write_samples,omitempty"`
	ProviderWriteDurationMS    int64 `json:"provider_write_duration_ms,omitempty"`
	PassiveWriteLatencyTotalMS int64 `json:"passive_write_latency_total_ms,omitempty"`
	PassiveWriteSamples        int   `json:"passive_write_samples,omitempty"`
}

type HistorySummary struct {
	Days       int            `json:"days"`
	Since      time.Time      `json:"since"`
	Until      time.Time      `json:"until"`
	Totals     Counter        `json:"totals"`
	Daily      []DatedCounter `json:"daily,omitempty"`
	Agents     []NamedCounter `json:"agents,omitempty"`
	Profiles   []NamedCounter `json:"profiles,omitempty"`
	HookEvents []NamedCounter `json:"hook_events,omitempty"`
	Providers  []NamedCounter `json:"providers,omitempty"`
	Storage    StorageInfo    `json:"storage"`
}

type DatedCounter struct {
	Date    string  `json:"date"`
	Counter Counter `json:"counter"`
}

type NamedCounter struct {
	Name    string  `json:"name"`
	Counter Counter `json:"counter"`
}

type StorageInfo struct {
	Dir         string `json:"dir"`
	EventsFile  string `json:"events_file"`
	MetricsFile string `json:"metrics_file"`
	EventBytes  int64  `json:"event_bytes"`
	TotalBytes  int64  `json:"total_bytes"`
	MaxBytes    int64  `json:"max_event_file_bytes"`
	MaxFiles    int    `json:"max_event_files"`
}

type Settings struct {
	Enabled             bool
	Dir                 string
	EventsFile          string
	MetricsFile         string
	MaxEventFileBytes   int64
	MaxEventFiles       int
	RetentionDays       int
	CaptureQueryPreview bool
	QueryPreviewChars   int
}

type Recorder struct {
	settings Settings
}

func NewRecorder(cfg config.TelemetryConfig, configPath string) *Recorder {
	settings := EffectiveSettings(cfg, configPath)
	return &Recorder{settings: settings}
}

func EffectiveSettings(cfg config.TelemetryConfig, configPath string) Settings {
	enabled := true
	if cfg.Enabled != nil {
		enabled = *cfg.Enabled
	}
	dir := config.ExpandPath(cfg.Dir)
	if dir == "" {
		dir = defaultTelemetryDir(configPath)
	}
	eventsFile := cfg.EventsFile
	if eventsFile == "" {
		eventsFile = "events.jsonl"
	}
	metricsFile := cfg.MetricsFile
	if metricsFile == "" {
		metricsFile = "metrics.json"
	}
	maxEventFileBytes := cfg.MaxEventFileBytes
	if maxEventFileBytes <= 0 {
		maxEventFileBytes = 1 << 20
	}
	maxEventFiles := cfg.MaxEventFiles
	if maxEventFiles <= 0 {
		maxEventFiles = 3
	}
	retentionDays := cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 30
	}
	queryPreviewChars := cfg.QueryPreviewChars
	if queryPreviewChars <= 0 {
		queryPreviewChars = 80
	}
	return Settings{
		Enabled:             enabled,
		Dir:                 dir,
		EventsFile:          filepath.Base(eventsFile),
		MetricsFile:         filepath.Base(metricsFile),
		MaxEventFileBytes:   maxEventFileBytes,
		MaxEventFiles:       maxEventFiles,
		RetentionDays:       retentionDays,
		CaptureQueryPreview: cfg.CaptureQueryPreview,
		QueryPreviewChars:   queryPreviewChars,
	}
}

func (r *Recorder) Record(event Event) error {
	if r == nil || !r.settings.Enabled {
		return nil
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	} else {
		event.Time = event.Time.UTC()
	}
	if err := os.MkdirAll(r.settings.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockDir(r.settings.Dir)
	if err != nil {
		return err
	}
	defer unlock()

	if err := r.appendEvent(event); err != nil {
		return err
	}
	metrics, err := r.loadMetrics()
	if err != nil {
		return err
	}
	metrics.Update(event, r.settings.RetentionDays)
	return r.saveMetrics(metrics)
}

func (r *Recorder) History(days int) (HistorySummary, error) {
	if r == nil {
		return HistorySummary{Days: days}, nil
	}
	if !r.settings.Enabled {
		return HistorySummary{Days: days, Storage: r.storageInfo()}, nil
	}
	if days <= 0 {
		days = r.settings.RetentionDays
	}
	metrics, err := r.loadMetrics()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return HistorySummary{Days: days, Storage: r.storageInfo()}, nil
		}
		return HistorySummary{}, err
	}
	until := time.Now().UTC()
	since := dateStart(until).AddDate(0, 0, -days+1)
	summary := HistorySummary{
		Days:    days,
		Since:   since,
		Until:   until,
		Storage: r.storageInfo(),
	}
	profiles := make(map[string]Counter)
	agents := make(map[string]Counter)
	hookEvents := make(map[string]Counter)
	providers := make(map[string]Counter)
	for i := 0; i < days; i++ {
		date := since.AddDate(0, 0, i).Format("2006-01-02")
		day, ok := metrics.Daily[date]
		if !ok {
			continue
		}
		summary.Totals.Add(day.Counter)
		summary.Daily = append(summary.Daily, DatedCounter{Date: date, Counter: day.Counter})
		addCounters(agents, day.Agents)
		addCounters(profiles, day.Profiles)
		addCounters(hookEvents, day.HookEvents)
		addCounters(providers, day.Providers)
	}
	summary.Agents = sortedNamedCounters(agents)
	summary.Profiles = sortedNamedCounters(profiles)
	summary.HookEvents = sortedNamedCounters(hookEvents)
	summary.Providers = sortedNamedCounters(providers)
	return summary, nil
}

func (r *Recorder) TailEvents(limit int) ([]Event, error) {
	if r == nil {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	var events []Event
	for _, path := range r.eventPathsOldestFirst() {
		fileEvents, err := readEventFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		events = append(events, fileEvents...)
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (r *Recorder) FollowEvents(ctx context.Context, tail int, pollInterval time.Duration, emit func(Event) error) error {
	if r == nil {
		return nil
	}
	if emit == nil {
		return errors.New("telemetry follow emit function is required")
	}
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	if err := os.MkdirAll(r.settings.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := lockDir(r.settings.Dir)
	if err != nil {
		return err
	}
	initial, initialErr := r.TailEvents(tail)
	cursor := &eventCursor{}
	cursorErr := cursor.open(r.eventsPath(), true)
	unlock()
	if initialErr != nil {
		return initialErr
	}
	if cursorErr != nil {
		return cursorErr
	}
	defer cursor.close()
	for _, event := range initial {
		if err := emit(event); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			events, err := cursor.poll(r.eventPathsOldestFirst())
			if err != nil {
				return err
			}
			for _, event := range events {
				if err := emit(event); err != nil {
					return err
				}
			}
		}
	}
}

func (r *Recorder) eventPathsOldestFirst() []string {
	paths := make([]string, 0, r.settings.MaxEventFiles)
	for i := r.settings.MaxEventFiles - 1; i >= 1; i-- {
		paths = append(paths, fmt.Sprintf("%s.%d", r.eventsPath(), i))
	}
	return append(paths, r.eventsPath())
}

func readEventFile(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(data, '\n')
		if lastNewline < 0 {
			return nil, nil
		}
		data = data[:lastNewline+1]
	}
	return decodeEventData(path, data)
}

func decodeEventData(path string, data []byte) ([]Event, error) {
	lines := bytes.Split(data, []byte{'\n'})
	events := make([]Event, 0, len(lines)-1)
	for index, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode telemetry event %s line %d: %w", path, index+1, err)
		}
		events = append(events, event)
	}
	return events, nil
}

type eventCursor struct {
	file    *os.File
	pending []byte
}

func (c *eventCursor) open(path string, seekEnd bool) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if seekEnd {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close()
			return err
		}
	}
	c.file = file
	c.pending = nil
	return nil
}

func (c *eventCursor) close() {
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
}

func (c *eventCursor) poll(paths []string) ([]Event, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	activePath := paths[len(paths)-1]
	events, err := c.readAvailable(activePath)
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Stat(activePath)
	if errors.Is(err, os.ErrNotExist) {
		return events, nil
	}
	if err != nil {
		return nil, err
	}
	if c.file == nil {
		if err := c.open(activePath, false); err != nil {
			return nil, err
		}
		newEvents, err := c.readAvailable(activePath)
		return append(events, newEvents...), err
	}
	return c.pollOpenFile(paths, activePath, pathInfo, events)
}

func (c *eventCursor) pollOpenFile(paths []string, activePath string, pathInfo os.FileInfo, events []Event) ([]Event, error) {
	currentInfo, err := c.file.Stat()
	if err != nil {
		return nil, err
	}
	offset, err := c.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if os.SameFile(currentInfo, pathInfo) {
		if pathInfo.Size() >= offset {
			return events, nil
		}
		if _, err := c.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		c.pending = nil
		newEvents, err := c.readAvailable(activePath)
		return append(events, newEvents...), err
	}

	currentIndex := -1
	for index, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if os.SameFile(currentInfo, info) {
			currentIndex = index
			break
		}
	}
	c.close()
	if currentIndex < 0 {
		currentIndex = len(paths) - 2
	}
	for index := currentIndex + 1; index < len(paths); index++ {
		path := paths[index]
		if index == len(paths)-1 {
			if err := c.open(path, false); err != nil {
				return nil, err
			}
			newEvents, err := c.readAvailable(path)
			if err != nil {
				return nil, err
			}
			events = append(events, newEvents...)
			continue
		}
		fileEvents, err := readEventFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		events = append(events, fileEvents...)
	}
	return events, nil
}

func (c *eventCursor) readAvailable(path string) ([]Event, error) {
	if c.file == nil {
		return nil, nil
	}
	data, err := io.ReadAll(c.file)
	if err != nil {
		return nil, err
	}
	data = append(c.pending, data...)
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		c.pending = append(c.pending[:0], data...)
		return nil, nil
	}
	complete := data[:lastNewline+1]
	c.pending = append(c.pending[:0], data[lastNewline+1:]...)
	return decodeEventData(path, complete)
}

func (m *Metrics) Update(event Event, retentionDays int) {
	now := event.Time.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if m.Version == 0 {
		m.Version = metricsVersion
	}
	if m.FirstSeen.IsZero() {
		m.FirstSeen = now
	}
	m.UpdatedAt = now
	initMetricMaps(m)
	eventCounter := counterForEvent(event)
	m.Totals.Add(eventCounter)
	if shouldAggregateAgent(event) {
		m.Agents[event.Target] = addCounter(m.Agents[event.Target], eventCounter)
	}
	if event.Profile != "" {
		m.Profiles[event.Profile] = addCounter(m.Profiles[event.Profile], eventCounter)
	}
	if event.Target != "" || event.HookEvent != "" {
		key := event.Target + "/" + event.HookEvent
		key = strings.Trim(key, "/")
		if key != "" {
			m.HookEvents[key] = addCounter(m.HookEvents[key], eventCounter)
		}
	}
	for provider, counter := range providerCountersForEvent(event) {
		m.Providers[provider] = addCounter(m.Providers[provider], counter)
	}

	date := now.Format("2006-01-02")
	day := m.Daily[date]
	initDailyMaps(&day)
	day.Counter.Add(eventCounter)
	if shouldAggregateAgent(event) {
		day.Agents[event.Target] = addCounter(day.Agents[event.Target], eventCounter)
	}
	if event.Profile != "" {
		day.Profiles[event.Profile] = addCounter(day.Profiles[event.Profile], eventCounter)
	}
	if event.Target != "" || event.HookEvent != "" {
		key := event.Target + "/" + event.HookEvent
		key = strings.Trim(key, "/")
		if key != "" {
			day.HookEvents[key] = addCounter(day.HookEvents[key], eventCounter)
		}
	}
	for provider, counter := range providerCountersForEvent(event) {
		day.Providers[provider] = addCounter(day.Providers[provider], counter)
	}
	m.Daily[date] = day
	m.Prune(retentionDays)
}

func (m *Metrics) Prune(retentionDays int) {
	if retentionDays <= 0 || len(m.Daily) == 0 {
		return
	}
	cutoff := dateStart(time.Now().UTC()).AddDate(0, 0, -retentionDays+1)
	for date := range m.Daily {
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Before(cutoff) {
			delete(m.Daily, date)
		}
	}
}

func (c *Counter) Add(other Counter) {
	c.Events += other.Events
	c.Successes += other.Successes
	c.Errors += other.Errors
	c.Skipped += other.Skipped
	c.Recalls += other.Recalls
	c.Hits += other.Hits
	c.Inserted += other.Inserted
	c.Writes += other.Writes
	c.Items += other.Items
	c.Refs += other.Refs
	c.Flushes += other.Flushes
	c.ProviderErrors += other.ProviderErrors
	c.DurationMS += other.DurationMS
	c.ProviderWriteSamples += other.ProviderWriteSamples
	c.ProviderWriteDurationMS += other.ProviderWriteDurationMS
	c.PassiveWriteLatencyTotalMS += other.PassiveWriteLatencyTotalMS
	c.PassiveWriteSamples += other.PassiveWriteSamples
}

func QueryFields(query string, capturePreview bool, previewChars int) (hash string, length int, preview string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", 0, ""
	}
	sum := sha256.Sum256([]byte(query))
	hash = hex.EncodeToString(sum[:])[:16]
	length = utf8.RuneCountInString(query)
	if capturePreview {
		preview = truncateRunes(query, previewChars)
	}
	return hash, length, preview
}

func ProviderHits(hits []memory.MemoryHit) map[string]int {
	counts := make(map[string]int)
	for _, hit := range hits {
		if hit.Provider == "" {
			continue
		}
		counts[hit.Provider]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func ProviderRefs(refs []memory.MemoryRef) map[string]int {
	counts := make(map[string]int)
	for _, ref := range refs {
		if ref.Provider == "" {
			continue
		}
		counts[ref.Provider]++
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func ProviderErrors(errors []memory.ProviderError) []ProviderErrorDetail {
	if len(errors) == 0 {
		return nil
	}
	details := make([]ProviderErrorDetail, 0, len(errors))
	for _, err := range errors {
		if err.Provider == "" {
			continue
		}
		details = append(details, ProviderErrorDetail{
			Provider: err.Provider,
			Op:       err.Op,
			Required: err.Required,
		})
	}
	return details
}

func (r *Recorder) PrepareQueryEvent(event *Event, query string) {
	hash, length, preview := QueryFields(query, r.settings.CaptureQueryPreview, r.settings.QueryPreviewChars)
	event.QueryHash = hash
	event.QueryLength = length
	event.QueryPreview = preview
}

func (r *Recorder) loadMetrics() (Metrics, error) {
	path := r.metricsPath()
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metrics{Version: metricsVersion}, nil
		}
		return Metrics{}, err
	}
	defer file.Close()
	var metrics Metrics
	if err := json.NewDecoder(file).Decode(&metrics); err != nil && !errors.Is(err, io.EOF) {
		return Metrics{}, err
	}
	if metrics.Version == 0 {
		metrics.Version = metricsVersion
	}
	initMetricMaps(&metrics)
	return metrics, nil
}

func (r *Recorder) saveMetrics(metrics Metrics) error {
	path := r.metricsPath()
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(metrics)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tmp)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}

func (r *Recorder) appendEvent(event Event) error {
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	path := r.eventsPath()
	if err := r.rotateIfNeeded(path, int64(len(line))); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	if _, err := writer.Write(line); err != nil {
		return err
	}
	return writer.Flush()
}

func (r *Recorder) rotateIfNeeded(path string, incomingBytes int64) error {
	maxBytes := r.settings.MaxEventFileBytes
	if maxBytes <= 0 {
		return nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if stat.Size()+incomingBytes <= maxBytes {
		return nil
	}
	backups := r.settings.MaxEventFiles - 1
	if backups <= 0 {
		return os.Remove(path)
	}
	oldest := fmt.Sprintf("%s.%d", path, backups)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := backups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		dst := fmt.Sprintf("%s.%d", path, i+1)
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

func (r *Recorder) storageInfo() StorageInfo {
	info := StorageInfo{
		Dir:         r.settings.Dir,
		EventsFile:  r.eventsPath(),
		MetricsFile: r.metricsPath(),
		MaxBytes:    r.settings.MaxEventFileBytes,
		MaxFiles:    r.settings.MaxEventFiles,
	}
	paths := []string{r.eventsPath(), r.metricsPath()}
	for i := 1; i < r.settings.MaxEventFiles; i++ {
		paths = append(paths, fmt.Sprintf("%s.%d", r.eventsPath(), i))
	}
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		if path == r.eventsPath() {
			info.EventBytes = stat.Size()
		}
		info.TotalBytes += stat.Size()
	}
	return info
}

func (r *Recorder) eventsPath() string {
	return filepath.Join(r.settings.Dir, r.settings.EventsFile)
}

func (r *Recorder) metricsPath() string {
	return filepath.Join(r.settings.Dir, r.settings.MetricsFile)
}

func defaultTelemetryDir(configPath string) string {
	configPath = config.ExpandPath(configPath)
	if configPath != "" && configPath != config.DefaultConfigPath() {
		return filepath.Join(filepath.Dir(configPath), "state")
	}
	return config.DefaultStateDir()
}

func counterForEvent(event Event) Counter {
	counter := Counter{
		Events:         1,
		DurationMS:     event.DurationMS,
		Hits:           event.HitCount,
		Inserted:       event.InsertedCount,
		Items:          event.ItemCount,
		Refs:           event.RefCount,
		Flushes:        boolInt(event.Flushed > 0),
		ProviderErrors: len(event.ProviderErrorDetails),
	}
	if event.Success {
		counter.Successes = 1
	} else {
		counter.Errors = 1
	}
	if event.Skipped {
		counter.Skipped = 1
	}
	switch event.Kind {
	case "recall", "hook_recall":
		if !event.Skipped {
			counter.Recalls = 1
		}
	case "remember", "hook_write":
		if !event.Skipped {
			counter.Writes = 1
		}
	case "hook_delivery":
		if event.Success && !event.Skipped {
			counter.Writes = 1
			counter.ProviderWriteSamples = 1
			counter.ProviderWriteDurationMS = event.ProviderDurationMS
			counter.PassiveWriteLatencyTotalMS = event.PassiveWriteLatencyTotalMS
			counter.PassiveWriteSamples = event.PassiveWriteSamples
		}
	}
	if len(event.ProviderErrorDetails) > 0 && counter.Errors == 0 {
		counter.Errors = len(event.ProviderErrorDetails)
	}
	return counter
}

func providerCountersForEvent(event Event) map[string]Counter {
	counters := make(map[string]Counter)
	for provider, count := range event.ProviderRecalls {
		counter := counters[provider]
		counter.Recalls += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderWrites {
		counter := counters[provider]
		counter.Writes += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderHits {
		counter := counters[provider]
		counter.Hits += count
		counters[provider] = counter
	}
	for provider, count := range event.ProviderRefs {
		counter := counters[provider]
		counter.Refs += count
		counters[provider] = counter
	}
	for _, providerErr := range event.ProviderErrorDetails {
		counter := counters[providerErr.Provider]
		counter.ProviderErrors++
		counter.Errors++
		counters[providerErr.Provider] = counter
	}
	if event.Kind == "hook_delivery" && event.Success && !event.Skipped && strings.TrimSpace(event.Provider) != "" {
		counter := counters[event.Provider]
		counter.ProviderWriteSamples++
		counter.ProviderWriteDurationMS += event.ProviderDurationMS
		counter.PassiveWriteLatencyTotalMS += event.PassiveWriteLatencyTotalMS
		counter.PassiveWriteSamples += event.PassiveWriteSamples
		counters[event.Provider] = counter
	}
	for provider, counter := range counters {
		if provider == "" {
			delete(counters, provider)
			continue
		}
		counter.Events = 1
		counters[provider] = counter
	}
	return counters
}

func shouldAggregateAgent(event Event) bool {
	return event.Source == "hook" && strings.TrimSpace(event.Target) != ""
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func initMetricMaps(metrics *Metrics) {
	if metrics.Daily == nil {
		metrics.Daily = make(map[string]DailyMetric)
	}
	if metrics.Agents == nil {
		metrics.Agents = make(map[string]Counter)
	}
	if metrics.Profiles == nil {
		metrics.Profiles = make(map[string]Counter)
	}
	if metrics.HookEvents == nil {
		metrics.HookEvents = make(map[string]Counter)
	}
	if metrics.Providers == nil {
		metrics.Providers = make(map[string]Counter)
	}
}

func initDailyMaps(day *DailyMetric) {
	if day.Profiles == nil {
		day.Profiles = make(map[string]Counter)
	}
	if day.Agents == nil {
		day.Agents = make(map[string]Counter)
	}
	if day.HookEvents == nil {
		day.HookEvents = make(map[string]Counter)
	}
	if day.Providers == nil {
		day.Providers = make(map[string]Counter)
	}
}

func addCounter(base, delta Counter) Counter {
	base.Add(delta)
	return base
}

func addCounters(dst map[string]Counter, src map[string]Counter) {
	for name, counter := range src {
		dst[name] = addCounter(dst[name], counter)
	}
}

func sortedNamedCounters(counters map[string]Counter) []NamedCounter {
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]NamedCounter, 0, len(names))
	for _, name := range names {
		result = append(result, NamedCounter{Name: name, Counter: counters[name]})
	}
	return result
}

func dateStart(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func truncateRunes(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
