// Package telegram provides minimal per-chat message tracking.
package telegram

import (
	"strings"
	"time"
)

// maxBusyDuration is the maximum time a request can be busy before being
// considered stale. This prevents chats from being stuck forever if events
// are lost due to crashes or network issues.
const maxBusyDuration = 1 * time.Minute

// messageTracker tracks the state for a single chat's current streaming message.
// This is intentionally minimal - session info lives in the Bridge, not here.
type messageTracker struct {
	streamingMsgID int64
	lastStreamText string
	thinkingLines  []string
	busy           bool
	busySince      time.Time // when busy was set, for staleness detection
	requestID      string
	contentBuf     strings.Builder
	nextTypingAt   time.Time
}

// isBusy returns true if the chat is currently processing a request.
// It also checks for stale busy state (e.g., due to lost events) and
// auto-resets if the busy duration exceeds maxBusyDuration.
func (t *messageTracker) isBusy() bool {
	if !t.busy {
		return false
	}

	// Check for stale busy state - auto-reset if busy for too long
	if !t.busySince.IsZero() && time.Since(t.busySince) > maxBusyDuration {
		t.markIdle()
		return false
	}

	return true
}

// markBusy sets the tracker as busy with the given request ID.
func (t *messageTracker) markBusy(requestID string) {
	t.busy = true
	t.busySince = time.Now()
	t.requestID = requestID
	t.contentBuf.Reset()
	t.streamingMsgID = 0
	t.lastStreamText = ""
	t.thinkingLines = nil
	t.nextTypingAt = time.Time{}
}

// markIdle clears the busy state and resets transient fields.
func (t *messageTracker) markIdle() {
	t.busy = false
	t.busySince = time.Time{}
	t.requestID = ""
	t.contentBuf.Reset()
	t.streamingMsgID = 0
	t.lastStreamText = ""
	t.thinkingLines = nil
}

// appendThinkingLine adds a tool/thinking line to the in-progress status message.
func (t *messageTracker) appendThinkingLine(line string) {
	if line == "" {
		return
	}
	t.thinkingLines = append(t.thinkingLines, line)
}

// getThinkingLines returns accumulated tool/thinking lines for the current request.
func (t *messageTracker) getThinkingLines() []string {
	return t.thinkingLines
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

// setLastStreamText records the text last sent or edited for the streaming message.
func (t *messageTracker) setLastStreamText(text string) {
	t.lastStreamText = text
}

// isSameStreamText reports whether the streaming message already shows this text.
func (t *messageTracker) isSameStreamText(text string) bool {
	return t.lastStreamText == text
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
