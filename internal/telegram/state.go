// Package telegram provides minimal per-chat message tracking.
package telegram

import (
	"strings"
	"time"
)

// messageTracker tracks the state for a single chat's current streaming message.
// This is intentionally minimal - session info lives in the Bridge, not here.
type messageTracker struct {
	streamingMsgID       int64
	lastStreamText       string
	thinkingLines        []string
	busy                 bool
	requestID            string
	contentBuf           strings.Builder
	nextTypingAt         time.Time
	nextStatusEditAt     time.Time
	pendingAttachments   []pendingAttachment
	pendingAttachmentsAt time.Time
}

const (
	thinkingEditInterval = 2 * time.Second
	// pendingAttachmentTTL bounds how long files sent without an instruction
	// stay attached to the chat's next message.
	pendingAttachmentTTL = 15 * time.Minute
)

// isBusy returns true while a run is active for this chat.
func (t *messageTracker) isBusy() bool {
	return t.busy
}

// matchesRequest reports whether an event belongs to the active run.
// Empty request IDs are accepted because not every event carries one.
func (t *messageTracker) matchesRequest(requestID string) bool {
	if requestID == "" {
		return true
	}
	return t.requestID == requestID
}

// markBusy sets the tracker as busy with the given request ID.
func (t *messageTracker) markBusy(requestID string) {
	t.busy = true
	t.requestID = requestID
	t.contentBuf.Reset()
	t.streamingMsgID = 0
	t.lastStreamText = ""
	t.thinkingLines = nil
	t.nextTypingAt = time.Time{}
	t.nextStatusEditAt = time.Time{}
}

// markIdle clears the busy state and resets transient fields.
func (t *messageTracker) markIdle() {
	t.busy = false
	t.requestID = ""
	t.contentBuf.Reset()
	t.streamingMsgID = 0
	t.lastStreamText = ""
	t.thinkingLines = nil
	t.nextStatusEditAt = time.Time{}
}

// addPending queues files sent without an instruction for the next message.
// An expired batch is dropped first so unrelated files never carry over.
func (t *messageTracker) addPending(attachments []pendingAttachment, now time.Time) {
	if len(attachments) == 0 {
		return
	}
	if now.Sub(t.pendingAttachmentsAt) > pendingAttachmentTTL {
		t.pendingAttachments = nil
	}
	t.pendingAttachments = append(t.pendingAttachments, attachments...)
	t.pendingAttachmentsAt = now
}

// takePending returns queued files and clears the batch.
func (t *messageTracker) takePending(now time.Time) []pendingAttachment {
	if len(t.pendingAttachments) == 0 {
		return nil
	}
	expired := now.Sub(t.pendingAttachmentsAt) > pendingAttachmentTTL
	pending := t.pendingAttachments
	t.pendingAttachments = nil
	t.pendingAttachmentsAt = time.Time{}
	if expired {
		return nil
	}
	return pending
}

// clearPending drops queued files without using them.
func (t *messageTracker) clearPending() {
	t.pendingAttachments = nil
	t.pendingAttachmentsAt = time.Time{}
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

// canEditStatus returns true if the thinking status may be edited now.
func (t *messageTracker) canEditStatus(now time.Time) bool {
	return !now.Before(t.nextStatusEditAt)
}

// markStatusEdited sets the next allowed thinking status edit time.
func (t *messageTracker) markStatusEdited(now time.Time, interval time.Duration) {
	t.nextStatusEditAt = now.Add(interval)
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
