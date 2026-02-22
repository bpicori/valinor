package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
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
	config     config.Config
	subPersist *bus.Subscription
	subControl *bus.Subscription
}

// NewSession creates a session that subscribes to all bus events for persistence.
func NewSession(meta Metadata, store Store, ag *llm.LLM, builder *agentcontext.Builder, initialMessages []agentcontext.Message, b *bus.Bus, cfg config.Config) *Session {
	return &Session{
		meta:       meta,
		store:      store,
		agent:      ag,
		builder:    builder,
		messages:   append([]agentcontext.Message{}, initialMessages...),
		bus:        b,
		config:     cfg,
		subPersist: b.Subscribe(), // no types = all events
		subControl: b.Subscribe(bus.EventUserMessage, bus.EventNewConversation, bus.EventChangeProviderModel),
	}
}

// Run starts the control and persistence loops. Call in a goroutine.
// Returns when ctx is cancelled.
func (s *Session) Run(ctx context.Context) {
	applog.Info("session run start", applog.M("session_id", s.meta.ID))
	go s.runPersistence(ctx)
	s.runControl(ctx)
	applog.Info("session run cancelled", applog.M("session_id", s.meta.ID))
}

// runControl dispatches input events to the agent.
func (s *Session) runControl(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-s.subControl.Ch():
			if !ok {
				applog.Warning("session control channel closed", applog.M("session_id", s.meta.ID))
				return
			}
			switch ev.Type {
			case bus.EventUserMessage:
				p := ev.Payload.(bus.UserMessagePayload)
				if err := s.appendUserMessage(ctx, p.Content); err != nil {
					applog.Error("session append error", applog.M("session_id", s.meta.ID, "error", err))
					continue
				}
				s.messages = append(s.messages, agentcontext.Message{
					Role:    agentcontext.RoleUser,
					Content: p.Content,
				})
				runCtx := s.builder.BuildRunContext(s.messages)
				result, err := s.agent.Run(ctx, llm.RunRequest{Context: runCtx})
				if err != nil {
					applog.Error("session agent run error", applog.M("session_id", s.meta.ID, "error", err))
				} else {
					s.messages = result.Messages
				}
			case bus.EventNewConversation:
				s.messages = []agentcontext.Message{
					{
						Role:    agentcontext.RoleSystem,
						Content: s.builder.BasePrompt(),
					},
				}
			case bus.EventChangeProviderModel:
				p := ev.Payload.(bus.ChangeProviderModelPayload)
				if err := s.switchProviderModel(ctx, p.Provider, p.Model); err != nil {
					applog.Error("session switch provider/model error", applog.M("session_id", s.meta.ID, "provider", p.Provider, "model", p.Model, "error", err))
				}
			}
		}
	}
}

// switchProviderModel updates the session's model within the same provider.
func (s *Session) switchProviderModel(ctx context.Context, providerName, model string) error {
	if providerName == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if _, ok := s.config.Providers[providerName]; !ok {
		return fmt.Errorf("provider '%s' not found in config", providerName)
	}
	if s.meta.Provider != providerName {
		return fmt.Errorf("changing provider requires creating a new session")
	}

	// Same provider: only update model
	s.agent.SetModel(model)
	s.meta.Model = model
	s.meta.UpdatedAt = time.Now()
	if err := s.store.SaveMetadata(ctx, s.meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	applog.Info("session model switched", applog.M("session_id", s.meta.ID, "model", model))
	return nil
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
		content, err := json.Marshal(assistantContentPayload{Content: p.Content})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAssistantContent, "error", err))
			return
		}
		if err := s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAssistantContent, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAssistantContent, "error", err))
		}
		return

	case bus.EventAgentThinking:
		p := ev.Payload.(bus.ThinkingPayload)
		content, err := json.Marshal(p)
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentThinking, "error", err))
			return
		}
		if err := s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentThinking, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentThinking, "error", err))
		}
		return

	case bus.EventAgentStepTokens:
		p := ev.Payload.(bus.StepTokenUsagePayload)
		content, err := json.Marshal(p)
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentStepTokens, "error", err))
			return
		}
		if err := s.store.AppendEvent(ctx, StoredEvent{Timestamp: now, Type: StoredEventAgentStepTokens, Content: content}); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentStepTokens, "error", err))
		}
		return

	case bus.EventAgentToolCallStart:
		p := ev.Payload.(bus.ToolCallStartPayload)
		content, err := json.Marshal(toolCallContent{
			ToolCallID: p.ToolCallID,
			ToolName:   p.ToolName,
			ToolArgs:   p.ToolArgs,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventToolCall, "error", err))
			return
		}
		evt := StoredEvent{Timestamp: now, Type: StoredEventToolCall, Content: content}
		if err := s.store.AppendEvent(ctx, evt); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventToolCall, "error", err))
		}
		return

	case bus.EventAgentToolCallDone:
		p := ev.Payload.(bus.ToolCallDonePayload)
		content, err := json.Marshal(toolResultContent{
			ToolCallID: p.ToolCallID,
			ToolName:   p.ToolName,
			ToolResult: p.ToolResult,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventToolResult, "error", err))
			return
		}
		evt := StoredEvent{Timestamp: now, Type: StoredEventToolResult, Content: content}
		if err := s.store.AppendEvent(ctx, evt); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventToolResult, "error", err))
		}
		return

	case bus.EventAgentDone:
		p := ev.Payload.(bus.AgentDonePayload)
		doneContent, err := json.Marshal(map[string]int64{
			"prompt_tokens":     p.PromptTokens,
			"completion_tokens": p.CompletionTokens,
			"total_tokens":      p.TotalTokens,
		})
		if err != nil {
			applog.Error("session persist marshal error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentDone, "error", err))
			return
		}
		evt := StoredEvent{Timestamp: now, Type: StoredEventAgentDone, Content: doneContent}
		if err := s.store.AppendEvent(ctx, evt); err != nil {
			applog.Error("session persist append error", applog.M("session_id", s.meta.ID, "event_type", StoredEventAgentDone, "error", err))
		}
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
	sessionID := s.meta.ID
	applog.Debug("session close start", applog.M("session_id", sessionID))
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
		if err != nil {
			applog.Error("session close store error", applog.M("session_id", sessionID, "error", err))
			return err
		}
		applog.Debug("session close success", applog.M("session_id", sessionID))
	}
	return nil
}

// Meta returns the session metadata.
func (s *Session) Meta() Metadata {
	return s.meta
}
