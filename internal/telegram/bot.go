// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/logger"
)

// Bot is the main Telegram bot orchestrator.
// It coordinates the poller, bridge, and renderer through a single event loop.
type Bot struct {
	// Configuration
	config Config

	// Components
	client   *Client
	store    *offsetStore
	poller   *Poller
	bridges  map[string]*Bridge
	renderer *Renderer

	// Shared event channel - all bridges push events here
	events chan chatEvent

	// Per-chat message trackers (single goroutine access, no mutex needed)
	trackers map[int64]*messageTracker

	bridgesMu sync.RWMutex

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
		config:   cfg,
		bridges:  make(map[string]*Bridge),
		events:   make(chan chatEvent, 100),
		trackers: make(map[int64]*messageTracker),
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

	// Initialize stateless renderer
	b.renderer = NewRenderer()

	// Initialize poller with persisted offset
	b.poller = NewPoller(b.client, DefaultPollLimit, b.store.Get())
	if b.config.PollTimeout > 0 {
		b.poller.SetPollTimeout(b.config.PollTimeout)
	}

	// Start poller and bot main loop
	b.poller.Start(b.ctx)
	b.wg.Add(1)
	go b.run()

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

	// Close events channel to signal run() to exit
	close(b.events)

	b.wg.Wait()
	b.ready.Store(false)

	logger.Info("telegram bot: stopped", nil)
}

// run is the single main event loop that processes both Telegram updates and gateway events.
// This ensures all per-chat state access is naturally serialized.
func (b *Bot) run() {
	defer b.wg.Done()

	for {
		select {
		case <-b.ctx.Done():
			return
		case update, ok := <-b.poller.Updates:
			if !ok {
				return
			}
			b.persistOffset(update)
			b.onUpdate(update)
		case ev, ok := <-b.events:
			if !ok {
				return
			}
			b.onEvent(ev)
		}
	}
}

// persistOffset saves the update ID to avoid reprocessing on restart.
func (b *Bot) persistOffset(update Update) {
	if err := b.store.Set(update.UpdateID); err != nil {
		logger.Warning("telegram bot: failed to persist offset",
			logger.BuildMetadata("error", err, "update_id", update.UpdateID))
	}
}

// getOrCreateTracker returns the message tracker for a chat, creating it if needed.
func (b *Bot) getOrCreateTracker(chatID int64) *messageTracker {
	if t, ok := b.trackers[chatID]; ok {
		return t
	}
	t := &messageTracker{}
	b.trackers[chatID] = t
	return t
}

// onUpdate handles a Telegram update from the poller.
func (b *Bot) onUpdate(update Update) {
	cmd, err := parseUpdate(update)
	if err != nil {
		logger.Error("telegram bot: failed to parse update",
			logger.BuildMetadata("error", err, "update_id", update.UpdateID))
		return
	}
	if cmd == nil {
		return
	}

	tracker := b.getOrCreateTracker(cmd.Identity.ChatID)

	if err := b.processCommand(cmd, tracker); err != nil {
		logger.Error("telegram bot: failed to process command",
			logger.BuildMetadata("error", err, "chat_id", cmd.Identity.ChatID))
	}
}

// onEvent handles a gateway event from a bridge.
func (b *Bot) onEvent(ev chatEvent) {
	if ev.identity.ChatID == 0 {
		logger.Warning("telegram bot: received event for unknown chat",
			logger.BuildMetadata("request_id", ev.event.RequestID, "event_type", ev.event.Type))
		return
	}

	tracker := b.getOrCreateTracker(ev.identity.ChatID)

	if err := b.processEvent(ev, tracker); err != nil {
		logger.Error("telegram bot: failed to process event",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
}

// processCommand handles a user command.
func (b *Bot) processCommand(cmd *Command, tracker *messageTracker) error {
	bridge, err := b.getOrCreateBridge(cmd.Identity)
	if err != nil {
		return fmt.Errorf("get bridge for chat: %w", err)
	}

	// Handle control commands directly
	if cmd.IsControlCommand {
		if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
			return fmt.Errorf("failed to send control command")
		}
		return nil
	}

	// Handle status command locally
	if cmd.Type == "status" {
		sessionKey, provider, model, contextTokens, tokensEstimated, _ := bridge.SessionInfo()
		result := b.renderer.RenderStatus(sessionKey, provider, model, contextTokens, tokensEstimated)
		return b.sendTelegramMessage(cmd.Identity, tracker, result)
	}

	// Check if chat is busy
	if tracker.isBusy() {
		notice := &RenderResult{
			Text:      "\u23f3 Please wait for the current response to complete\\.",
			ParseMode: "MarkdownV2",
		}
		return b.sendTelegramMessage(cmd.Identity, tracker, notice)
	}

	// Finalize any existing streaming before starting new conversation
	if cmd.Type == gateway.CommandNewConversation {
		tracker.markIdle()
	}

	// Mark as busy before sending
	tracker.markBusy(cmd.RequestID)

	if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
		tracker.markIdle()
		return fmt.Errorf("failed to send command")
	}

	return nil
}

// processEvent handles a gateway event.
func (b *Bot) processEvent(ev chatEvent, tracker *messageTracker) error {
	switch ev.event.Type {
	case event.AgentContentDelta:
		return b.handleContentDelta(ev, tracker)
	case event.AgentContentFull:
		return b.handleContentFull(ev, tracker)
	case event.AgentImage:
		return b.handleImage(ev, tracker)
	case event.AgentDone:
		return b.handleAgentDone(ev, tracker)
	case event.AgentError:
		return b.handleAgentError(ev, tracker)
	case event.AgentCancelled:
		return b.handleAgentCancelled(ev, tracker)
	case event.SessionAttached:
		return b.handleSessionAttached(ev, tracker)
	case event.NewConversation:
		return b.handleNewConversation(ev, tracker)
	case event.ChangeModelEvent:
		return b.handleModelChanged(ev, tracker)
	case event.CronJobResult:
		return b.handleCronJobResult(ev, tracker)
	case event.AgentThinking:
		return b.handleThinking(ev, tracker)
	default:
		// Other events (tool calls, tokens) - render if needed
		result := b.renderer.RenderEvent(ev.event)
		if result != nil {
			return b.sendTelegramMessage(ev.identity, tracker, result)
		}
		return nil
	}
}

// handleThinking appends tool activity to the in-place status message.
func (b *Bot) handleThinking(ev chatEvent, tracker *messageTracker) error {
	payload, ok := ev.event.Payload.(gateway.ThinkingPayload)
	if !ok {
		return nil
	}

	line := b.renderer.renderThinkingLine(payload)
	if line == "" {
		return nil
	}

	tracker.appendThinkingLine(line)
	result := b.renderer.renderThinkingStatus(tracker.getThinkingLines())
	if result == nil {
		return nil
	}
	return b.sendTelegramMessage(ev.identity, tracker, result)
}

// handleContentDelta accumulates streamed content.
func (b *Bot) handleContentDelta(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ContentDeltaPayload); ok {
		tracker.appendContent(payload.Content)
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleContentFull replaces accumulated content.
func (b *Bot) handleContentFull(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ContentFullPayload); ok {
		tracker.contentBuf.Reset()
		tracker.appendContent(payload.Content)
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleImage sends a generated image payload immediately.
func (b *Bot) handleImage(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ImagePayload); ok {
		if err := b.sendTelegramMessage(ev.identity, tracker, &RenderResult{
			Images:  []gateway.ImagePayload{payload},
			IsFinal: true,
		}); err != nil {
			return fmt.Errorf("send image message: %w", err)
		}
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleAgentDone processes the final agent completion.
func (b *Bot) handleAgentDone(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderFinalContent(tracker.getContent())
	tracker.markIdle()

	if result != nil {
		if err := b.sendTelegramMessage(ev.identity, tracker, result); err != nil {
			return fmt.Errorf("send final message: %w", err)
		}
	}
	return nil
}

// handleAgentError processes agent errors.
func (b *Bot) handleAgentError(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleAgentCancelled processes agent cancellation.
func (b *Bot) handleAgentCancelled(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleSessionAttached renders session attachment notification.
func (b *Bot) handleSessionAttached(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleNewConversation renders new conversation confirmation.
func (b *Bot) handleNewConversation(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleModelChanged renders model change notification.
func (b *Bot) handleModelChanged(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleCronJobResult sends scheduled job results to the chat.
func (b *Bot) handleCronJobResult(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
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

	if existing := b.bridges[identityKey]; existing != nil {
		return existing, nil
	}

	return b.createBridgeLocked(identity, identityKey)
}

// createBridgeLocked creates a new bridge for the given identity.
func (b *Bot) createBridgeLocked(identity ChatIdentity, identityKey string) (*Bridge, error) {
	chatBridge := NewBridge(
		b.config.GatewayURL,
		identity,
		b.events,
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

// GetStats returns bot statistics for monitoring.
func (b *Bot) GetStats() BotStats {
	stats := BotStats{
		Ready: b.ready.Load(),
	}

	stats.ActiveChats = len(b.trackers)

	b.bridgesMu.RLock()
	for _, bridge := range b.bridges {
		if bridge.IsConnected() {
			stats.Connected = true
			break
		}
	}
	b.bridgesMu.RUnlock()

	stats.Polling = b.poller != nil && b.ready.Load()

	return stats
}

// BotStats contains bot statistics.
type BotStats struct {
	Ready       bool
	Connected   bool
	Polling     bool
	ActiveChats int
}

// ChatIdentity uniquely identifies a Telegram DM chat for session binding.
type ChatIdentity struct {
	ChatID   int64  `json:"chat_id"`            // Telegram chat ID
	Username string `json:"username,omitempty"` // Optional username for display
}

// ToClientIdentity converts ChatIdentity to gateway.ClientIdentity.
// The identity string is formatted as "{chatID}".
func (ci ChatIdentity) ToClientIdentity() string {
	return strconv.FormatInt(ci.ChatID, 10)
}

// ParseChatIdentity parses a client identity string into ChatIdentity.
// Expected format: "{chatID}".
func ParseChatIdentity(identity string) (ChatIdentity, error) {
	chatID, err := strconv.ParseInt(identity, 10, 64)
	if err != nil {
		return ChatIdentity{}, fmt.Errorf("invalid chat identity format: %s", identity)
	}
	return ChatIdentity{ChatID: chatID}, nil
}

// FromTelegramUpdate extracts ChatIdentity from a Telegram update.
func FromTelegramUpdate(chat *Chat) ChatIdentity {
	if chat == nil {
		return ChatIdentity{}
	}
	return ChatIdentity{
		ChatID:   chat.ID,
		Username: chat.Username,
	}
}
