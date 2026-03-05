// Package telegram provides minimal per-chat message tracking.
package telegram

import (
	"strings"
	"time"
)

// messageTracker tracks the state for a single chat's current streaming message.
// This is intentionally minimal - session info lives in the Bridge, not here.
type messageTracker struct {
	streamingMsgID int64
	busy           bool
	requestID      string
	contentBuf     strings.Builder
	nextTypingAt   time.Time
}

// isBusy returns true if the chat is currently processing a request.
func (t *messageTracker) isBusy() bool {
	return t.busy
}

// markBusy sets the tracker as busy with the given request ID.
func (t *messageTracker) markBusy(requestID string) {
	t.busy = true
	t.requestID = requestID
	t.contentBuf.Reset()
	t.streamingMsgID = 0
	t.nextTypingAt = time.Time{}
}

// markIdle clears the busy state and resets transient fields.
func (t *messageTracker) markIdle() {
	t.busy = false
	t.requestID = ""
	t.contentBuf.Reset()
	t.streamingMsgID = 0
}

// appendContent adds delta content during streaming.
func (t *messageTracker) appendContent(delta string) {
	t.contentBuf.WriteString(delta)
}

// getContent returns the accumulated streaming content.
func (t *messageTracker) getContent() string {
	return t.contentBuf.String()
}

// setStreamingMsgID sets the message ID for the current streaming session.
func (t *messageTracker) setStreamingMsgID(id int64) {
	t.streamingMsgID = id
}

// getStreamingMsgID returns the current streaming message ID.
func (t *messageTracker) getStreamingMsgID() int64 {
	return t.streamingMsgID
}

// canSendTyping returns true if typing can be sent now (rate limiting).
func (t *messageTracker) canSendTyping(now time.Time) bool {
	return !now.Before(t.nextTypingAt)
}

// markTypingSent sets the next allowed typing action time.
func (t *messageTracker) markTypingSent(now time.Time, interval time.Duration) {
	t.nextTypingAt = now.Add(interval)
}

// backoffTyping delays typing actions after rate limit responses.
func (t *messageTracker) backoffTyping(now time.Time, retryAfter time.Duration) {
	t.nextTypingAt = now.Add(retryAfter)
}
