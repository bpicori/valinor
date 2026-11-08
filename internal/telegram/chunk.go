package telegram

import "strings"

// splitTelegramText splits text into chunks no longer than limit runes.
// It prefers paragraph, line, sentence, and word boundaries and keeps fenced
// code blocks balanced by closing and reopening a fence when a hard split is
// unavoidable.
func splitTelegramText(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}

	var chunks []string
	for len(runes) > limit {
		cut, inFence := telegramSplitPoint(runes, limit)
		if cut <= 0 {
			cut = limit
			inFence = fenceOpenAt(runes, cut)
		}
		// Closing a fence adds four runes; keep the chunk under the cap.
		if inFence && cut+4 > limit && limit > 4 {
			cut = limit - 4
			inFence = fenceOpenAt(runes, cut)
		}

		chunk := string(runes[:cut])
		rest := runes[cut:]
		if inFence && limit <= 8 {
			// A balanced fence cannot fit in tiny chunks; fall back to a hard split.
			inFence = false
		}
		if inFence {
			if !strings.HasSuffix(chunk, "\n") {
				chunk += "\n"
			}
			chunk += "```"
			rest = append([]rune("```\n"), rest...)
		}

		chunks = append(chunks, chunk)
		runes = rest
	}
	if len(runes) > 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

// telegramSplitPoint returns the best cut position at or before limit and
// whether that position is inside a fenced code block.
func telegramSplitPoint(runes []rune, limit int) (int, bool) {
	inFence := false
	atLineStart := true
	lastParagraph := -1
	lastNewline := -1
	lastSentence := -1
	lastSpace := -1

	max := limit
	if max > len(runes) {
		max = len(runes)
	}

	for i := 0; i < max; i++ {
		if atLineStart {
			if hasFencePrefix(runes[i:]) {
				inFence = !inFence
			}
			atLineStart = false
		}

		r := runes[i]
		switch {
		case r == '\n':
			if !inFence {
				if i+1 < len(runes) && runes[i+1] == '\n' {
					lastParagraph = i + 2
				}
				lastNewline = i + 1
			}
			atLineStart = true
		case (r == '.' || r == '!' || r == '?') && !inFence:
			lastSentence = i + 1
		case r == ' ' && !inFence:
			lastSpace = i + 1
		}
	}

	for _, candidate := range []int{lastParagraph, lastNewline, lastSentence, lastSpace} {
		if candidate > 0 && candidate <= limit {
			return candidate, fenceOpenAt(runes, candidate)
		}
	}
	return max, fenceOpenAt(runes, max)
}

// fenceOpenAt reports whether position cut is inside a fenced code block.
func fenceOpenAt(runes []rune, cut int) bool {
	inFence := false
	atLineStart := true
	for i := 0; i < cut && i < len(runes); i++ {
		if atLineStart {
			if hasFencePrefix(runes[i:]) {
				inFence = !inFence
			}
			atLineStart = false
		}
		if runes[i] == '\n' {
			atLineStart = true
		}
	}
	return inFence
}

// hasFencePrefix reports whether the line starts with a backtick or tilde fence.
func hasFencePrefix(runes []rune) bool {
	i := 0
	for i < len(runes) && i < 3 && runes[i] == ' ' {
		i++
	}
	if i+2 >= len(runes) {
		return false
	}
	marker := runes[i]
	if marker != '`' && marker != '~' {
		return false
	}
	return runes[i+1] == marker && runes[i+2] == marker
}
