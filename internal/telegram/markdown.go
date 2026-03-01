// Package telegram provides event-to-message rendering for Telegram.
package telegram

import (
	"fmt"
	"regexp"
	"strings"
)

// escapeTelegramMarkdownV2 escapes special characters for Telegram MarkdownV2.
func escapeTelegramMarkdownV2(text string) string {
	telegramSpecialChars := "_*[]()~`>#+-=|{}.!"
	for _, char := range telegramSpecialChars {
		text = strings.ReplaceAll(text, string(char), "\\"+string(char))
	}
	return text
}

// escapeTelegramMarkdownV2CodeBlocks escapes special characters inside code blocks.
func escapeTelegramMarkdownV2CodeBlocks(text string) string {
	backslashRegex := regexp.MustCompile(`\([^ ]|\|$)`)

	text = backslashRegex.ReplaceAllStringFunc(text, func(match string) string {
		if len(match) > 1 {
			return fmt.Sprintf("\\\\%s", match[1:])
		}

		return "\\\\"
	})

	text = escapeTelegramMarkdownV2(text)

	return text
}

// wrapLinksInMarkdown wraps unwrapped URLs in markdown link format.
func wrapLinksInMarkdown(text string) string {
	re := regexp.MustCompile(`\bhttps?://[a-zA-Z0-9-.]+(.[a-zA-Z]{2,})(:[0-9]{1,5})?(/[a-zA-Z0-9-_.~%/?#=&+]*)?\b`)

	wrappedText := re.ReplaceAllStringFunc(text, func(link string) string {
		if strings.Contains(link, "](http") || strings.Contains(link, "[http") {
			return link
		}
		return fmt.Sprintf("[%s](%s)", link, link)
	})

	return wrappedText
}

// removePlaceholders removes any existing placeholders from text.
func removePlaceholders(text string) string {
	re := regexp.MustCompile(`ELEMENTPLACEHOLDER\d+|CODEBLOCKPLACEHOLDER\d+|INLINECODEPLACEHOLDER\d+|LINKPLACEHOLDER\d`)

	result := re.ReplaceAllString(text, "")

	return result
}

// addPlaceholders replaces code blocks, inline code, and links with placeholders.
func addPlaceholders(text string, elements map[string]string) (string, map[string]string) {
	codeBlockRegex := regexp.MustCompile("```(?s:.*?)```")
	inlineCodeRegex := regexp.MustCompile("`[^`]+`")
	linkRegex := regexp.MustCompile(`\[[^\]]+\]\([^\)]+\)`)

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

			linkText = escapeTelegramMarkdownV2(linkText)
			element = "[" + linkText + "](" + linkURL + ")"
		}

		if strings.HasPrefix(placeholder, "CODEBLOCKPLACEHOLDER") {
			re := regexp.MustCompile("(?s)```([a-zA-Z]*)\\n(.*?)\\n```")

			element = re.ReplaceAllStringFunc(element, func(block string) string {
				matches := re.FindStringSubmatch(block)
				if len(matches) > 2 {
					language := matches[1]
					return fmt.Sprintf("```%s\n%s\n```", language, escapeTelegramMarkdownV2CodeBlocks(matches[2]))
				}
				return block
			})
		}

		if strings.HasPrefix(placeholder, "INLINECODEPLACEHOLDER") {
			re := regexp.MustCompile("`([^`]+)`")

			element = re.ReplaceAllStringFunc(element, func(block string) string {
				matches := re.FindStringSubmatch(block)
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

// processStyles converts markdown formatting to Telegram MarkdownV2 format.
func processStyles(md string) string {
	replacements := []struct {
		regex       *regexp.Regexp
		replacement string
	}{
		// bold text: convert **text** to *text*
		{regexp.MustCompile(`\\\*\\\*(.*?)\\\*\\\*`), `*$1*`},

		// italic text: convert *text* or _text_ to _text_
		{regexp.MustCompile(`\\\*(.*?)\\\*`), `_${1}_`},
		{regexp.MustCompile(`\\_(.*?)\\_`), `_${1}_`},

		// strikethrough text: convert ~~text~ or ~text~ to ~text~
		{regexp.MustCompile(`\\~\\~(.*?)\\~\\~`), `~$1~`},
		{regexp.MustCompile(`\\~(.*?)\\~`), `~$1~`},
	}

	for _, r := range replacements {
		md = r.regex.ReplaceAllString(md, r.replacement)
	}

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

	md = escapeTelegramMarkdownV2(md)

	md = processStyles(md)

	md = processPlaceholders(md, elements)

	return md
}
