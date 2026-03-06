// Package telegram provides Markdown to Telegram MarkdownV2 conversion.
package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

// Pre-compiled regexps for performance.
var (
	// Placeholder removal
	placeholderRegex = regexp.MustCompile(`ELEMENTPLACEHOLDER\d+|CODEBLOCKPLACEHOLDER\d+|INLINECODEPLACEHOLDER\d+|LINKPLACEHOLDER\d+|BOLDPLACEHOLDER\d+|ITALICPLACEHOLDER\d+|STRIKEPLACEHOLDER\d+`)

	// Element detection
	codeBlockRegex  = regexp.MustCompile("```(?s:.*?)```")
	inlineCodeRegex = regexp.MustCompile("`[^`]+`")
	linkRegex       = regexp.MustCompile(`\[[^\]]+\]\([^\)]+\)`)

	// URL wrapping
	urlRegex = regexp.MustCompile(`\bhttps?://[a-zA-Z0-9-.]+(.[a-zA-Z]{2,})(:[0-9]{1,5})?(/[a-zA-Z0-9-_.~%/?#=&+]*)?\b`)

	// Style conversions
	boldRegex           = regexp.MustCompile(`\*\*(.*?)\*\*`)
	boldUnderscore      = regexp.MustCompile(`__(.*?)__`)
	italicAsterisk      = regexp.MustCompile(`\*(.*?)\*`)
	italicUnderscore    = regexp.MustCompile(`_(.*?)_`)
	strikeRegex         = regexp.MustCompile(`~~(.*?)~~`)

	// Code block language extraction
	codeBlockLangRegex = regexp.MustCompile("(?s)```([a-zA-Z]*)\\n(.*?)\\n```")

	// Inline code extraction
	inlineCodeExtractRegex = regexp.MustCompile("`([^`]+)`")
)

// mdReplacer escapes special characters for Telegram MarkdownV2.
// These are the characters that MUST be escaped in Telegram MarkdownV2.
var mdReplacer = strings.NewReplacer(
	"_", "\\_",
	"*", "\\*",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"~", "\\~",
	"`", "\\`",
	">", "\\>",
	"#", "\\#",
	"+", "\\+",
	"-", "\\-",
	"=", "\\=",
	"|", "\\|",
	"{", "\\{",
	"}", "\\}",
	".", "\\.",
	"!", "\\!",
)

// EscapeMarkdown escapes text for Telegram's MarkdownV2 format.
// This escapes all special characters and should be used for plain text
// that shouldn't have any markdown formatting interpreted.
func EscapeMarkdown(text string) string {
	return mdReplacer.Replace(text)
}

// EscapeMarkdownf escapes formatted text for MarkdownV2.
func EscapeMarkdownf(format string, args ...interface{}) string {
	text := EscapeMarkdown(format)
	// Re-escape any args that might be strings
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			args[i] = EscapeMarkdown(s)
		}
	}
	return fmt.Sprintf(text, args...)
}

// wrapLinksInMarkdown wraps unwrapped URLs in markdown link format.
func wrapLinksInMarkdown(text string) string {
	return urlRegex.ReplaceAllStringFunc(text, func(link string) string {
		if strings.Contains(link, "](http") || strings.Contains(link, "[http") {
			return link
		}
		return fmt.Sprintf("[%s](%s)", link, link)
	})
}

// removePlaceholders removes any existing placeholders from text.
func removePlaceholders(text string) string {
	return placeholderRegex.ReplaceAllString(text, "")
}

// addPlaceholders replaces code blocks, inline code, and links with placeholders.
func addPlaceholders(text string, elements map[string]string) (string, map[string]string) {
	elementsRegex := regexp.MustCompile(fmt.Sprintf("%s|%s|%s", codeBlockRegex.String(), inlineCodeRegex.String(), linkRegex.String()))

	matches := elementsRegex.FindAllString(text, -1)
	for i, match := range matches {
		placeholder := fmt.Sprintf("ELEMENTPLACEHOLDER%d", len(elements)+i)

		// Determine element type and add appropriate marker
		switch {
		case codeBlockRegex.MatchString(match):
			placeholder = fmt.Sprintf("CODEBLOCKPLACEHOLDER%d", len(elements)+i)
		case inlineCodeRegex.MatchString(match):
			placeholder = fmt.Sprintf("INLINECODEPLACEHOLDER%d", len(elements)+i)
		case linkRegex.MatchString(match):
			placeholder = fmt.Sprintf("LINKPLACEHOLDER%d", len(elements)+i)
		}

		elements[placeholder] = match
		text = strings.Replace(text, match, placeholder, 1)
	}
	return text, elements
}

// processPlaceholders restores placeholders with properly escaped content.
func processPlaceholders(md string, elements map[string]string) string {
	for placeholder, element := range elements {
		if strings.HasPrefix(placeholder, "LINKPLACEHOLDER") {
			parts := strings.SplitN(element, "](", 2)
			linkText := parts[0][1:]
			linkURL := parts[1][:len(parts[1])-1]

			linkText = EscapeMarkdown(linkText)
			element = "[" + linkText + "](" + linkURL + ")"
		}

		if strings.HasPrefix(placeholder, "CODEBLOCKPLACEHOLDER") {
			element = codeBlockLangRegex.ReplaceAllStringFunc(element, func(block string) string {
				matches := codeBlockLangRegex.FindStringSubmatch(block)
				if len(matches) > 2 {
					language := matches[1]
					return fmt.Sprintf("```%s\n%s\n```", language, escapeTelegramMarkdownV2CodeBlocks(matches[2]))
				}
				return block
			})
		}

		if strings.HasPrefix(placeholder, "INLINECODEPLACEHOLDER") {
			element = inlineCodeExtractRegex.ReplaceAllStringFunc(element, func(block string) string {
				matches := inlineCodeExtractRegex.FindStringSubmatch(block)
				if len(matches) > 1 {
					return fmt.Sprintf("`%s`", escapeTelegramMarkdownV2CodeBlocks(matches[1]))
				}
				return block
			})
		}

		md = strings.Replace(md, placeholder, element, 1)
	}

	return md
}

// escapeTelegramMarkdownV2CodeBlocks escapes special characters inside code blocks.
func escapeTelegramMarkdownV2CodeBlocks(text string) string {
	// First handle special backslash patterns
	backslashRegex := regexp.MustCompile(`\\[()|]`)

	text = backslashRegex.ReplaceAllStringFunc(text, func(match string) string {
		if len(match) > 1 {
			return fmt.Sprintf("\\\\%s", match[1:])
		}
		return "\\\\"
	})

	text = EscapeMarkdown(text)

	return text
}

// escapeMarkdownExcept preserves formatting chars but escapes all others.
// Used for text inside bold/italic/strikethrough that needs escaping but
// not the formatting characters themselves.
func escapeMarkdownExcept(text string, preserve []rune) string {
	var result strings.Builder
	preserveMap := make(map[rune]bool)
	for _, r := range preserve {
		preserveMap[r] = true
	}

	replacements := map[rune]string{
		'_':  "\\_",
		'*':  "\\*",
		'[':  "\\[",
		']':  "\\]",
		'(':  "\\(",
		')':  "\\)",
		'~':  "\\~",
		'`':  "\\`",
		'>':  "\\>",
		'#':  "\\#",
		'+':  "\\+",
		'-':  "\\-",
		'=':  "\\=",
		'|':  "\\|",
		'{':  "\\{",
		'}':  "\\}",
		'.':  "\\.",
		'!':  "\\!",
	}

	for _, r := range text {
		if preserveMap[r] {
			result.WriteRune(r)
		} else if repl, ok := replacements[r]; ok {
			result.WriteString(repl)
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// processStyles converts markdown formatting to Telegram MarkdownV2 format.
// It converts styles to Telegram format and uses placeholders to protect
// the resulting formatting characters from being escaped.
func processStyles(md string, elements map[string]string) string {
	// Bold first: convert **text** and __text__ to placeholders containing *text*
	// We use placeholders to protect the asterisks from being escaped.
	// The text content inside is escaped except for * which is the format char.
	md = boldRegex.ReplaceAllStringFunc(md, func(match string) string {
		matches := boldRegex.FindStringSubmatch(match)
		if len(matches) > 1 {
			placeholder := fmt.Sprintf("BOLDPLACEHOLDER%d", len(elements))
			escaped := escapeMarkdownExcept(matches[1], []rune{'*'})
			elements[placeholder] = "*" + escaped + "*"
			return placeholder
		}
		return match
	})

	md = boldUnderscore.ReplaceAllStringFunc(md, func(match string) string {
		matches := boldUnderscore.FindStringSubmatch(match)
		if len(matches) > 1 {
			placeholder := fmt.Sprintf("BOLDPLACEHOLDER%d", len(elements))
			escaped := escapeMarkdownExcept(matches[1], []rune{'*'})
			elements[placeholder] = "*" + escaped + "*"
			return placeholder
		}
		return match
	})

	// Italic: convert *text* or _text_ to placeholders containing _text_
	// (Telegram italic). We use placeholders to protect underscores from escaping.
	// The text content inside is escaped except for _ which is the format char.
	md = italicAsterisk.ReplaceAllStringFunc(md, func(match string) string {
		matches := italicAsterisk.FindStringSubmatch(match)
		if len(matches) > 1 {
			placeholder := fmt.Sprintf("ITALICPLACEHOLDER%d", len(elements))
			escaped := escapeMarkdownExcept(matches[1], []rune{'_'})
			elements[placeholder] = "_" + escaped + "_"
			return placeholder
		}
		return match
	})

	md = italicUnderscore.ReplaceAllStringFunc(md, func(match string) string {
		matches := italicUnderscore.FindStringSubmatch(match)
		if len(matches) > 1 {
			placeholder := fmt.Sprintf("ITALICPLACEHOLDER%d", len(elements))
			escaped := escapeMarkdownExcept(matches[1], []rune{'_'})
			elements[placeholder] = "_" + escaped + "_"
			return placeholder
		}
		return match
	})

	// Strikethrough: convert ~~text~~ to placeholders containing ~text~
	// (Telegram strikethrough). We use placeholders to protect ~ from escaping.
	// The text content inside is escaped except for ~ which is the format char.
	md = strikeRegex.ReplaceAllStringFunc(md, func(match string) string {
		matches := strikeRegex.FindStringSubmatch(match)
		if len(matches) > 1 {
			placeholder := fmt.Sprintf("STRIKEPLACEHOLDER%d", len(elements))
			escaped := escapeMarkdownExcept(matches[1], []rune{'~'})
			elements[placeholder] = "~" + escaped + "~"
			return placeholder
		}
		return match
	})

	return md
}

// ConvertMarkdownToTelegram converts standard markdown to Telegram MarkdownV2 format.
// It handles:
//   - Converting **bold** and __bold__ to *bold* (Telegram bold)
//   - Converting *italic* and _italic_ to _italic_ (Telegram italic)
//   - Preserving code blocks and inline code with proper escaping
//   - Auto-wrapping unwrapped URLs in markdown links
//   - Escaping special characters that need escaping in MarkdownV2
func ConvertMarkdownToTelegram(md string) string {
	elements := make(map[string]string)

	md = removePlaceholders(md)
	md, elements = addPlaceholders(md, elements)

	md = wrapLinksInMarkdown(md)
	md, elements = addPlaceholders(md, elements)

	// Process markdown styles BEFORE escaping.
	// Bold (** and __) is processed first and wrapped in placeholders to protect
	// the asterisks from being escaped. Then italic (* and _) is processed.
	md = processStyles(md, elements)

	md = EscapeMarkdown(md)

	md = processPlaceholders(md, elements)

	return md
}
