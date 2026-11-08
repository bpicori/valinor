package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// maxToolResultChars bounds one tool result in the model context, the
	// client event payload, and the persisted transcript.
	maxToolResultChars  = 24_000
	toolResultHeadChars = 16_000
	toolResultTailChars = 8_000
)

// CapResult bounds a tool result so a single command cannot flood the context,
// the client events, or the persisted transcript. Oversized output keeps the
// head and the tail and tells the model how to read the rest.
func CapResult(name, content string) string {
	if len(content) <= maxToolResultChars {
		return content
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = "tool"
	}

	total := len(content)
	head := safePrefix(content, toolResultHeadChars)
	if idx := strings.LastIndex(head, "\n"); idx > len(head)/2 {
		head = head[:idx]
	}
	tail := safeSuffix(content, toolResultTailChars)
	if idx := strings.Index(tail, "\n"); idx >= 0 && idx < len(tail)-1 {
		tail = tail[idx+1:]
	}

	var b strings.Builder
	b.Grow(len(head) + len(tail) + 256)
	b.WriteString(head)
	fmt.Fprintf(&b,
		"\n\n[... %s output truncated: %d of %d characters shown. Narrow the command, or write the output to a workspace file and read it with offset/limit.]\n\n",
		label, len(head)+len(tail), total)
	b.WriteString(tail)
	return b.String()
}

// safePrefix returns s[:n] without splitting a UTF-8 rune.
func safePrefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// safeSuffix returns s[len(s)-n:] without splitting a UTF-8 rune.
func safeSuffix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
