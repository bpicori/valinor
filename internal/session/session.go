package session

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"valinor/internal/llm"
	"valinor/internal/bus"
)

// Session owns the agent and persists conversation events to the store.
type Session struct {
	meta  Metadata
	store Store
	agent *llm.Agent
	bus   *bus.Bus
	sub   *bus.Subscription

	mu                 sync.Mutex
	accumulatedContent string
}

// NewSession creates a session that subscribes to all bus events for persistence.
func NewSession(meta Metadata, store Store, ag *llm.Agent, b *bus.Bus) *Session {
	return &Session{
		meta:  meta,
		store: store,
		agent: ag,
		bus:   b,
		sub:   b.Subscribe(), // no types = all events
	}
}

// Run starts the agent and the persistence loop. Call in a goroutine.
// Returns when ctx is cancelled.
func (s *Session) Run(ctx context.Context) {
	go s.agent.Run(ctx)
	s.runPersistence(ctx)
}

// runPersistence subscribes to all bus events and persists them.
func (s *Session) runPersistence(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.sub.Ch():
			if !ok {
				return
			}
			s.persistEvent(ctx, ev)
		}
	}
}

// persistEvent converts a bus event to a StoredEvent and appends to the store.
func (s *Session) persistEvent(ctx context.Context, ev bus.Event) {
	now := time.Now()

	switch ev.Type {
	case bus.EventUserMessage:
		p := ev.Payload.(bus.UserMessagePayload)
		content, _ := json.Marshal(userMessageContent{Content: p.Content})
		evt := StoredEvent{Timestamp: now, Type: StoredEventUserMessage, Content: content}
		_ = s.store.AppendEvent(ctx, evt)
		return

	case bus.EventAgentContentDelta:
		s.mu.Lock()
		s.accumulatedContent += ev.Payload.(bus.ContentDeltaPayload).Content
		s.mu.Unlock()
		return

	case bus.EventAgentToolCallStart:
		p := ev.Payload.(bus.ToolCallStartPayload)
		content, _ := json.Marshal(toolCallContent{
			ToolCallID: p.ToolCallID,
			ToolName:   p.ToolName,
			ToolArgs:   p.ToolArgs,
		})
		evt := StoredEvent{Timestamp: now, Type: StoredEventToolCall, Content: content}
		_ = s.store.AppendEvent(ctx, evt)
		return

	case bus.EventAgentToolCallDone:
		p := ev.Payload.(bus.ToolCallDonePayload)
		content, _ := json.Marshal(toolResultContent{
			ToolCallID: p.ToolCallID,
			ToolName:   p.ToolName,
			ToolResult: p.ToolResult,
		})
		evt := StoredEvent{Timestamp: now, Type: StoredEventToolResult, Content: content}
		_ = s.store.AppendEvent(ctx, evt)
		return

	case bus.EventAgentDone:
		s.mu.Lock()
		content := s.accumulatedContent
		s.accumulatedContent = ""
		s.mu.Unlock()
		if content != "" {
			ac, _ := json.Marshal(assistantContentPayload{Content: content})
			_ = s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAssistantContent, Content: ac})
		}
		p := ev.Payload.(bus.AgentDonePayload)
		doneContent, _ := json.Marshal(map[string]int64{
			"prompt_tokens":     p.PromptTokens,
			"completion_tokens": p.CompletionTokens,
			"total_tokens":      p.TotalTokens,
		})
		evt := StoredEvent{Timestamp: now, Type: StoredEventAgentDone, Content: doneContent}
		_ = s.store.AppendEvent(ctx, evt)
		return
	}
}

// Close releases resources. Call when the session is no longer needed.
func (s *Session) Close() error {
	if s.sub != nil {
		s.bus.Unsubscribe(s.sub)
		s.sub = nil
	}
	if s.agent != nil {
		_ = s.agent.Close()
		s.agent = nil
	}
	if s.store != nil {
		err := s.store.Close()
		s.store = nil
		return err
	}
	return nil
}

// Meta returns the session metadata.
func (s *Session) Meta() Metadata {
	return s.meta
}
