// Package telegram provides per-chat session state management.
package telegram

import (
	"sync"
	"time"

	"valinor/internal/router"
)

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

	// Content accumulation during streaming (for potential recovery)
	StreamingContent string

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
	cs.StreamingContent = ""
	cs.ActiveToolCalls = nil
	cs.NextTypingActionAt = time.Time{}
}

// FinalizeStreaming marks streaming as complete and clears transient state.
func (cs *ChatState) FinalizeStreaming() {
	cs.IsStreaming = false
	cs.CurrentRequestID = ""
	cs.StreamingContent = ""
	cs.ActiveToolCalls = nil
	cs.StreamingMessageID = 0
	cs.NextTypingActionAt = time.Time{}
}

// AppendContent adds delta content during streaming.
func (cs *ChatState) AppendContent(delta string) {
	cs.StreamingContent += delta
}

// SetStreamingContent replaces the accumulated streaming content.
func (cs *ChatState) SetStreamingContent(content string) {
	cs.StreamingContent = content
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

// ClearStreamingMessageID clears the streaming message ID.
func (cs *ChatState) ClearStreamingMessageID() {
	cs.StreamingMessageID = 0
}

// UpdateLastMessageID updates the last message ID.
func (cs *ChatState) UpdateLastMessageID(id int64) {
	cs.LastMessageID = id
}

// SetToolCalls sets the active tool calls.
func (cs *ChatState) SetToolCalls(calls []ToolCallInfo) {
	cs.ActiveToolCalls = calls
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

// Get returns the chat state if it exists, nil otherwise.
func (store *ChatStateStore) Get(identity ChatIdentity) *ChatState {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.states[identity.SequentialKey()]
}

// Remove deletes a chat state from the store.
func (store *ChatStateStore) Remove(identity ChatIdentity) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.states, identity.SequentialKey())
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
