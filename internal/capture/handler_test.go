package capture

import (
	"context"
	"testing"

	"github.com/pax-beehive/paxm/internal/config"
)

type recallStub struct{ calls int }

func (s *recallStub) Recall(context.Context, Event) (Result, error) { s.calls++; return Result{}, nil }

func TestHandlerOwnsHookPolicy(t *testing.T) {
	cfg := config.DefaultConfig("/tmp/paxm-handler-test.yaml")
	agent := cfg.Agents["codex"]
	agent.Enabled = true
	hook := agent.Hooks["user_input"]
	hook.Write.Enabled = true
	hook.Recall.Enabled = true
	hook.Recall.Initial = &config.HookInitialRecall{Enabled: true}
	agent.Hooks["user_input"] = hook
	cfg.Agents["codex"] = agent
	recall := &recallStub{}
	buffered := 0
	h := Handler{Config: cfg, Recall: recall, MarkInitial: func(e Event) (Event, error) {
		if e.Metadata == nil {
			e.Metadata = map[string]string{}
		}
		e.Metadata[RecallPhaseMetadataKey] = RecallPhaseInitial
		return e, nil
	}, Buffer: func(Event) error { buffered++; return nil }}
	outcome, err := h.Handle(context.Background(), Event{Target: "codex", Event: "user_input"})
	if err != nil || outcome.Result == nil || recall.calls != 1 || buffered != 1 {
		t.Fatalf("outcome=%+v calls=%d buffered=%d err=%v", outcome, recall.calls, buffered, err)
	}
	if outcome.Event.Metadata[RecallPhaseMetadataKey] != RecallPhaseInitial {
		t.Fatalf("metadata=%v", outcome.Event.Metadata)
	}
}

func TestHandlerRejectsForeignOwnerBeforeLoadingRecall(t *testing.T) {
	cfg := config.DefaultConfig("/tmp/paxm-handler-test.yaml")
	agent := cfg.Agents["codex"]
	agent.Enabled = true
	agent.Integration.Owner = config.IntegrationOwnerCodexPlugin
	cfg.Agents["codex"] = agent
	loaded := false
	h := Handler{Config: cfg, SourceOwner: config.IntegrationOwnerPaxm, Recall: RecallFunc(func(context.Context, Event) (Result, error) {
		loaded = true
		return Result{}, nil
	})}
	outcome, err := h.Handle(context.Background(), Event{Target: "codex", Event: "user_input"})
	if err != nil || !outcome.Ignored || loaded {
		t.Fatalf("outcome=%+v loaded=%v err=%v", outcome, loaded, err)
	}
}
