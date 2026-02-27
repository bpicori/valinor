// Package telegram provides per-chat state, identity, and sequential processing.
package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/router"
)

// Sequential key patterns for deterministic session resume and ordered processing.
// These patterns mirror the OpenClaw sequentialization design:
//   - DM: telegram:{chatId}
//   - Group/Forum topic: telegram:{chatId}:topic:{threadId}
//   - Control lane for cancel/stop commands: telegram:{chatId}:control
const (
	// KeyPrefix is the prefix for all sequential keys (DMs, groups, channels, forum topics).
	KeyPrefix = "telegram"
	// KeySegmentTopic is the segment identifier for topic/thread IDs.
	KeySegmentTopic = "topic"
	// KeySegmentControl is the segment identifier for control lane keys.
	KeySegmentControl = "control"
)

// IdentityType identifies the type of Telegram chat for routing purposes.
type IdentityType string

const (
	// IdentityTypeDM is a direct message chat.
	IdentityTypeDM IdentityType = "dm"
	// IdentityTypeGroup is a group chat.
	IdentityTypeGroup IdentityType = "group"
	// IdentityTypeChannel is a channel.
	IdentityTypeChannel IdentityType = "channel"
	// IdentityTypeForumTopic is a forum topic within a supergroup.
	IdentityTypeForumTopic IdentityType = "forum_topic"
)

// ChatIdentity uniquely identifies a Telegram chat or forum topic for session binding.
type ChatIdentity struct {
	ChatID   int64        `json:"chat_id"`            // Telegram chat ID
	ThreadID *int64       `json:"thread_id"`          // Optional forum topic thread ID
	Type     IdentityType `json:"type"`               // Type of chat
	Username string       `json:"username,omitempty"` // Optional username for display
	Title    string       `json:"title,omitempty"`    // Optional chat title for display
}

// IsDirectMessage returns true if this is a DM (not a group/channel/topic).
func (ci ChatIdentity) IsDirectMessage() bool {
	return ci.Type == IdentityTypeDM && ci.ThreadID == nil
}

// IsForumTopic returns true if this identity represents a forum topic.
func (ci ChatIdentity) IsForumTopic() bool {
	return ci.ThreadID != nil
}

// ToClientIdentity converts ChatIdentity to router.ClientIdentity.
// The identity string is formatted as "{chatID}" or "{chatID}:topic:{threadID}".
func (ci ChatIdentity) ToClientIdentity() string {
	if ci.ThreadID != nil {
		return fmt.Sprintf("%d:%s:%d", ci.ChatID, KeySegmentTopic, *ci.ThreadID)
	}
	return strconv.FormatInt(ci.ChatID, 10)
}

// SequentialKey returns the key for per-chat sequential processing.
// This is used as the worker queue key to ensure ordered message handling.
func (ci ChatIdentity) SequentialKey() string {
	if ci.ThreadID != nil {
		return fmt.Sprintf("%s:%d:%s:%d", KeyPrefix, ci.ChatID, KeySegmentTopic, *ci.ThreadID)
	}
	return fmt.Sprintf("%s:%d", KeyPrefix, ci.ChatID)
}

// ControlLaneKey returns the control lane key for cancel/stop commands.
// Control commands use a separate lane to avoid blocking behind regular messages.
func (ci ChatIdentity) ControlLaneKey() string {
	return fmt.Sprintf("%s:%d:%s", KeyPrefix, ci.ChatID, KeySegmentControl)
}

// ParseChatIdentity parses a client identity string into ChatIdentity.
// Expected formats: "{chatID}" or "{chatID}:topic:{threadID}".
func ParseChatIdentity(identity string) (ChatIdentity, error) {
	// Try to parse as simple chat ID
	chatID, err := strconv.ParseInt(identity, 10, 64)
	if err == nil {
		return ChatIdentity{
			ChatID: chatID,
			Type:   IdentityTypeDM,
		}, nil
	}

	// Try to parse as "chatID:topic:threadID" format
	parts := strings.Split(identity, ":")
	if len(parts) == 3 && parts[1] == KeySegmentTopic {
		chatIDInt, err1 := strconv.ParseInt(parts[0], 10, 64)
		threadIDInt, err2 := strconv.ParseInt(parts[2], 10, 64)
		if err1 == nil && err2 == nil {
			return ChatIdentity{
				ChatID:   chatIDInt,
				ThreadID: &threadIDInt,
				Type:     IdentityTypeForumTopic,
			}, nil
		}
	}

	return ChatIdentity{}, fmt.Errorf("invalid chat identity format: %s", identity)
}

// FromTelegramUpdate extracts ChatIdentity from a Telegram update.
// The chat type is inferred from the Telegram chat object.
func FromTelegramUpdate(chat *Chat) ChatIdentity {
	if chat == nil {
		return ChatIdentity{}
	}

	ci := ChatIdentity{
		ChatID:   chat.ID,
		Username: chat.Username,
		Title:    chat.Title,
	}

	// Determine chat type from Telegram type
	switch chat.Type {
	case "private":
		ci.Type = IdentityTypeDM
	case "group":
		ci.Type = IdentityTypeGroup
	case "supergroup":
		ci.Type = IdentityTypeGroup
	case "channel":
		ci.Type = IdentityTypeChannel
	}

	return ci
}

// WithThreadID returns a new ChatIdentity with the specified thread ID set.
// This is used for forum topics where the thread ID comes from message_thread_id.
func (ci ChatIdentity) WithThreadID(threadID int64) ChatIdentity {
	return ChatIdentity{
		ChatID:   ci.ChatID,
		ThreadID: &threadID,
		Type:     IdentityTypeForumTopic,
		Username: ci.Username,
		Title:    ci.Title,
	}
}

// ChatState tracks the state for a single chat conversation.
// This mirrors TUI's Model but is scoped to a single chat session.
type ChatState struct {
	Identity ChatIdentity

	// Message tracking (similar to TUI's lastMessage pattern)
	LastMessageID      int64 // Most recent message sent (any type)
	StreamingMessageID int64 // Message ID currently being streamed/edited

	// Streaming state
	IsStreaming bool

	// Request tracking for response routing
	CurrentRequestID string

	// Content accumulation during streaming using strings.Builder for O(n) efficiency.
	streamingBuf strings.Builder

	// Typing action pacing to avoid Telegram 429 rate limits.
	NextTypingActionAt time.Time

	// Tool calls tracking (similar to TUI's chatMessage.ToolCalls)
	ActiveToolCalls []ToolCallInfo

	// Session info (mirrors TUI's sessionKey, providerName, modelName)
	SessionKey   string
	ProviderName string
	ModelName    string

	// Final state
	Usage PromptTokenUsage
}

// ToolCallInfo tracks an active tool call during streaming.
type ToolCallInfo struct {
	ID     string
	Name   string
	Args   string
	Result string
	Done   bool
}

// PromptTokenUsage tracks token usage for display.
type PromptTokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// CanStartNewRequest returns true if the chat is ready for a new request.
// This provides TUI-like state guarding - prevents starting a new request while streaming.
func (cs *ChatState) CanStartNewRequest() bool {
	return !cs.IsStreaming
}

// StartStreaming marks the chat as streaming with the given request ID.
func (cs *ChatState) StartStreaming(requestID string) {
	cs.IsStreaming = true
	cs.CurrentRequestID = requestID
	cs.streamingBuf.Reset()
	cs.ActiveToolCalls = nil
	cs.NextTypingActionAt = time.Time{}
}

// FinalizeStreaming marks streaming as complete and clears transient state.
func (cs *ChatState) FinalizeStreaming() {
	cs.IsStreaming = false
	cs.CurrentRequestID = ""
	cs.streamingBuf.Reset()
	cs.ActiveToolCalls = nil
	cs.StreamingMessageID = 0
	cs.NextTypingActionAt = time.Time{}
}

// AppendContent adds delta content during streaming.
func (cs *ChatState) AppendContent(delta string) {
	cs.streamingBuf.WriteString(delta)
}

// GetStreamingContent returns the accumulated streaming content.
func (cs *ChatState) GetStreamingContent() string {
	return cs.streamingBuf.String()
}

// SetStreamingContent replaces the accumulated streaming content.
func (cs *ChatState) SetStreamingContent(content string) {
	cs.streamingBuf.Reset()
	cs.streamingBuf.WriteString(content)
}

// CanSendTypingAction returns true if typing can be sent now.
func (cs *ChatState) CanSendTypingAction(now time.Time) bool {
	return !now.Before(cs.NextTypingActionAt)
}

// MarkTypingActionSent sets the next allowed typing action time.
func (cs *ChatState) MarkTypingActionSent(now time.Time, interval time.Duration) {
	cs.NextTypingActionAt = now.Add(interval)
}

// BackoffTypingAction delays typing actions after rate limit responses.
func (cs *ChatState) BackoffTypingAction(now time.Time, retryAfter time.Duration) {
	cs.NextTypingActionAt = now.Add(retryAfter)
}

// SetStreamingMessageID sets the message ID for the current streaming session.
func (cs *ChatState) SetStreamingMessageID(id int64) {
	cs.StreamingMessageID = id
}

// GetStreamingMessageID returns the current streaming message ID.
func (cs *ChatState) GetStreamingMessageID() int64 {
	return cs.StreamingMessageID
}

// UpdateLastMessageID updates the last message ID.
func (cs *ChatState) UpdateLastMessageID(id int64) {
	cs.LastMessageID = id
}

// AddToolCall adds a new tool call.
func (cs *ChatState) AddToolCall(call ToolCallInfo) {
	cs.ActiveToolCalls = append(cs.ActiveToolCalls, call)
}

// MarkToolCallDone marks a tool call as complete with its result.
func (cs *ChatState) MarkToolCallDone(toolCallID, result string) {
	for i := range cs.ActiveToolCalls {
		if cs.ActiveToolCalls[i].ID == toolCallID {
			cs.ActiveToolCalls[i].Done = true
			cs.ActiveToolCalls[i].Result = result
			break
		}
	}
}

// UpdateUsage updates token usage statistics.
func (cs *ChatState) UpdateUsage(usage PromptTokenUsage) {
	cs.Usage = usage
}

// UpdateSessionInfo updates the session key, provider, and model.
func (cs *ChatState) UpdateSessionInfo(sessionKey, provider, model string) {
	cs.SessionKey = sessionKey
	cs.ProviderName = provider
	cs.ModelName = model
}

// ChatStateStore manages chat states for all conversations.
type ChatStateStore struct {
	states map[string]*ChatState
	mu     sync.RWMutex
}

// NewChatStateStore creates a new chat state store.
func NewChatStateStore() *ChatStateStore {
	return &ChatStateStore{
		states: make(map[string]*ChatState),
	}
}

// GetOrCreate returns the chat state for the given identity, creating it if needed.
func (store *ChatStateStore) GetOrCreate(identity ChatIdentity) *ChatState {
	key := identity.SequentialKey()

	store.mu.RLock()
	if state, ok := store.states[key]; ok {
		store.mu.RUnlock()
		return state
	}
	store.mu.RUnlock()

	store.mu.Lock()
	defer store.mu.Unlock()

	// Double-check after acquiring write lock
	if state, ok := store.states[key]; ok {
		return state
	}

	state := &ChatState{
		Identity: identity,
	}
	store.states[key] = state
	return state
}

// WorkItem represents a unit of work to be processed sequentially per chat.
// This is similar to how TUI's Update() receives both user input and gateway events.
type WorkItem struct {
	// Type indicates what kind of work this is
	Type WorkItemType

	// For command work items
	Command *Command

	// For event work items (stored by value for safety)
	Event router.Event

	// RequestID for correlation
	RequestID string

	// Identity for event routing (only used for events, populated before submission)
	Identity ChatIdentity
}

// WorkItemType distinguishes between different work types.
type WorkItemType int

const (
	// WorkItemCommand is a user command from Telegram
	WorkItemCommand WorkItemType = iota
	// WorkItemEvent is a gateway event for this chat
	WorkItemEvent
)

// IsControlCommand returns true if this work item is a control command.
func (wi *WorkItem) IsControlCommand() bool {
	if wi.Type != WorkItemCommand || wi.Command == nil {
		return false
	}
	return wi.Command.IsControlCommand
}

// GetSequentialKey returns the appropriate sequential key for this work item.
func (wi *WorkItem) GetSequentialKey() string {
	switch wi.Type {
	case WorkItemCommand:
		if wi.Command != nil {
			return wi.Command.GetSequentialKey()
		}
	case WorkItemEvent:
		// Events are pre-routed with their identity
		// They use the same sequential key as commands for ordering
		return wi.Identity.SequentialKey()
	}
	return ""
}

// WorkItemFunc is the function type for processing a work item.
type WorkItemFunc func(ctx context.Context, item *WorkItem) error

// Sequencer ensures ordered processing of work items within each chat.
// Each chat key (including control lanes) gets its own sequential worker queue.
// This mirrors TUI's single-threaded Update() loop but adapted for multi-chat.
type Sequencer struct {
	workFn WorkItemFunc

	// queues maps sequential keys to their work queues
	queues map[string]chan *WorkItem
	// cancelFuncs maps sequential keys to their context cancel functions
	cancelFuncs map[string]context.CancelFunc
	// wg tracks active workers per key
	wg map[string]*sync.WaitGroup

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSequencer creates a new per-chat sequencer.
func NewSequencer(workFn WorkItemFunc) *Sequencer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Sequencer{
		workFn:      workFn,
		queues:      make(map[string]chan *WorkItem),
		cancelFuncs: make(map[string]context.CancelFunc),
		wg:          make(map[string]*sync.WaitGroup),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Stop gracefully stops all worker queues and waits for completion.
func (s *Sequencer) Stop() {
	s.cancel()

	s.mu.Lock()
	// Cancel all worker contexts
	for _, cancel := range s.cancelFuncs {
		cancel()
	}
	// Collect all wait groups
	groups := make([]*sync.WaitGroup, 0, len(s.wg))
	for _, wg := range s.wg {
		groups = append(groups, wg)
	}
	s.mu.Unlock()

	// Wait for all workers to finish outside the lock
	for _, wg := range groups {
		wg.Wait()
	}

	applog.Info("telegram sequencer: stopped", nil)
}

// Submit queues a work item for sequential processing by its sequential key.
// The work item is processed in order with other items sharing the same key.
// This allows both commands and gateway events to be serialized per chat.
func (s *Sequencer) Submit(item *WorkItem, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if we're shutting down
	select {
	case <-s.ctx.Done():
		return false
	default:
	}

	// Get or create the queue for this key
	queue, ok := s.queues[key]
	if !ok {
		// Create new worker queue for this key
		queue = make(chan *WorkItem, 64)
		s.queues[key] = queue

		// Create per-key context
		keyCtx, keyCancel := context.WithCancel(s.ctx)
		s.cancelFuncs[key] = keyCancel

		// Create wait group for this key
		wg := &sync.WaitGroup{}
		s.wg[key] = wg

		// Start worker goroutine for this key
		wg.Add(1)
		go s.worker(keyCtx, key, queue, wg)
	}

	// Try to queue the work item (non-blocking)
	select {
	case queue <- item:
		return true
	default:
		applog.Warning("telegram sequencer: queue full, dropping work item",
			applog.BuildMetadata("key", key, "request_id", item.RequestID))
		return false
	}
}

// worker processes work items for a single sequential key in order.
// This is the per-chat equivalent of TUI's Update() loop.
func (s *Sequencer) worker(ctx context.Context, key string, queue chan *WorkItem, wg *sync.WaitGroup) {
	defer wg.Done()
	defer s.cleanupWorker(key)

	applog.Debug("telegram sequencer: worker started", applog.BuildMetadata("key", key))

	for {
		select {
		case <-ctx.Done():
			applog.Debug("telegram sequencer: worker exiting", applog.BuildMetadata("key", key))
			return

		case item, ok := <-queue:
			if !ok {
				return
			}

			// Process the work item
			if err := s.workFn(ctx, item); err != nil {
				applog.Error("telegram sequencer: work function error",
					applog.BuildMetadata("key", key, "error", err, "request_id", item.RequestID))
			}
		}
	}
}

// cleanupWorker removes the worker's resources when it exits.
func (s *Sequencer) cleanupWorker(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.queues, key)
	delete(s.cancelFuncs, key)
	delete(s.wg, key)

	applog.Debug("telegram sequencer: worker cleaned up", applog.BuildMetadata("key", key))
}

// ActiveKeys returns the number of active sequential keys (worker queues).
// Useful for metrics and monitoring.
func (s *Sequencer) ActiveKeys() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.queues)
}

// TotalQueued returns the total number of queued work items across all keys.
// Useful for metrics and monitoring.
func (s *Sequencer) TotalQueued() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, queue := range s.queues {
		total += len(queue)
	}
	return total
}
