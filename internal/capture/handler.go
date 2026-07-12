package capture

import (
	"context"
	"strings"
	"time"

	"github.com/pax-beehive/paxm/internal/config"
)

// RecallPolicy is the passive-recall capability used by hook handling.
type RecallPolicy interface {
	Recall(context.Context, Event) (Result, error)
}

type RecallFunc func(context.Context, Event) (Result, error)

func (f RecallFunc) Recall(ctx context.Context, event Event) (Result, error) {
	return f(ctx, event)
}

// Handler owns hook ownership, initial-recall, passive-write, and recall
// orchestration. Transport adapters only decode an Event and render Outcome.
type Handler struct {
	Config        config.Config
	SourceOwner   string
	Recall        RecallPolicy
	MarkInitial   func(Event) (Event, error)
	Buffer        func(Event) error
	ObserveRecall func(Event, Result, time.Duration, error)
}

type Outcome struct {
	Event       Event
	Result      *Result
	Ignored     bool
	BufferError error
}

func (h Handler) Handle(ctx context.Context, event Event) (Outcome, error) {
	if !SourceAllowed(h.Config, event, h.SourceOwner) {
		return Outcome{Event: event, Ignored: true}, nil
	}
	if InitialRecallEnabled(h.Config, event) && h.MarkInitial != nil {
		marked, err := h.MarkInitial(event)
		if err == nil {
			event = marked
		}
	}
	outcome := Outcome{Event: event}
	if WriteEnabled(h.Config, event) && h.Buffer != nil {
		outcome.BufferError = h.Buffer(event)
	}
	if event.Event != "user_input" || h.Recall == nil {
		return outcome, nil
	}
	started := time.Now()
	result, err := h.Recall.Recall(ctx, event)
	if h.ObserveRecall != nil {
		h.ObserveRecall(event, result, time.Since(started), err)
	}
	outcome.Result = &result
	return outcome, err
}

func SourceAllowed(cfg config.Config, event Event, source string) bool {
	owner := strings.ToLower(strings.TrimSpace(cfg.Agents[event.Target].Integration.Owner))
	source = strings.ToLower(strings.TrimSpace(source))
	if owner == config.IntegrationOwnerCodexPlugin || owner == config.IntegrationOwnerClaudePlugin {
		return source == owner
	}
	return source == "" || source == config.IntegrationOwnerPaxm
}

func WriteEnabled(cfg config.Config, event Event) bool {
	target := event.Target
	if target == "" {
		target = "codex"
	}
	agent, ok := cfg.Agents[target]
	if !ok || !agent.Enabled {
		return false
	}
	hook, ok := agent.Hooks[event.Event]
	return ok && hook.Write.Enabled
}

func InitialRecallEnabled(cfg config.Config, event Event) bool {
	target, name := event.Target, event.Event
	if target == "" {
		target = "codex"
	}
	if name == "" {
		name = "user_input"
	}
	if name != "user_input" {
		return false
	}
	agent, ok := cfg.Agents[target]
	if !ok || !agent.Enabled {
		return false
	}
	hook, ok := agent.Hooks[name]
	return ok && hook.Recall.Enabled && hook.Recall.Initial != nil && hook.Recall.Initial.Enabled
}
