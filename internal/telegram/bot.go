// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"valinor/internal/applog"
	"valinor/internal/event"
	"valinor/internal/router"
)

const (
	typingActionInterval      = 4 * time.Second
	typingActionErrorCooldown = 2 * time.Second
	defaultTypingRetryAfter   = 3 * time.Second
)

// Bot is the main Telegram bot orchestrator.
// It coordinates the poller, dispatcher, sequencer, bridge, and renderer.
// Flow architecture mirrors TUI: all work items (commands + events) flow through
// per-chat sequential processing for state consistency.
type Bot struct {
	// Configuration
	config Config

	// Components
	client     *Client
	store      *OffsetStore
	dispatcher *Dispatcher
	poller     *Poller
	sequencer  *Sequencer
	bridge     *Bridge
	renderer   *Renderer

	// Per-chat state management (replaces old chatStates map)
	stateStore *ChatStateStore

	// Request tracking for routing gateway events to chats
	requestChats map[string]ChatIdentity
	requestMu    sync.RWMutex

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ready  atomic.Bool
}

// Config holds the Telegram bot configuration.
type Config struct {
	// BotToken is the Telegram Bot API token (required)
	BotToken string
	// GatewayURL is the WebSocket URL for the Valinor gateway (required)
	GatewayURL string
	// StorageDir is the directory for persisting offset (optional, defaults to ~/.valinor)
	StorageDir string
	// PollTimeout is the long-polling timeout in seconds (default: 30)
	PollTimeout int
	// RequireMention requires bot mention in groups (default: false)
	RequireMention bool
}

// NewBot creates a new Telegram bot instance.
func NewBot(cfg Config) (*Bot, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("bot token is required")
	}
	if cfg.GatewayURL == "" {
		return nil, fmt.Errorf("gateway URL is required")
	}

	return &Bot{
		config:       cfg,
		stateStore:   NewChatStateStore(),
		requestChats: make(map[string]ChatIdentity),
	}, nil
}

// Start initializes and starts all bot components.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Initialize API client
	b.client = NewClient(b.config.BotToken)

	// Verify bot token by calling getMe
	me, err := b.client.GetMe(b.ctx)
	if err != nil {
		return fmt.Errorf("verify bot token: %w", err)
	}
	applog.Info("telegram bot: authenticated",
		applog.BuildMetadata("username", me.Username, "id", me.ID))

	// Initialize offset store
	store, err := NewOffsetStore(b.config.StorageDir)
	if err != nil {
		return fmt.Errorf("initialize offset store: %w", err)
	}
	b.store = store

	// Initialize dispatcher
	b.dispatcher = NewDispatcher(me.Username)
	b.dispatcher.SetRequireMention(b.config.RequireMention)

	// Initialize stateless renderer
	b.renderer = NewRenderer()

	// Initialize sequencer with unified work processor
	// This is key: both commands AND events go through the same sequential path
	b.sequencer = NewSequencer(b.processWorkItem)

	// Initialize bridge with gateway event handler
	// Events are routed through the sequencer for per-chat serialization
	b.bridge = NewBridge(b.config.GatewayURL, "telegram", b.handleGatewayEvent)

	// Start bridge first (we need to be connected before handling commands)
	if err := b.bridge.Start(b.ctx); err != nil {
		return fmt.Errorf("start bridge: %w", err)
	}

	// Initialize poller
	b.poller = NewPoller(b.client, b.store, b.handleUpdate)
	if b.config.PollTimeout > 0 {
		b.poller.SetPollTimeout(b.config.PollTimeout)
	}

	// Start poller
	b.poller.Start(b.ctx)

	b.ready.Store(true)
	applog.Info("telegram bot: started", nil)

	return nil
}

// Stop gracefully stops all bot components.
func (b *Bot) Stop() {
	if !b.ready.Load() {
		return
	}

	applog.Info("telegram bot: stopping", nil)

	b.cancel()

	// Stop in reverse order of initialization
	if b.poller != nil {
		b.poller.Stop()
	}
	if b.bridge != nil {
		b.bridge.Stop()
	}
	if b.sequencer != nil {
		b.sequencer.Stop()
	}

	b.wg.Wait()
	b.ready.Store(false)

	applog.Info("telegram bot: stopped", nil)
}

// IsReady returns true if the bot is fully started and ready.
func (b *Bot) IsReady() bool {
	return b.ready.Load()
}

// handleUpdate is called by the poller for each received Telegram update.
func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
	// Dispatch the update to get a command
	cmd, err := b.dispatcher.Dispatch(ctx, update)
	if err != nil {
		return fmt.Errorf("dispatch update %d: %w", update.UpdateID, err)
	}
	if cmd == nil {
		// Update was filtered out (no message, etc.)
		return nil
	}

	// Create work item for the command
	item := &WorkItem{
		Type:      WorkItemCommand,
		Command:   cmd,
		RequestID: cmd.RequestID,
	}

	// Submit to sequencer for ordered processing
	key := cmd.GetSequentialKey()
	if !b.sequencer.Submit(item, key) {
		return fmt.Errorf("sequencer queue full for %s", key)
	}

	return nil
}

// handleGatewayEvent is called by the bridge when a gateway event is received.
// The event is converted to a work item and routed through the sequencer
// to ensure per-chat serialization (matching TUI's single-threaded Update() flow).
func (b *Bot) handleGatewayEvent(ev router.Event) {
	// Get chat identity from request tracking
	identity, ok := b.getChatForRequest(ev.RequestID)
	if !ok {
		applog.Warning("telegram bot: received event for unknown request",
			applog.BuildMetadata("request_id", ev.RequestID, "event_type", ev.Type))
		return
	}

	// Create work item for the event
	item := &WorkItem{
		Type:      WorkItemEvent,
		Event:     ev,
		RequestID: ev.RequestID,
		Identity:  identity,
	}

	// Determine sequential key (control lane for final events if we want priority)
	// For now, use regular sequential key to preserve order
	key := identity.SequentialKey()

	// Submit to sequencer for ordered processing alongside commands
	// This is the key change: events now flow through the same sequential path
	if !b.sequencer.Submit(item, key) {
		applog.Warning("telegram bot: event dropped, sequencer queue full",
			applog.BuildMetadata("request_id", ev.RequestID, "key", key))
	}

	// Note: Final event cleanup happens in processWorkItem to ensure proper sequencing
}

// processWorkItem is the unified work processor called by the sequencer.
// This mirrors TUI's Update() - all state changes happen sequentially per chat.
func (b *Bot) processWorkItem(ctx context.Context, item *WorkItem) error {
	switch item.Type {
	case WorkItemCommand:
		return b.processCommand(ctx, item)
	case WorkItemEvent:
		return b.processEvent(ctx, item)
	default:
		return fmt.Errorf("unknown work item type: %v", item.Type)
	}
}

// processCommand handles command work items.
// Similar to TUI's command handling in handleKeyMsg/handleSubmit.
func (b *Bot) processCommand(_ context.Context, item *WorkItem) error {
	cmd := item.Command
	if cmd == nil {
		return fmt.Errorf("nil command in work item")
	}

	// Get or create chat state (like TUI's model initialization)
	state := b.stateStore.GetOrCreate(cmd.Identity)

	// Track this request for response routing
	b.trackRequest(cmd.RequestID, cmd.Identity)

	// For control commands (cancel), send immediately without state checks
	// This maintains the fast-path for cancellation
	if cmd.IsControlCommand {
		if !b.bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
			return fmt.Errorf("failed to send control command")
		}
		return nil
	}

	// TUI-like state guarding: check if we can start a new request
	if !state.CanStartNewRequest() {
		// Send a notice that we're busy
		notice := &RenderResult{
			Text:      "⏳ Please wait for the current response to complete.",
			ParseMode: "MarkdownV2",
		}
		return b.sendTelegramMessage(cmd.Identity, state, notice)
	}

	// Handle new conversation command
	if cmd.Type == router.CommandNewConversation {
		// Reset streaming state (like TUI's handleNewConversation)
		state.FinalizeStreaming()
	}

	// Send command to gateway
	if !b.bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
		return fmt.Errorf("failed to send command")
	}

	// Mark as streaming for user messages
	if cmd.Type == router.CommandUserMessage {
		state.StartStreaming(cmd.RequestID)
	}

	return nil
}

// processEvent handles gateway event work items.
// Similar to TUI's handleWSEventMsg - state-guarded event processing.
func (b *Bot) processEvent(ctx context.Context, item *WorkItem) error {
	ev := &item.Event

	// Get chat identity from request tracking
	identity, ok := b.getChatForRequest(ev.RequestID)
	if !ok {
		// Event arrived after request was cleaned up - ignore
		return nil
	}

	// Get chat state
	state := b.stateStore.GetOrCreate(identity)

	// TUI-like state guarding: ignore events if not in streaming state
	// (except for session setup events which are always processed)
	switch ev.Type {
	case event.EventType(router.EventSessionAttached),
		event.EventType(router.EventNewConversation),
		event.EventType(router.EventChangeProviderModel):
		// Always process these events regardless of streaming state
	default:
		// For streaming-related events, check if we're in the right state
		if !state.IsStreaming && ev.RequestID != state.CurrentRequestID {
			// Event for a completed or unknown request - ignore
			return nil
		}
	}

	// Handle state transitions based on event type (TUI-like state machine)
	switch ev.Type {
	case event.EventType(router.EventAgentContentDelta):
		// Accumulate streamed content but only show Telegram typing action.
		if payload, ok := ev.Payload.(router.ContentDeltaPayload); ok {
			state.AppendContent(payload.Content)
		}
		if err := b.sendTypingAction(identity, state); err != nil {
			applog.Warning("telegram bot: send typing action failed",
				applog.BuildMetadata("error", err, "chat_id", identity.ChatID))
		}
		return nil

	case event.EventType(router.EventAgentContentFull):
		// Full content events replace the current accumulated text.
		if payload, ok := ev.Payload.(router.ContentFullPayload); ok {
			state.SetStreamingContent(payload.Content)
		}
		if err := b.sendTypingAction(identity, state); err != nil {
			applog.Warning("telegram bot: send typing action failed",
				applog.BuildMetadata("error", err, "chat_id", identity.ChatID))
		}
		return nil

	case event.EventType(router.EventAgentToolCallStart):
		// Track tool call like TUI
		if payload, ok := ev.Payload.(router.ToolCallStartPayload); ok {
			state.AddToolCall(ToolCallInfo{
				ID:   payload.ToolCallID,
				Name: payload.ToolName,
				Args: payload.ToolArgs,
			})
		}

	case event.EventType(router.EventAgentToolCallDone):
		// Mark tool call done
		if payload, ok := ev.Payload.(router.ToolCallDonePayload); ok {
			state.MarkToolCallDone(payload.ToolCallID, payload.ToolResult)
		}

	case event.EventType(router.EventAgentDone):
		// Send a single final message with the accumulated content.
		if payload, ok := ev.Payload.(router.AgentDonePayload); ok {
			state.UpdateUsage(PromptTokenUsage{
				PromptTokens:     payload.PromptTokens,
				CompletionTokens: payload.CompletionTokens,
				TotalTokens:      payload.TotalTokens,
			})
		}

		finalResult := b.renderer.RenderFinalContent(state.StreamingContent)
		state.FinalizeStreaming()
		b.cleanupRequest(ev.RequestID)
		if finalResult != nil {
			if err := b.sendTelegramMessage(identity, state, finalResult); err != nil {
				return fmt.Errorf("send final message: %w", err)
			}
			return nil
		}

	case event.EventType(router.EventAgentError):
		// Handle error like TUI's handleAgentError
		state.FinalizeStreaming()
		b.cleanupRequest(ev.RequestID)

	case event.EventType(router.EventAgentCancelled):
		// Handle cancellation like TUI's handleAgentCancelled
		state.FinalizeStreaming()
		b.cleanupRequest(ev.RequestID)
	}

	// Render the event to a Telegram message.
	result := b.renderer.RenderEvent(*ev)
	if result == nil {
		return nil
	}

	// Send the message via Telegram API
	if err := b.sendTelegramMessage(identity, state, result); err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	return nil
}

// isFinalEvent returns true if this event type indicates the end of a request.
func isFinalEvent(evType event.EventType) bool {
	switch evType {
	case event.AgentDone, event.AgentError, event.AgentCancelled:
		return true
	default:
		return false
	}
}

// trackRequest maps a request ID to a chat identity for response routing.
func (b *Bot) trackRequest(requestID string, identity ChatIdentity) {
	b.requestMu.Lock()
	defer b.requestMu.Unlock()
	b.requestChats[requestID] = identity
}

// getChatForRequest retrieves the chat identity for a request ID.
func (b *Bot) getChatForRequest(requestID string) (ChatIdentity, bool) {
	b.requestMu.RLock()
	defer b.requestMu.RUnlock()
	identity, ok := b.requestChats[requestID]
	return identity, ok
}

// cleanupRequest removes a request from tracking.
func (b *Bot) cleanupRequest(requestID string) {
	b.requestMu.Lock()
	defer b.requestMu.Unlock()
	delete(b.requestChats, requestID)
}

// sendTypingAction updates the chat with Telegram's typing indicator.
// It uses per-chat pacing/backoff to avoid Telegram 429 rate limits.
func (b *Bot) sendTypingAction(identity ChatIdentity, state *ChatState) error {
	now := time.Now()
	if !state.CanSendTypingAction(now) {
		return nil
	}

	req := SendChatActionRequest{
		ChatID: identity.ChatID,
		Action: "typing",
	}
	if identity.ThreadID != nil {
		req.MessageThreadID = identity.ThreadID
	}

	if err := b.client.SendChatAction(b.ctx, req); err != nil {
		retryAfter := typingActionErrorCooldown
		if apiErr, ok := err.(*APIResponse); ok && apiErr.ErrorCode == 429 {
			retryAfter = defaultTypingRetryAfter
			if apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
				retryAfter = time.Duration(apiErr.Parameters.RetryAfter) * time.Second
			}
		}
		state.BackoffTypingAction(now, retryAfter)
		return err
	}

	state.MarkTypingActionSent(now, typingActionInterval)
	return nil
}

// sendTelegramMessage sends a message via the Telegram API.
// Supports both sending new messages and editing existing ones for streaming updates.
// This now uses ChatState for determining edit vs new message (TUI-like lastMessage pattern).
func (b *Bot) sendTelegramMessage(identity ChatIdentity, state *ChatState, result *RenderResult) error {
	// Determine if we should edit or send a new message
	// TUI-like pattern: edit the streaming message if we're in streaming mode
	shouldEdit := result.IsStreaming && state.GetStreamingMessageID() != 0

	if shouldEdit {
		// Edit existing streaming message
		req := EditMessageTextRequest{
			ChatID:      identity.ChatID,
			MessageID:   state.GetStreamingMessageID(),
			Text:        result.Text,
			ParseMode:   result.ParseMode,
			ReplyMarkup: result.ReplyMarkup,
		}

		// Handle thread ID for forum topics
		if identity.ThreadID != nil {
			applog.Debug("telegram bot: editing message in thread",
				applog.BuildMetadata("chat_id", identity.ChatID, "thread_id", *identity.ThreadID, "message_id", state.GetStreamingMessageID()))
		}

		_, err := b.client.EditMessageText(b.ctx, req)
		if err != nil {
			// If editing fails (e.g., message too old), fall back to sending new message
			applog.Warning("telegram bot: edit failed, sending new message",
				applog.BuildMetadata("error", err, "chat_id", identity.ChatID))
			return b.sendNewMessage(identity, state, result)
		}

		return nil
	}

	// Send new message
	return b.sendNewMessage(identity, state, result)
}

// sendNewMessage sends a new Telegram message.
// Sets the streaming message ID if this is the first message during streaming.
func (b *Bot) sendNewMessage(identity ChatIdentity, state *ChatState, result *RenderResult) error {
	req := SendMessageRequest{
		ChatID:      identity.ChatID,
		Text:        result.Text,
		ParseMode:   result.ParseMode,
		ReplyMarkup: result.ReplyMarkup,
	}

	// Handle thread ID for forum topics
	if identity.ThreadID != nil {
		req.MessageThreadID = identity.ThreadID
	}

	msg, err := b.client.SendMessage(b.ctx, req)
	if err != nil {
		return fmt.Errorf("send message to chat %d: %w", identity.ChatID, err)
	}

	// Update state with new message ID
	state.UpdateLastMessageID(msg.MessageID)

	// Set as streaming message ID if we're in streaming mode and don't have one yet
	// This ensures subsequent deltas edit this message (TUI-like pattern)
	if result.IsStreaming && state.GetStreamingMessageID() == 0 {
		state.SetStreamingMessageID(msg.MessageID)
	}

	applog.Debug("telegram bot: message sent",
		applog.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID))

	return nil
}

// GetStats returns bot statistics for monitoring.
func (b *Bot) GetStats() BotStats {
	stats := BotStats{
		Ready: b.ready.Load(),
	}

	if b.sequencer != nil {
		stats.ActiveChats = b.sequencer.ActiveKeys()
		stats.TotalQueued = b.sequencer.TotalQueued()
	}

	if b.bridge != nil {
		stats.Connected = b.bridge.IsConnected()
	}

	if b.poller != nil {
		stats.Polling = b.poller.IsRunning()
	}

	return stats
}

// BotStats contains bot statistics.
type BotStats struct {
	Ready       bool
	Connected   bool
	Polling     bool
	ActiveChats int
	TotalQueued int
}
