package session

import (
	"context"
	"encoding/json"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
)

// timelinePersister listens to selected bus events and appends them to the store.
type timelinePersister struct {
	sessionID string
	store     Store
	bus       *bus.Bus
	sub       *bus.Subscription
}

func newTimelinePersister(sessionID string, store Store, b *bus.Bus) *timelinePersister {
	return &timelinePersister{
		sessionID: sessionID,
		store:     store,
		bus:       b,
		sub: b.Subscribe(
			bus.EventAgentContentFull,
			bus.EventAgentThinking,
			bus.EventAgentStepTokens,
			bus.EventAgentToolCallStart,
			bus.EventAgentToolCallDone,
			bus.EventAgentDone,
		),
	}
}

func (p *timelinePersister) Run(ctx context.Context) {
	if p.sub == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-p.sub.Ch():
			if !ok {
				return
			}
			p.persistEvent(ctx, ev)
		}
	}
}

func (p *timelinePersister) Close() {
	if p.sub == nil {
		return
	}
	p.bus.Unsubscribe(p.sub)
	p.sub = nil
}

// persistEvent converts a bus event to a StoredEvent and appends to the store.
func (p *timelinePersister) persistEvent(ctx context.Context, ev bus.Event) {
	now := time.Now()

	switch ev.Type {
	case bus.EventAgentContentFull:
		payload, ok := ev.Payload.(bus.ContentFullPayload)
		if !ok {
			return
		}
		content, err := json.Marshal(assistantContentPayload{Content: payload.Content})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventAssistantContent, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAssistantContent, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventAssistantContent, "error", err))
		}
		return

	case bus.EventAgentThinking:
		payload, ok := ev.Payload.(bus.ThinkingPayload)
		if !ok {
			return
		}
		content, err := json.Marshal(payload)
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentThinking, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentThinking, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentThinking, "error", err))
		}
		return

	case bus.EventAgentStepTokens:
		payload, ok := ev.Payload.(bus.StepTokenUsagePayload)
		if !ok {
			return
		}
		content, err := json.Marshal(payload)
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentStepTokens, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentStepTokens, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentStepTokens, "error", err))
		}
		return

	case bus.EventAgentToolCallStart:
		payload, ok := ev.Payload.(bus.ToolCallStartPayload)
		if !ok {
			return
		}
		content, err := json.Marshal(toolCallContent{
			ToolCallID: payload.ToolCallID,
			ToolName:   payload.ToolName,
			ToolArgs:   payload.ToolArgs,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventToolCall, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventToolCall, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventToolCall, "error", err))
		}
		return

	case bus.EventAgentToolCallDone:
		payload, ok := ev.Payload.(bus.ToolCallDonePayload)
		if !ok {
			return
		}
		content, err := json.Marshal(toolResultContent{
			ToolCallID: payload.ToolCallID,
			ToolName:   payload.ToolName,
			ToolResult: payload.ToolResult,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventToolResult, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventToolResult, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventToolResult, "error", err))
		}
		return

	case bus.EventAgentDone:
		payload, ok := ev.Payload.(bus.AgentDonePayload)
		if !ok {
			return
		}
		doneContent, err := json.Marshal(map[string]int64{
			"prompt_tokens":     payload.PromptTokens,
			"completion_tokens": payload.CompletionTokens,
			"total_tokens":      payload.TotalTokens,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentDone, "error", err))
			return
		}
		if err := p.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentDone, Content: doneContent}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", p.sessionID, "event_type", StoredEventAgentDone, "error", err))
		}
	}
}
