// Package dashboard serves a localhost read-only view over paxm telemetry so
// operators can inspect metrics, logs, sessions, and recall quality.
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/operator"
	"github.com/pax-beehive/paxm/internal/telemetry"
)

const (
	defaultDays        = 7
	defaultEventLimit  = 50
	maxEventLimit      = 1000
	sessionEventWindow = 1000
)

// Server exposes telemetry reads as JSON APIs plus a single-page UI.
type Server struct {
	operator *operator.Service
	days     int
}

func New(op *operator.Service, days int) *Server {
	if days <= 0 {
		days = defaultDays
	}
	return &Server{operator: op, days: days}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /api/summary", s.handleSummary)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	return mux
}

// Serve binds addr, reports the effective URL on out, and serves until ctx is
// cancelled.
func (s *Server) Serve(ctx context.Context, addr string, out io.Writer) error {
	if err := validateLoopbackAddr(addr); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	_, _ = fmt.Fprintf(out, "paxm dashboard listening on http://%s\n", listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func validateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid dashboard listen address %q: %w", addr, err)
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("dashboard listen address must use a loopback host, got %q", host)
	}
	return nil
}

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, pageHTML)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	days, ok := queryInt(w, r, "days", s.days)
	if !ok {
		return
	}
	summary, err := s.operator.History(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, summary)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryInt(w, r, "limit", defaultEventLimit)
	if !ok {
		return
	}
	offset, ok := queryOffset(w, r)
	if !ok {
		return
	}
	filter := parseEventFilter(r)
	events, err := s.operator.TailEvents(maxEventLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	filtered := make([]telemetry.Event, 0, limit)
	for _, event := range events {
		if filter.matches(event) {
			filtered = append(filtered, event)
		}
	}
	total := len(filtered)
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	writeJSON(w, map[string]any{"events": filtered[offset:end], "total": total, "limit": limit, "offset": offset})
}

// eventFilter narrows the telemetry event window by structured fields and a
// free-text query preview match.
type eventFilter struct {
	kinds       map[string]bool
	sources     map[string]bool
	session     string
	profile     string
	query       string
	state       string
	hitProvider string
	hitQuery    string
}

func parseEventFilter(r *http.Request) eventFilter {
	return eventFilter{
		kinds:       commaSet(r.URL.Query().Get("kind")),
		sources:     commaSet(r.URL.Query().Get("source")),
		session:     strings.TrimSpace(r.URL.Query().Get("session")),
		profile:     strings.TrimSpace(r.URL.Query().Get("profile")),
		query:       strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))),
		state:       strings.TrimSpace(r.URL.Query().Get("state")),
		hitProvider: strings.TrimSpace(r.URL.Query().Get("hit_provider")),
		hitQuery:    strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hit_q"))),
	}
}

func commaSet(value string) map[string]bool {
	parts := strings.Split(value, ",")
	set := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			set[part] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func (f eventFilter) matches(event telemetry.Event) bool {
	if len(f.kinds) > 0 && !f.kinds[event.Kind] {
		return false
	}
	if len(f.sources) > 0 && !f.sources[event.Source] {
		return false
	}
	if f.session != "" && event.SessionKey != f.session {
		return false
	}
	if f.profile != "" && event.Profile != f.profile {
		return false
	}
	if f.query != "" && !strings.Contains(strings.ToLower(event.QueryPreview), f.query) {
		return false
	}
	if (f.hitProvider != "" || f.hitQuery != "") && !hitsMatch(event.RecallHits, f.hitProvider, f.hitQuery) {
		return false
	}
	switch f.state {
	case "success":
		return event.Success && !event.Skipped
	case "error":
		return !event.Success
	case "timeout":
		return event.RecallTimedOut
	case "skipped":
		return event.Skipped
	}
	return true
}

// hitsMatch reports whether any recall hit satisfies the provider and text
// constraints; an unset constraint matches everything.
func hitsMatch(hits []telemetry.RecallHit, provider, query string) bool {
	for _, hit := range hits {
		if provider != "" && hit.Provider != provider {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(hit.TextPreview), query) {
			continue
		}
		return true
	}
	return false
}

// Session aggregates telemetry events that share one capture session key.
type Session struct {
	Key        string    `json:"key"`
	Target     string    `json:"target,omitempty"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	Recalls    int       `json:"recalls"`
	Writes     int       `json:"writes"`
	Deliveries int       `json:"deliveries"`
	LastQuery  string    `json:"last_query,omitempty"`
}

func (s *Server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	events, err := s.operator.TailEvents(sessionEventWindow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"sessions": aggregateSessions(events)})
}

func aggregateSessions(events []telemetry.Event) []Session {
	byKey := make(map[string]*Session)
	for _, event := range events {
		key := strings.TrimSpace(event.SessionKey)
		if key == "" {
			continue
		}
		session, ok := byKey[key]
		if !ok {
			session = &Session{Key: key, FirstSeen: event.Time, LastSeen: event.Time}
			byKey[key] = session
		}
		if event.Time.Before(session.FirstSeen) {
			session.FirstSeen = event.Time
		}
		if event.Time.After(session.LastSeen) {
			session.LastSeen = event.Time
		}
		if event.Target != "" {
			session.Target = event.Target
		}
		switch event.Kind {
		case "recall", "hook_recall":
			session.Recalls++
			if event.QueryPreview != "" {
				session.LastQuery = event.QueryPreview
			}
		case "remember", "hook_write":
			session.Writes++
		case "hook_delivery":
			session.Deliveries++
		}
	}
	sessions := make([]Session, 0, len(byKey))
	for _, session := range byKey {
		sessions = append(sessions, *session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastSeen.After(sessions[j].LastSeen) })
	return sessions
}

func queryInt(w http.ResponseWriter, r *http.Request, name string, fallback int) (int, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		writeError(w, http.StatusBadRequest, name+" must be a positive integer")
		return 0, false
	}
	return parsed, true
}

func queryOffset(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("offset"))
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return 0, false
	}
	return parsed, true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
