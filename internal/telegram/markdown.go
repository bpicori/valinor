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
	placeholderRegex = regexp.MustCompile(`ELEMENTPLACEHOLDER\d+|CODEBLOCKPLACEHOLDER\d+|INLINECODEPLACEHOLDER\d+|LINKPLACEHOLDER\d`)

	// Element detection
	codeBlockRegex  = regexp.MustCompile("```(?s:.*?)```")
	inlineCodeRegex = regexp.MustCompile("`[^`]+`")
	linkRegex       = regexp.MustCompile(`\[[^\]]+\]\([^\)]+\)`)

	// URL wrapping
	urlRegex = regexp.MustCompile(`\bhttps?://[a-zA-Z0-9-.]+(.[a-zA-Z]{2,})(:[0-9]{1,5})?(/[a-zA-Z0-9-_.~%/?#=&+]*)?\b`)

	// Style conversions
	boldRegex        = regexp.MustCompile(`\*\*(.*?)\*\*`)
	italicAsterisk   = regexp.MustCompile(`\*(.*?)\*`)
	italicUnderscore = regexp.MustCompile(`_(.*?)_`)
	strikeRegex      = regexp.MustCompile(`~~(.*?)~~`)

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

// processStyles converts markdown formatting to Telegram MarkdownV2 format.
func processStyles(md string) string {
	// bold text: convert **text** to *text*
	md = boldRegex.ReplaceAllString(md, "*$1*")

	// italic text: convert *text* or _text_ to _text_
	md = italicAsterisk.ReplaceAllString(md, "_${1}_")
	md = italicUnderscore.ReplaceAllString(md, "_${1}_")

	// strikethrough text: convert ~~text~~ to ~text~
	md = strikeRegex.ReplaceAllString(md, "~$1~")

	return md
}

// ConvertMarkdownToTelegram converts standard markdown to Telegram MarkdownV2 format.
// It handles:
//   - Converting **bold** to *bold* (Telegram bold)
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

	md = EscapeMarkdown(md)

	md = processStyles(md)

	md = processPlaceholders(md, elements)

	return md
}
