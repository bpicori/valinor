// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"valinor/internal/applog"
	"valinor/internal/event"
	"valinor/internal/router"
)

// Bot is the main Telegram bot orchestrator.
// It coordinates the poller, dispatcher, sequencer, bridge, and renderer.
type Bot struct {
	// Configuration
	config Config

	// Components
	client    *Client
	store     *OffsetStore
	dispatcher *Dispatcher
	poller    *Poller
	sequencer *Sequencer
	bridge    *Bridge
	renderer  *Renderer

	// Per-chat state tracking
	chatStates map[string]*ChatState
	chatMu     sync.RWMutex

	// Request tracking for response routing
	requestChats map[string]ChatIdentity
	requestMu    sync.RWMutex

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ready  atomic.Bool
}

// ChatState tracks the state for a single chat conversation.
type ChatState struct {
	Identity       ChatIdentity
	LastMessageID  int64
	IsStreaming    bool
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
	// Allowlist is the list of allowed usernames (empty = allow all)
	Allowlist []string
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
		chatStates:   make(map[string]*ChatState),
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
	b.dispatcher.SetAllowlist(b.config.Allowlist)
	b.dispatcher.SetRequireMention(b.config.RequireMention)

	// Initialize renderer
	b.renderer = NewRenderer()

	// Initialize sequencer
	b.sequencer = NewSequencer(b.handleCommand)

	// Initialize bridge
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

// handleUpdate is called by the poller for each received update.
func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
	// Dispatch the update to get a command
	cmd, err := b.dispatcher.Dispatch(ctx, update)
	if err != nil {
		return fmt.Errorf("dispatch update %d: %w", update.UpdateID, err)
	}
	if cmd == nil {
		// Update was filtered out (allowlist, no message, etc.)
		return nil
	}

	// Submit to sequencer for ordered processing
	if !b.sequencer.Submit(cmd) {
		return fmt.Errorf("sequencer queue full for %s", cmd.GetSequentialKey())
	}

	return nil
}

// handleCommand is called by the sequencer for each command (sequential per chat).
func (b *Bot) handleCommand(ctx context.Context, cmd *Command) error {
	// Initialize or get chat state
	state := b.getOrCreateChatState(cmd.Identity)

	// Track this request for response routing
	b.trackRequest(cmd.RequestID, cmd.Identity)

	// For control commands (cancel), send immediately
	if cmd.IsControlCommand {
		if !b.bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
			return fmt.Errorf("failed to send control command")
		}
		return nil
	}

	// For new conversation, clear streaming state
	if cmd.Type == router.CommandNewConversation {
		state.IsStreaming = false
		b.renderer.ClearStreamingMessageID()
	}

	// Send command to gateway
	if !b.bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
		return fmt.Errorf("failed to send command")
	}

	// Mark as streaming for content deltas
	if cmd.Type == router.CommandUserMessage {
		state.IsStreaming = true
	}

	return nil
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

// handleGatewayEvent is called by the bridge when an event is received.
func (b *Bot) handleGatewayEvent(ev router.Event) {
	// Render the event to a Telegram message
	result := b.renderer.RenderEvent(ev)
	if result == nil {
		// Event doesn't need a message (e.g., step tokens)
		return
	}

	// Get chat identity from request tracking
	identity, ok := b.getChatForRequest(ev.RequestID)
	if !ok {
		applog.Warning("telegram bot: received event for unknown request",
			applog.BuildMetadata("request_id", ev.RequestID, "event_type", ev.Type))
		return
	}

	// Send the message via Telegram API
	if err := b.sendTelegramMessage(identity, result); err != nil {
		applog.Error("telegram bot: failed to send message",
			applog.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}

	// If this was a final event (done, error, cancelled), clear streaming state and cleanup
	switch ev.Type {
	case event.AgentDone, event.AgentError, event.AgentCancelled:
		b.renderer.ClearStreamingMessageID()
		b.cleanupRequest(ev.RequestID)
	}
}

// sendTelegramMessage sends a message via the Telegram API.
// Supports both sending new messages and editing existing ones for streaming updates.
func (b *Bot) sendTelegramMessage(identity ChatIdentity, result *RenderResult) error {
	state := b.getOrCreateChatState(identity)

	// Determine if we should edit or send a new message
	if result.EditMessageID != 0 && state.LastMessageID != 0 {
		// Edit existing message
		req := EditMessageTextRequest{
			ChatID:    identity.ChatID,
			MessageID: result.EditMessageID,
			Text:      result.Text,
			ParseMode: result.ParseMode,
			ReplyMarkup: result.ReplyMarkup,
		}

		// Handle thread ID for forum topics
		if identity.ThreadID != nil {
			// Telegram API doesn't support editing messages in threads differently,
			// but we include it in logging for debugging
			applog.Debug("telegram bot: editing message in thread",
				applog.BuildMetadata("chat_id", identity.ChatID, "thread_id", *identity.ThreadID, "message_id", result.EditMessageID))
		}

		msg, err := b.client.EditMessageText(b.ctx, req)
		if err != nil {
			// If editing fails (e.g., message too old), fall back to sending new message
			applog.Warning("telegram bot: edit failed, sending new message",
				applog.BuildMetadata("error", err, "chat_id", identity.ChatID))
			return b.sendNewMessage(identity, state, result)
		}

		// Update last message ID (editing preserves it)
		state.LastMessageID = msg.MessageID
		return nil
	}

	// Send new message
	return b.sendNewMessage(identity, state, result)
}

// sendNewMessage sends a new Telegram message.
// Only sets this as the streaming message if it's the first message during a streaming session.
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
	state.LastMessageID = msg.MessageID

	// Only set as streaming message ID if we don't already have one.
	// This ensures tool call indicators and other status messages don't
	// replace the main AI response message being streamed.
	if state.IsStreaming && b.renderer.GetStreamingMessageID() == 0 {
		b.renderer.SetStreamingMessageID(msg.MessageID)
	}

	applog.Debug("telegram bot: message sent",
		applog.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID))

	return nil
}

// getOrCreateChatState gets or initializes state for a chat.
func (b *Bot) getOrCreateChatState(identity ChatIdentity) *ChatState {
	key := identity.SequentialKey()

	b.chatMu.Lock()
	defer b.chatMu.Unlock()

	if state, ok := b.chatStates[key]; ok {
		return state
	}

	state := &ChatState{
		Identity: identity,
	}
	b.chatStates[key] = state
	return state
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
