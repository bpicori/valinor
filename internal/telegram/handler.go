// Package telegram provides gateway event processing and state transitions.
package telegram

import (
	"context"
	"fmt"

	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/logger"
)

// processEvent handles gateway event work items.
// Similar to TUI's handleWSEventMsg - state-guarded event processing.
func (b *Bot) processEvent(_ context.Context, item *WorkItem) error {
	ev := &item.Event

	// Get chat identity from request tracking, or fall back to work item identity
	identity, ok := b.resolveIdentity(ev.RequestID, item.Identity)
	if !ok {
		return nil
	}

	// Get chat state
	state := b.stateStore.GetOrCreate(identity)

	// State guarding: skip events if not in the right streaming state
	if !b.shouldProcessEvent(ev, state) {
		return nil
	}

	// Handle state transitions based on event type
	if err := b.handleEvent(ev, identity, state); err != nil {
		return err
	}

	// Render and send the event message
	return b.renderAndSend(identity, state, *ev)
}

// resolveIdentity determines the chat identity for an event.
func (b *Bot) resolveIdentity(requestID string, fallback ChatIdentity) (ChatIdentity, bool) {
	if identity, ok := b.getChatForRequest(requestID); ok {
		return identity, true
	}
	if fallback.ChatID == 0 {
		return ChatIdentity{}, false
	}
	return fallback, true
}

// shouldProcessEvent returns true if the event should be processed given current state.
// Always allows session setup events and broadcast events like CronJobResult.
func (b *Bot) shouldProcessEvent(ev *gateway.Event, state *ChatState) bool {
	switch ev.Type {
	case event.SessionAttached, event.NewConversation, event.ChangeModelEvent, event.CronJobResult:
		return true
	default:
		return state.IsStreaming || ev.RequestID == state.CurrentRequestID
	}
}

// handleEvent processes event-specific state transitions.
func (b *Bot) handleEvent(ev *gateway.Event, identity ChatIdentity, state *ChatState) error {
	switch ev.Type {
	case event.AgentContentDelta:
		return b.handleContentDelta(ev, identity, state)
	case event.AgentContentFull:
		return b.handleContentFull(ev, identity, state)
	case event.AgentToolCallStart:
		b.handleToolCallStart(ev, state)
	case event.AgentToolCallDone:
		b.handleToolCallDone(ev, state)
	case event.AgentDone:
		return b.handleAgentDone(ev, identity, state)
	case event.AgentError:
		b.handleAgentError(ev, state)
	case event.AgentCancelled:
		b.handleAgentCancelled(ev, state)
	case event.SessionAttached:
		b.handleSessionAttached(ev, state)
	case event.ChangeModelEvent:
		b.handleModelChanged(ev, state)
	case event.CronJobResult:
		return b.handleCronJobResult(ev, identity)
	}
	return nil
}

// handleContentDelta accumulates streamed content and sends typing action.
func (b *Bot) handleContentDelta(ev *gateway.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(gateway.ContentDeltaPayload); ok {
		state.AppendContent(payload.Content)
	}
	if err := b.sendTypingAction(identity, state); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}
	return nil
}

// handleContentFull replaces accumulated content with full content.
func (b *Bot) handleContentFull(ev *gateway.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(gateway.ContentFullPayload); ok {
		state.SetStreamingContent(payload.Content)
	}
	if err := b.sendTypingAction(identity, state); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}
	return nil
}

// handleToolCallStart tracks a new tool call.
func (b *Bot) handleToolCallStart(ev *gateway.Event, state *ChatState) {
	if payload, ok := ev.Payload.(gateway.ToolCallStartPayload); ok {
		state.AddToolCall(ToolCallInfo{
			ID:   payload.ToolCallID,
			Name: payload.ToolName,
			Args: payload.ToolArgs,
		})
	}
}

// handleToolCallDone marks a tool call as complete.
func (b *Bot) handleToolCallDone(ev *gateway.Event, state *ChatState) {
	if payload, ok := ev.Payload.(gateway.ToolCallDonePayload); ok {
		state.MarkToolCallDone(payload.ToolCallID, payload.ToolResult)
	}
}

// handleAgentDone processes the final agent completion.
func (b *Bot) handleAgentDone(ev *gateway.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(gateway.AgentDonePayload); ok {
		state.UpdateUsage(PromptTokenUsage{
			PromptTokens:     payload.PromptTokens,
			CompletionTokens: payload.CompletionTokens,
			TotalTokens:      payload.TotalTokens,
		})
	}

	finalResult := b.renderer.RenderFinalContent(state.GetStreamingContent())
	state.FinalizeStreaming()
	b.cleanupRequest(ev.RequestID)

	if finalResult != nil {
		if err := b.sendTelegramMessage(identity, state, finalResult); err != nil {
			return fmt.Errorf("send final message: %w", err)
		}
	}
	return nil
}

// handleAgentError processes agent errors.
func (b *Bot) handleAgentError(ev *gateway.Event, state *ChatState) {
	state.FinalizeStreaming()
	b.cleanupRequest(ev.RequestID)
}

// handleAgentCancelled processes agent cancellation.
func (b *Bot) handleAgentCancelled(ev *gateway.Event, state *ChatState) {
	state.FinalizeStreaming()
	b.cleanupRequest(ev.RequestID)
}

// handleSessionAttached stores session info for /status command.
func (b *Bot) handleSessionAttached(ev *gateway.Event, state *ChatState) {
	if payload, ok := ev.Payload.(gateway.SessionAttachedPayload); ok {
		state.UpdateSessionInfo(payload.SessionKey, payload.Provider, payload.Model)
	}
}

// handleModelChanged updates provider/model in state.
func (b *Bot) handleModelChanged(ev *gateway.Event, state *ChatState) {
	if payload, ok := ev.Payload.(gateway.ChangeProviderModelPayload); ok {
		state.ProviderName = payload.Provider
		state.ModelName = payload.Model
	}
}

// handleCronJobResult sends scheduled job results to the chat.
func (b *Bot) handleCronJobResult(ev *gateway.Event, identity ChatIdentity) error {
	if payload, ok := ev.Payload.(event.CronJobResultPayload); ok {
		// Build display title - use job name if available
		title := "Scheduled Task"
		if payload.JobName != "" {
			title = payload.JobName
		}

		content := fmt.Sprintf("%s (%s)\n\n%s",
			title,
			payload.Timestamp.Format("15:04"),
			payload.Content)
		result := &RenderResult{
			Text:      content,
			ParseMode: "",
			IsFinal:   true,
		}

		// Get or create chat state for tracking the message
		state := b.stateStore.GetOrCreate(identity)

		if err := b.sendTelegramMessage(identity, state, result); err != nil {
			return fmt.Errorf("send cron job result: %w", err)
		}
	}
	return nil
}

// renderAndSend renders an event and sends it via Telegram API.
func (b *Bot) renderAndSend(identity ChatIdentity, state *ChatState, ev gateway.Event) error {
	result := b.renderer.RenderEvent(ev)
	if result == nil {
		return nil
	}
	if err := b.sendTelegramMessage(identity, state, result); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}
