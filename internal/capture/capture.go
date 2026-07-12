package capture

import (
	"context"
	"encoding/json"

	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/facade"
	"github.com/pax-beehive/paxm/internal/tools"
)

const RecallPhaseMetadataKey = "paxm_recall_phase"
const RecallPhaseInitial = "initial"

type Event struct {
	Target    string            `json:"target,omitempty"`
	Event     string            `json:"event,omitempty"`
	Query     string            `json:"query,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`
	Assistant string            `json:"assistant,omitempty"`
	Messages  []Message         `json:"messages,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Limit     int               `json:"limit,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Raw       json.RawMessage   `json:"-"`
}
type Message struct {
	Role    string `json:"role,omitempty"`
	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`
	Source  string `json:"source,omitempty"`
}
type Result struct {
	Target  string              `json:"target"`
	Event   string              `json:"event"`
	Query   string              `json:"query,omitempty"`
	Skipped bool                `json:"skipped,omitempty"`
	Recall  *tools.RecallResult `json:"recall,omitempty"`
}

// Service owns passive hook policy and adapts the compatibility implementation
// behind an independent capture contract.
type Service struct{ core *facade.Service }

func New(core *facade.Service) *Service { return &Service{core: core} }
func (s *Service) Recall(ctx context.Context, event Event) (Result, error) {
	value, err := s.core.RunHook(ctx, toFacadeEvent(event))
	result := Result{Target: value.Target, Event: value.Event, Query: value.Query, Skipped: value.Skipped}
	if value.Recall != nil {
		copy := tools.RecallResult(*value.Recall)
		result.Recall = &copy
	}
	return result, err
}
func (s *Service) WriteItem(event Event) (tools.RememberInput, bool, error) {
	return s.core.HookWriteItem(toFacadeEvent(event))
}
func (s *Service) BufferConfig(event Event) config.HookBufferConfig {
	return s.core.HookBufferConfig(toFacadeEvent(event))
}
func toFacadeEvent(event Event) facade.HookEvent {
	messages := make([]facade.HookMessage, 0, len(event.Messages))
	for _, message := range event.Messages {
		messages = append(messages, facade.HookMessage{Role: message.Role, Text: message.Text, Content: message.Content, Source: message.Source})
	}
	return facade.HookEvent{Target: event.Target, Event: event.Event, Query: event.Query, Prompt: event.Prompt, Assistant: event.Assistant, Messages: messages, Workspace: event.Workspace, Limit: event.Limit, Metadata: event.Metadata, Raw: event.Raw}
}
