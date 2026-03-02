// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"valinor/internal/gateway"
	"valinor/internal/logger"
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
	chatStore *KnownChatStore
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

	// Initialize known chats store
	chatStore, err := newKnownChatStore(cfg.StorageDir)
	if err != nil {
		return nil, fmt.Errorf("initialize known chats store: %w", err)
	}

	return &Bot{
		config:       cfg,
		stateStore:   NewChatStateStore(),
		requestChats: make(map[string]ChatIdentity),
		bridges:      make(map[string]*Bridge),
		chatStore:    chatStore,
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

	// Pre-create bridges for all known chats so they can receive cron results
	b.preCreateBridgesForKnownChats()

	// Initialize poller
	b.poller = NewPoller(b.client, b.store, b.onTelegramUpdate)
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

// onTelegramUpdate is called by the poller for each received Telegram update.
func (b *Bot) onTelegramUpdate(ctx context.Context, update Update) error {
	// Dispatch the update to get a command
	cmd, err := b.dispatch(update)
	if err != nil {
		return fmt.Errorf("dispatch update %d: %w", update.UpdateID, err)
	}
	if cmd == nil {
		// Update was filtered out (no message, etc.)
		return nil
	}

	// Record this chat as known (persist for future bridge pre-creation)
	if err := b.chatStore.Add(cmd.Identity); err != nil {
		logger.Warning("telegram bot: failed to record chat",
			logger.BuildMetadata("error", err, "chat_id", cmd.Identity.ChatID))
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
func (b *Bot) handleGatewayEvent(sourceIdentity ChatIdentity, ev gateway.Event) {
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
	if cmd.Type == gateway.CommandNewConversation {
		// Reset streaming state (like TUI's handleNewConversation)
		state.FinalizeStreaming()
	}

	// Send command to gateway
	if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
		return fmt.Errorf("failed to send command")
	}

	// Mark as streaming for user messages
	if cmd.Type == gateway.CommandUserMessage {
		state.StartStreaming(cmd.RequestID)
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

	return b.createBridgeLocked(identity, identityKey)
}

// createBridgeLocked creates a new bridge for the given identity.
// Must be called with b.bridgesMu held for writing.
func (b *Bot) createBridgeLocked(identity ChatIdentity, identityKey string) (*Bridge, error) {
	chatBridge := NewBridge(
		b.config.GatewayURL,
		identityKey,
		func(ev gateway.Event) {
			b.handleGatewayEvent(identity, ev)
		},
	)
	if err := chatBridge.Start(b.ctx); err != nil {
		return nil, fmt.Errorf("start bridge for %s: %w", identityKey, err)
	}

	b.bridges[identityKey] = chatBridge
	return chatBridge, nil
}

// preCreateBridgesForKnownChats creates bridges for all known chats on startup.
// This ensures cron results are delivered to all chats that have ever messaged the bot,
// not just those that are currently sending messages.
func (b *Bot) preCreateBridgesForKnownChats() {
	knownChats := b.chatStore.List()
	if len(knownChats) == 0 {
		return
	}

	logger.Info("telegram bot: pre-creating bridges for known chats",
		logger.BuildMetadata("count", len(knownChats)))

	created := 0
	failed := 0

	for _, chat := range knownChats {
		identity := chat.ToChatIdentity()
		identityKey := identity.ToClientIdentity()

		// Check if bridge already exists
		b.bridgesMu.RLock()
		exists := b.bridges[identityKey] != nil
		b.bridgesMu.RUnlock()

		if exists {
			continue
		}

		b.bridgesMu.Lock()
		_, err := b.createBridgeLocked(identity, identityKey)
		b.bridgesMu.Unlock()

		if err != nil {
			logger.Warning("telegram bot: failed to pre-create bridge for known chat",
				logger.BuildMetadata("error", err, "chat_id", identity.ChatID, "thread_id", identity.ThreadID))
			failed++
		} else {
			created++
		}
	}

	logger.Info("telegram bot: pre-created bridges",
		logger.BuildMetadata("created", created, "failed", failed, "total_known", len(knownChats)))
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
