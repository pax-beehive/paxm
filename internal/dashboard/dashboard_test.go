package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/operator"
	"github.com/pax-beehive/paxm/internal/telemetry"
)

func newTestServer(t *testing.T) (*httptest.Server, *telemetry.Recorder) {
	t.Helper()
	dir := t.TempDir()
	enabled := true
	cfg := config.Config{Version: 1, Telemetry: config.TelemetryConfig{Enabled: &enabled, Dir: dir}}
	recorder := telemetry.NewRecorder(cfg.Telemetry, filepath.Join(dir, "config.yaml"))
	op := operator.New(filepath.Join(dir, "config.yaml"), cfg, nil, nil)
	return httptest.NewServer(New(op, 7).Handler()), recorder
}

func seedEvents(t *testing.T, recorder *telemetry.Recorder) {
	t.Helper()
	now := time.Now().UTC()
	session := "codex/workspace/ws/session/s1"
	events := []telemetry.Event{
		{Time: now.Add(-3 * time.Hour), Kind: "recall", Source: "cli", Command: "recall", Success: true, Profile: "default", QueryHash: "abc123", HitCount: 2, RecallHits: []telemetry.RecallHit{{Provider: "sqlite", ID: "1", Score: 1, TextPreview: "deploy runbook"}}},
		{Time: now.Add(-2 * time.Hour), Kind: "hook_recall", Source: "hook", Command: "hook", Target: "codex", HookEvent: "user_input", Success: true, SessionKey: session, QueryPreview: "deploy prod", HitCount: 1},
		{Time: now.Add(-1 * time.Hour), Kind: "hook_write", Source: "hook", Command: "hook", Target: "codex", HookEvent: "turn_end", Success: true, SessionKey: session},
		{Time: now, Kind: "hook_delivery", Source: "capture_queue", Command: "hook", Success: true, SessionKey: session},
	}
	for _, event := range events {
		if err := recorder.Record(event); err != nil {
			t.Fatal(err)
		}
	}
}

func getJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", url, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("GET %s decode: %v", url, err)
	}
}

func TestSummaryEndpoint(t *testing.T) {
	t.Parallel()
	server, recorder := newTestServer(t)
	defer server.Close()
	seedEvents(t, recorder)

	var summary telemetry.HistorySummary
	getJSON(t, server.URL+"/api/summary?days=7", &summary)
	if summary.Totals.Recalls != 2 || summary.Totals.Writes != 2 || summary.Totals.Hits != 3 {
		t.Fatalf("totals = %#v, want 2 recalls / 2 writes / 3 hits", summary.Totals)
	}

	response, err := http.Get(server.URL + "/api/summary?days=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad days status = %d, want 400", response.StatusCode)
	}
}

func TestEventsEndpointFilters(t *testing.T) {
	t.Parallel()
	server, recorder := newTestServer(t)
	defer server.Close()
	seedEvents(t, recorder)

	var result struct {
		Events []telemetry.Event `json:"events"`
		Total  int               `json:"total"`
	}
	getJSON(t, server.URL+"/api/events?kind=recall", &result)
	if len(result.Events) != 1 || result.Events[0].Source != "cli" || result.Total != 1 {
		t.Fatalf("kind filter events = %#v total=%d", result.Events, result.Total)
	}
	if len(result.Events[0].RecallHits) != 1 || result.Events[0].RecallHits[0].Provider != "sqlite" {
		t.Fatalf("recall hits = %#v", result.Events[0].RecallHits)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?kind=recall,hook_recall", &result)
	if result.Total != 2 {
		t.Fatalf("kind list total = %d, want 2", result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?session=codex/workspace/ws/session/s1", &result)
	if len(result.Events) != 3 || result.Total != 3 {
		t.Fatalf("session filter events = %d total=%d, want 3", len(result.Events), result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?limit=2", &result)
	if len(result.Events) != 2 || result.Total != 4 {
		t.Fatalf("limit events = %d total=%d, want 2 of 4", len(result.Events), result.Total)
	}
	if result.Events[0].Kind != "hook_delivery" || result.Events[1].Kind != "hook_write" {
		t.Fatalf("events not newest-first: %#v", result.Events)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?limit=2&offset=2", &result)
	if len(result.Events) != 2 || result.Total != 4 {
		t.Fatalf("offset events = %d total=%d, want 2 of 4", len(result.Events), result.Total)
	}
	if result.Events[0].Kind != "hook_recall" || result.Events[1].Kind != "recall" {
		t.Fatalf("offset page = %#v", result.Events)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?q=deploy", &result)
	if result.Total != 1 || result.Events[0].Kind != "hook_recall" {
		t.Fatalf("q filter = %#v total=%d", result.Events, result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?state=success&kind=recall,hook_recall", &result)
	if result.Total != 2 {
		t.Fatalf("state filter total = %d, want 2", result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?state=timeout", &result)
	if result.Total != 0 {
		t.Fatalf("timeout filter total = %d, want 0", result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?hit_provider=sqlite", &result)
	if result.Total != 1 || result.Events[0].Kind != "recall" {
		t.Fatalf("hit provider filter = %#v total=%d", result.Events, result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?hit_provider=zep", &result)
	if result.Total != 0 {
		t.Fatalf("unknown hit provider total = %d, want 0", result.Total)
	}

	result.Events = nil
	getJSON(t, server.URL+"/api/events?hit_q=runbook", &result)
	if result.Total != 1 || result.Events[0].Kind != "recall" {
		t.Fatalf("hit text filter = %#v total=%d", result.Events, result.Total)
	}

	response, err := http.Get(server.URL + "/api/events?limit=-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad limit status = %d, want 400", response.StatusCode)
	}

	response2, err := http.Get(server.URL + "/api/events?offset=-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response2.Body.Close() }()
	if response2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad offset status = %d, want 400", response2.StatusCode)
	}
}

func TestSessionsEndpointAggregatesBySessionKey(t *testing.T) {
	t.Parallel()
	server, recorder := newTestServer(t)
	defer server.Close()
	seedEvents(t, recorder)

	var result struct {
		Sessions []Session `json:"sessions"`
	}
	getJSON(t, server.URL+"/api/sessions", &result)
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want 1", result.Sessions)
	}
	session := result.Sessions[0]
	if session.Key != "codex/workspace/ws/session/s1" || session.Target != "codex" {
		t.Fatalf("session = %#v", session)
	}
	if session.Recalls != 1 || session.Writes != 1 || session.Deliveries != 1 {
		t.Fatalf("session counts = %#v", session)
	}
	if session.LastQuery != "deploy prod" {
		t.Fatalf("last query = %q", session.LastQuery)
	}
}

func TestPageServesHTML(t *testing.T) {
	t.Parallel()
	server, _ := newTestServer(t)
	defer server.Close()

	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestNewClampsInvalidDays(t *testing.T) {
	t.Parallel()
	if got := New(nil, 0).days; got != defaultDays {
		t.Fatalf("days = %d, want %d", got, defaultDays)
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:7465"},
		{name: "ipv6 loopback", addr: "[::1]:7465"},
		{name: "localhost", addr: "localhost:7465"},
		{name: "ipv4 wildcard", addr: "0.0.0.0:7465", wantErr: true},
		{name: "ipv6 wildcard", addr: "[::]:7465", wantErr: true},
		{name: "non-loopback", addr: "192.0.2.10:7465", wantErr: true},
		{name: "missing port", addr: "127.0.0.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateLoopbackAddr(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateLoopbackAddr(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}
