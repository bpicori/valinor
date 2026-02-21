package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"valinor/internal/bus"
	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
)

// Session owns the agent and persists conversation events to the store.
type Session struct {
	meta       Metadata
	store      Store
	agent      *llm.LLM
	builder    *agentcontext.Builder
	messages   []agentcontext.Message
	bus        *bus.Bus
	subPersist *bus.Subscription
	subControl *bus.Subscription
}

// NewSession creates a session that subscribes to all bus events for persistence.
func NewSession(meta Metadata, store Store, ag *llm.LLM, builder *agentcontext.Builder, initialMessages []agentcontext.Message, b *bus.Bus) *Session {
	return &Session{
		meta:       meta,
		store:      store,
		agent:      ag,
		builder:    builder,
		messages:   append([]agentcontext.Message{}, initialMessages...),
		bus:        b,
		subPersist: b.Subscribe(), // no types = all events
		subControl: b.Subscribe(bus.EventUserMessage, bus.EventNewConversation),
	}
}

// Run starts the control and persistence loops. Call in a goroutine.
// Returns when ctx is cancelled.
func (s *Session) Run(ctx context.Context) {
	go s.runPersistence(ctx)
	s.runControl(ctx)
}

// runControl dispatches input events to the agent.
func (s *Session) runControl(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.subControl.Ch():
			if !ok {
				return
			}
			switch ev.Type {
			case bus.EventUserMessage:
				p := ev.Payload.(bus.UserMessagePayload)
				if err := s.appendUserMessage(ctx, p.Content); err != nil {
					continue
				}
				s.messages = append(s.messages, agentcontext.Message{
					Role:    agentcontext.RoleUser,
					Content: p.Content,
				})
				runCtx := s.builder.BuildRunContext(s.messages)
				result, err := s.agent.Run(ctx, llm.RunRequest{Context: runCtx})
				if err == nil {
					s.messages = result.Messages
				}
			case bus.EventNewConversation:
				s.messages = []agentcontext.Message{
					{
						Role:    agentcontext.RoleSystem,
						Content: s.builder.BasePrompt(),
					},
				}
			}
		}
	}
}

// runPersistence subscribes to all bus events and persists them.
func (s *Session) runPersistence(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.subPersist.Ch():
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
	case bus.EventAgentContentDelta:
		// UI streaming event; persistence uses EventAgentContentFull.
		return

	case bus.EventAgentContentFull:
		p := ev.Payload.(bus.ContentFullPayload)
		content, _ := json.Marshal(assistantContentPayload{Content: p.Content})
		_ = s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAssistantContent, Content: content})
		return

	case bus.EventAgentThinking:
		p := ev.Payload.(bus.ThinkingPayload)
		content, _ := json.Marshal(p)
		_ = s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentThinking, Content: content})
		return

	case bus.EventAgentStepTokens:
		p := ev.Payload.(bus.StepTokenUsagePayload)
		content, _ := json.Marshal(p)
		_ = s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentStepTokens, Content: content})
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

func (s *Session) appendUserMessage(ctx context.Context, userMessage string) error {
	content, err := json.Marshal(userMessageContent{Content: userMessage})
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	return s.store.AppendEvent(ctx, StoredEvent{
		Timestamp: time.Now(),
		Type:      StoredEventUserMessage,
		Content:   content,
	})
}

// Close releases resources. Call when the session is no longer needed.
func (s *Session) Close() error {
	if s.subPersist != nil {
		s.bus.Unsubscribe(s.subPersist)
		s.subPersist = nil
	}
	if s.subControl != nil {
		s.bus.Unsubscribe(s.subControl)
		s.subControl = nil
	}
	s.agent = nil
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
