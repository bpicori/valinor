// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"valinor/internal/event"
	"valinor/internal/logger"
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
	client    *Client
	store     *offsetStore
	poller    *Poller
	sequencer *Sequencer
	bridges   map[string]*Bridge
	renderer  *Renderer

	// Per-chat state management (replaces old chatStates map)
	stateStore *ChatStateStore

	// Request tracking for routing gateway events to chats
	requestChats map[string]ChatIdentity
	requestMu    sync.RWMutex
	bridgesMu    sync.RWMutex

	// Dispatch state
	botUsername    string // Bot's username for mention checking
	requireMention bool   // Require bot mention in groups

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ready  atomic.Bool
}

// Command represents a parsed Telegram command ready for routing.
type Command struct {
	// Identity is the chat identity for session routing
	Identity ChatIdentity
	// RequestID is a correlation ID for tracking
	RequestID string
	// Type is the router command type
	Type string
	// Payload is the typed command payload
	Payload interface{}
	// RawText is the original message text
	RawText string
	// IsControlCommand is true if this is a cancel/stop command
	IsControlCommand bool
}

// GetSequentialKey returns the appropriate sequential key for the command.
// Control commands use a separate lane to avoid blocking regular messages.
func (c *Command) GetSequentialKey() string {
	if c.IsControlCommand {
		return c.Identity.ControlLaneKey()
	}
	return c.Identity.SequentialKey()
}

// dispatch parses an update and returns a command if it should be processed.
// Returns nil if the update should be ignored (e.g., not allowed, no message, etc.).
func (b *Bot) dispatch(update Update) (*Command, error) {
	// Extract message from update
	msg := b.extractMessage(update)
	if msg == nil {
		return nil, nil // No message to process
	}

	// Check mention requirement in groups
	if b.requireMention && b.isGroupChat(msg.Chat) && !b.isMentioned(msg) {
		logger.Debug("telegram bot: bot not mentioned in group",
			logger.BuildMetadata("chat_id", msg.Chat.ID))
		return nil, nil
	}

	// Build chat identity
	identity := FromTelegramUpdate(msg.Chat)
	if msg.MessageThreadID != nil {
		identity = identity.WithThreadID(*msg.MessageThreadID)
	}

	// Parse command or message
	return b.parseCommand(identity, msg, update.UpdateID)
}

// extractMessage extracts the message from an update.
func (b *Bot) extractMessage(update Update) *Message {
	if update.Message != nil {
		return update.Message
	}
	if update.EditedMessage != nil {
		return update.EditedMessage
	}
	if update.ChannelPost != nil {
		return update.ChannelPost
	}
	// Callback queries don't have direct messages, handled separately
	return nil
}

// isGroupChat returns true if the chat is a group or supergroup.
func (b *Bot) isGroupChat(chat *Chat) bool {
	if chat == nil {
		return false
	}
	return chat.Type == "group" || chat.Type == "supergroup"
}

// isMentioned checks if the bot is mentioned in the message.
func (b *Bot) isMentioned(msg *Message) bool {
	if msg == nil || b.botUsername == "" {
		return false
	}

	botUsernameLower := strings.ToLower(b.botUsername)

	for _, entity := range msg.Entities {
		if entity.Type == "mention" {
			// Extract the mention text
			start := entity.Offset
			end := entity.Offset + entity.Length
			if start >= 0 && end <= int64(len(msg.Text)) {
				mention := strings.ToLower(msg.Text[start:end])
				if mention == "@"+botUsernameLower {
					return true
				}
			}
		}
		if entity.Type == "text_mention" {
			// Text mention with user object
			if entity.User != nil && strings.ToLower(entity.User.Username) == botUsernameLower {
				return true
			}
		}
	}

	// Also check if message text starts with @botname
	if strings.HasPrefix(strings.ToLower(msg.Text), "@"+botUsernameLower+" ") ||
		strings.ToLower(msg.Text) == "@"+botUsernameLower {
		return true
	}

	return false
}

// parseCommand parses a message into a Command.
func (b *Bot) parseCommand(identity ChatIdentity, msg *Message, updateID int64) (*Command, error) {
	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)

	// Check for commands
	if msg.IsCommand() {
		cmd, args := msg.CommandWithArgs()
		cmdLower := strings.ToLower(cmd)

		switch cmdLower {
		case "new", "clear":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandNewConversation,
				RawText:   msg.Text,
			}, nil

		case "cancel":
			return &Command{
				Identity:         identity,
				RequestID:        requestID,
				Type:             router.CommandCancelAgentRun,
				RawText:          msg.Text,
				IsControlCommand: true,
			}, nil

		case "model":
			// /model provider model - requires args
			parts := strings.Fields(args)
			if len(parts) >= 2 {
				return &Command{
					Identity:  identity,
					RequestID: requestID,
					Type:      router.CommandChangeProviderModel,
					Payload: router.ChangeProviderModelPayload{
						Provider: parts[0],
						Model:    strings.Join(parts[1:], " "),
					},
					RawText: msg.Text,
				}, nil
			}
			// Invalid model command - treat as regular message explaining usage
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandUserMessage,
				Payload: router.UserMessagePayload{
					Content: "To change the model, use: /model <provider> <model>",
				},
				RawText: msg.Text,
			}, nil

		case "status":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      "status", // Internal command type, not sent to router
				RawText:   msg.Text,
			}, nil

		default:
			// Unknown command - treat as regular message
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandUserMessage,
				Payload: router.UserMessagePayload{
					Content: msg.Text,
				},
				RawText: msg.Text,
			}, nil
		}
	}

	// Regular message
	return &Command{
		Identity:  identity,
		RequestID: requestID,
		Type:      router.CommandUserMessage,
		Payload: router.UserMessagePayload{
			Content: msg.Text,
		},
		RawText: msg.Text,
	}, nil
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
		bridges:      make(map[string]*Bridge),
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
	logger.Info("telegram bot: authenticated",
		logger.BuildMetadata("username", me.Username, "id", me.ID))

	// Initialize offset store
	store, err := newOffsetStore(b.config.StorageDir)
	if err != nil {
		return fmt.Errorf("initialize offset store: %w", err)
	}
	b.store = store

	// Initialize dispatch state
	b.botUsername = me.Username
	b.requireMention = b.config.RequireMention

	// Initialize stateless renderer
	b.renderer = NewRenderer()

	// Initialize sequencer with unified work processor
	// This is key: both commands AND events go through the same sequential path
	b.sequencer = NewSequencer(b.processWorkItem)

	// Initialize poller
	b.poller = NewPoller(b.client, b.store, b.handleUpdate)
	if b.config.PollTimeout > 0 {
		b.poller.SetPollTimeout(b.config.PollTimeout)
	}

	// Start poller
	b.poller.Start(b.ctx)

	b.ready.Store(true)
	logger.Info("telegram bot: started", nil)

	return nil
}

// Stop gracefully stops all bot components.
func (b *Bot) Stop() {
	if !b.ready.Load() {
		return
	}

	logger.Info("telegram bot: stopping", nil)

	b.cancel()

	// Stop in reverse order of initialization
	if b.poller != nil {
		b.poller.Stop()
	}
	b.stopAllBridges()
	if b.sequencer != nil {
		b.sequencer.Stop()
	}

	b.wg.Wait()
	b.ready.Store(false)

	logger.Info("telegram bot: stopped", nil)
}

// IsReady returns true if the bot is fully started and ready.
func (b *Bot) IsReady() bool {
	return b.ready.Load()
}

// handleUpdate is called by the poller for each received Telegram update.
func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
	// Dispatch the update to get a command
	cmd, err := b.dispatch(update)
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
func (b *Bot) handleGatewayEvent(sourceIdentity ChatIdentity, ev router.Event) {
	// Get chat identity from request tracking, or from session setup events
	identity, ok := b.getChatForRequest(ev.RequestID)
	if !ok {
		// For events without request correlation (for example SessionAttached),
		// use the bridge-bound chat identity.
		identity = sourceIdentity
	}

	if identity.ChatID == 0 {
		logger.Warning("telegram bot: received event for unknown request",
			logger.BuildMetadata("request_id", ev.RequestID, "event_type", ev.Type))
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
		logger.Warning("telegram bot: event dropped, sequencer queue full",
			logger.BuildMetadata("request_id", ev.RequestID, "key", key))
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
	bridge, err := b.getOrCreateBridge(cmd.Identity)
	if err != nil {
		return fmt.Errorf("get bridge for chat: %w", err)
	}
	b.hydrateStateFromBridge(state, bridge)

	// Track this request for response routing
	b.trackRequest(cmd.RequestID, cmd.Identity)

	// For control commands (cancel), send immediately without state checks
	// This maintains the fast-path for cancellation
	if cmd.IsControlCommand {
		if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
			return fmt.Errorf("failed to send control command")
		}
		return nil
	}

	// Handle local status command (doesn't go to gateway)
	if cmd.Type == "status" {
		result := b.renderer.RenderStatus(state.SessionKey, state.ProviderName, state.ModelName)
		return b.sendTelegramMessage(cmd.Identity, state, result)
	}

	// TUI-like state guarding: check if we can start a new request
	if !state.CanStartNewRequest() {
		// Send a notice that we're busy
		notice := &RenderResult{
			Text:      "⏳ Please wait for the current response to complete\\.",
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
	if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
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
// Always allows session setup events; requires matching streaming state for others.
func (b *Bot) shouldProcessEvent(ev *router.Event, state *ChatState) bool {
	switch ev.Type {
	case event.SessionAttached, event.NewConversation, event.ChangeModelEvent:
		return true
	default:
		return state.IsStreaming || ev.RequestID == state.CurrentRequestID
	}
}

// handleEvent processes event-specific state transitions.
func (b *Bot) handleEvent(ev *router.Event, identity ChatIdentity, state *ChatState) error {
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
func (b *Bot) handleContentDelta(ev *router.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(router.ContentDeltaPayload); ok {
		state.AppendContent(payload.Content)
	}
	if err := b.sendTypingAction(identity, state); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}
	return nil
}

// handleContentFull replaces accumulated content with full content.
func (b *Bot) handleContentFull(ev *router.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(router.ContentFullPayload); ok {
		state.SetStreamingContent(payload.Content)
	}
	if err := b.sendTypingAction(identity, state); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}
	return nil
}

// handleToolCallStart tracks a new tool call.
func (b *Bot) handleToolCallStart(ev *router.Event, state *ChatState) {
	if payload, ok := ev.Payload.(router.ToolCallStartPayload); ok {
		state.AddToolCall(ToolCallInfo{
			ID:   payload.ToolCallID,
			Name: payload.ToolName,
			Args: payload.ToolArgs,
		})
	}
}

// handleToolCallDone marks a tool call as complete.
func (b *Bot) handleToolCallDone(ev *router.Event, state *ChatState) {
	if payload, ok := ev.Payload.(router.ToolCallDonePayload); ok {
		state.MarkToolCallDone(payload.ToolCallID, payload.ToolResult)
	}
}

// handleAgentDone processes the final agent completion.
func (b *Bot) handleAgentDone(ev *router.Event, identity ChatIdentity, state *ChatState) error {
	if payload, ok := ev.Payload.(router.AgentDonePayload); ok {
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
func (b *Bot) handleAgentError(ev *router.Event, state *ChatState) {
	state.FinalizeStreaming()
	b.cleanupRequest(ev.RequestID)
}

// handleAgentCancelled processes agent cancellation.
func (b *Bot) handleAgentCancelled(ev *router.Event, state *ChatState) {
	state.FinalizeStreaming()
	b.cleanupRequest(ev.RequestID)
}

// handleSessionAttached stores session info for /status command.
func (b *Bot) handleSessionAttached(ev *router.Event, state *ChatState) {
	if payload, ok := ev.Payload.(router.SessionAttachedPayload); ok {
		state.UpdateSessionInfo(payload.SessionKey, payload.Provider, payload.Model)
	}
}

// handleModelChanged updates provider/model in state.
func (b *Bot) handleModelChanged(ev *router.Event, state *ChatState) {
	if payload, ok := ev.Payload.(router.ChangeProviderModelPayload); ok {
		state.ProviderName = payload.Provider
		state.ModelName = payload.Model
	}
}

// handleCronJobResult sends scheduled job results to the chat.
func (b *Bot) handleCronJobResult(ev *router.Event, identity ChatIdentity) error {
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
		if err := b.sendTelegramMessage(identity, nil, result); err != nil {
			return fmt.Errorf("send cron job result: %w", err)
		}
	}
	return nil
}

// renderAndSend renders an event and sends it via Telegram API.
func (b *Bot) renderAndSend(identity ChatIdentity, state *ChatState, ev router.Event) error {
	result := b.renderer.RenderEvent(ev)
	if result == nil {
		return nil
	}
	if err := b.sendTelegramMessage(identity, state, result); err != nil {
		return fmt.Errorf("send message: %w", err)
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

// getOrCreateBridge returns a started bridge bound to the specific chat identity.
func (b *Bot) getOrCreateBridge(identity ChatIdentity) (*Bridge, error) {
	identityKey := identity.ToClientIdentity()

	b.bridgesMu.RLock()
	bridge := b.bridges[identityKey]
	b.bridgesMu.RUnlock()
	if bridge != nil {
		return bridge, nil
	}

	b.bridgesMu.Lock()
	defer b.bridgesMu.Unlock()

	// Double-check under write lock.
	if existing := b.bridges[identityKey]; existing != nil {
		return existing, nil
	}

	chatBridge := NewBridge(
		b.config.GatewayURL,
		identityKey,
		func(ev router.Event) {
			b.handleGatewayEvent(identity, ev)
		},
	)
	if err := chatBridge.Start(b.ctx); err != nil {
		return nil, fmt.Errorf("start bridge for %s: %w", identityKey, err)
	}

	b.bridges[identityKey] = chatBridge
	return chatBridge, nil
}

// stopAllBridges stops and clears all per-chat bridges.
func (b *Bot) stopAllBridges() {
	b.bridgesMu.Lock()
	bridges := make([]*Bridge, 0, len(b.bridges))
	for key, bridge := range b.bridges {
		bridges = append(bridges, bridge)
		delete(b.bridges, key)
	}
	b.bridgesMu.Unlock()

	for _, bridge := range bridges {
		bridge.Stop()
	}
}

// hydrateStateFromBridge seeds chat state from bridge handshake data.
func (b *Bot) hydrateStateFromBridge(state *ChatState, bridge *Bridge) {
	sessionKey, provider, model, ok := bridge.SessionInfo()
	if !ok {
		return
	}
	if state.SessionKey == "" {
		state.SessionKey = sessionKey
	}
	if state.ProviderName == "" {
		state.ProviderName = provider
	}
	if state.ModelName == "" {
		state.ModelName = model
	}
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
			logger.Debug("telegram bot: editing message in thread",
				logger.BuildMetadata("chat_id", identity.ChatID, "thread_id", *identity.ThreadID, "message_id", state.GetStreamingMessageID()))
		}

		_, err := b.client.EditMessageText(b.ctx, req)
		if err != nil {
			// If editing fails (e.g., message too old), fall back to sending new message
			logger.Warning("telegram bot: edit failed, sending new message",
				logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
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

	logger.Debug("telegram bot: message sent",
		logger.BuildMetadata("chat_id", identity.ChatID, "message_id", msg.MessageID))

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

	b.bridgesMu.RLock()
	for _, bridge := range b.bridges {
		if bridge.IsConnected() {
			stats.Connected = true
			break
		}
	}
	b.bridgesMu.RUnlock()

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
