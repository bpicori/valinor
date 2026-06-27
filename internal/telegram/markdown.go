// Package telegram provides markdown rendering helpers for Telegram.
package telegram

import (
	"fmt"
	stdhtml "html"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
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

type telegramFormattedMarkdown struct {
	HTML      string
	PlainText string
}

type telegramRenderState struct {
	sb        strings.Builder
	listDepth int
}

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

// FormatTelegramMarkdown parses markdown with goldmark and renders a Telegram-safe
// HTML subset plus a plain-text fallback that can be used when Telegram rejects
// formatted content.
func FormatTelegramMarkdown(md string) telegramFormattedMarkdown {
	parser := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
	)
	source := []byte(md)
	doc := parser.Parser().Parse(gmtext.NewReader(source))

	return telegramFormattedMarkdown{
		HTML:      strings.TrimSpace(renderTelegramHTML(doc, source)),
		PlainText: strings.TrimSpace(renderTelegramPlainText(doc, source)),
	}
}

func renderTelegramHTML(node ast.Node, source []byte) string {
	state := &telegramRenderState{}
	renderTelegramHTMLNode(node, source, state)
	return state.sb.String()
}

func renderTelegramHTMLNode(node ast.Node, source []byte, state *telegramRenderState) {
	switch n := node.(type) {
	case *ast.Document:
		renderTelegramHTMLChildren(node, source, state)
	case *ast.Heading:
		state.sb.WriteString("<b>")
		renderTelegramHTMLChildren(node, source, state)
		state.sb.WriteString("</b>")
		writeHTMLBlockBreak(node, state)
	case *ast.Paragraph:
		renderTelegramHTMLChildren(node, source, state)
		writeHTMLBlockBreak(node, state)
	case *ast.Text:
		state.sb.WriteString(escapeTelegramHTML(string(n.Value(source))))
		if n.HardLineBreak() || n.SoftLineBreak() {
			state.sb.WriteString("\n")
		}
	case *ast.String:
		state.sb.WriteString(escapeTelegramHTML(string(n.Value)))
	case *ast.Emphasis:
		tag := "i"
		if n.Level >= 2 {
			tag = "b"
		}
		state.sb.WriteString("<")
		state.sb.WriteString(tag)
		state.sb.WriteString(">")
		renderTelegramHTMLChildren(node, source, state)
		state.sb.WriteString("</")
		state.sb.WriteString(tag)
		state.sb.WriteString(">")
	case *extensionast.Strikethrough:
		state.sb.WriteString("<s>")
		renderTelegramHTMLChildren(node, source, state)
		state.sb.WriteString("</s>")
	case *ast.CodeSpan:
		state.sb.WriteString("<code>")
		state.sb.WriteString(escapeTelegramHTML(renderTelegramPlainText(node, source)))
		state.sb.WriteString("</code>")
	case *ast.FencedCodeBlock:
		state.sb.WriteString("<pre>")
		state.sb.WriteString(escapeTelegramHTML(string(n.Text(source))))
		state.sb.WriteString("</pre>")
		writeHTMLBlockBreak(node, state)
	case *ast.CodeBlock:
		state.sb.WriteString("<pre>")
		state.sb.WriteString(escapeTelegramHTML(string(n.Text(source))))
		state.sb.WriteString("</pre>")
		writeHTMLBlockBreak(node, state)
	case *ast.Blockquote:
		quote := strings.Trim(renderTelegramPlainTextChildrenToString(node, source), "\n")
		if quote != "" {
			state.sb.WriteString("<i>")
			state.sb.WriteString(escapeTelegramHTML(quote))
			state.sb.WriteString("</i>")
			writeHTMLBlockBreak(node, state)
		}
	case *extensionast.Table:
		table := formatTelegramTable(n, source)
		if table != "" {
			state.sb.WriteString("<pre>")
			state.sb.WriteString(escapeTelegramHTML(table))
			state.sb.WriteString("</pre>")
			writeHTMLBlockBreak(node, state)
		}
	case *ast.List:
		renderTelegramHTMLList(n, source, state)
	case *ast.Link:
		dest := sanitizeTelegramURL(string(n.Destination))
		if dest == "" {
			renderTelegramHTMLChildren(node, source, state)
			return
		}
		state.sb.WriteString("<a href=\"")
		state.sb.WriteString(escapeTelegramHTMLAttr(dest))
		state.sb.WriteString("\">")
		renderTelegramHTMLChildren(node, source, state)
		state.sb.WriteString("</a>")
	case *ast.AutoLink:
		dest := sanitizeTelegramURL(string(n.URL(source)))
		label := string(n.Label(source))
		if dest == "" {
			state.sb.WriteString(escapeTelegramHTML(label))
			return
		}
		state.sb.WriteString("<a href=\"")
		state.sb.WriteString(escapeTelegramHTMLAttr(dest))
		state.sb.WriteString("\">")
		state.sb.WriteString(escapeTelegramHTML(label))
		state.sb.WriteString("</a>")
	case *ast.RawHTML:
		state.sb.WriteString(escapeTelegramHTML(string(n.Text(source))))
	case *ast.HTMLBlock:
		state.sb.WriteString(escapeTelegramHTML(string(n.Text(source))))
		writeHTMLBlockBreak(node, state)
	case *ast.ThematicBreak:
		state.sb.WriteString("----")
		writeHTMLBlockBreak(node, state)
	case *ast.Image:
		alt := strings.TrimSpace(renderTelegramPlainText(node, source))
		if alt != "" {
			state.sb.WriteString(escapeTelegramHTML(alt))
		}
	default:
		renderTelegramHTMLChildren(node, source, state)
	}
}

func renderTelegramHTMLChildren(node ast.Node, source []byte, state *telegramRenderState) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		renderTelegramHTMLNode(child, source, state)
	}
}

func renderTelegramHTMLList(list *ast.List, source []byte, state *telegramRenderState) {
	index := list.Start
	if index == 0 {
		index = 1
	}

	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		indent := strings.Repeat("  ", state.listDepth)
		marker := "• "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index)
			index++
		}

		state.sb.WriteString(indent)
		state.sb.WriteString(marker)

		itemState := &telegramRenderState{listDepth: state.listDepth + 1}
		renderTelegramHTMLChildren(item, source, itemState)
		body := strings.Trim(itemState.sb.String(), "\n")
		state.sb.WriteString(indentContinuation(body, indent+strings.Repeat(" ", len(marker))))

		if item.NextSibling() != nil {
			state.sb.WriteString("\n")
		}
	}

	writeHTMLBlockBreak(list, state)
}

func renderTelegramPlainText(node ast.Node, source []byte) string {
	state := &telegramRenderState{}
	renderTelegramPlainTextNode(node, source, state)
	return state.sb.String()
}

func renderTelegramPlainTextNode(node ast.Node, source []byte, state *telegramRenderState) {
	switch n := node.(type) {
	case *ast.Document:
		renderTelegramPlainTextChildren(node, source, state)
	case *ast.Heading:
		renderTelegramPlainTextChildren(node, source, state)
		writePlainBlockBreak(node, state)
	case *ast.Paragraph:
		renderTelegramPlainTextChildren(node, source, state)
		writePlainBlockBreak(node, state)
	case *ast.Text:
		state.sb.WriteString(string(n.Value(source)))
		if n.HardLineBreak() || n.SoftLineBreak() {
			state.sb.WriteString("\n")
		}
	case *ast.String:
		state.sb.WriteString(string(n.Value))
	case *ast.Emphasis:
		renderTelegramPlainTextChildren(node, source, state)
	case *extensionast.Strikethrough:
		renderTelegramPlainTextChildren(node, source, state)
	case *ast.CodeSpan:
		state.sb.WriteString(renderTelegramPlainTextChildrenToString(node, source))
	case *ast.FencedCodeBlock:
		state.sb.WriteString(string(n.Text(source)))
		writePlainBlockBreak(node, state)
	case *ast.CodeBlock:
		state.sb.WriteString(string(n.Text(source)))
		writePlainBlockBreak(node, state)
	case *ast.Blockquote:
		quote := strings.Trim(renderTelegramPlainTextChildrenToString(node, source), "\n")
		if quote != "" {
			state.sb.WriteString(prefixLines(quote, "> "))
			writePlainBlockBreak(node, state)
		}
	case *extensionast.Table:
		table := formatTelegramTable(n, source)
		if table != "" {
			state.sb.WriteString(table)
			writePlainBlockBreak(node, state)
		}
	case *ast.List:
		renderTelegramPlainTextList(n, source, state)
	case *ast.Link:
		label := strings.TrimSpace(renderTelegramPlainTextChildrenToString(node, source))
		dest := sanitizeTelegramURL(string(n.Destination))
		switch {
		case label == "":
			state.sb.WriteString(dest)
		case dest == "":
			state.sb.WriteString(label)
		case label == dest:
			state.sb.WriteString(label)
		default:
			state.sb.WriteString(label)
			state.sb.WriteString(" (")
			state.sb.WriteString(dest)
			state.sb.WriteString(")")
		}
	case *ast.AutoLink:
		state.sb.WriteString(string(n.URL(source)))
	case *ast.RawHTML:
		state.sb.WriteString(string(n.Text(source)))
	case *ast.HTMLBlock:
		state.sb.WriteString(string(n.Text(source)))
		writePlainBlockBreak(node, state)
	case *ast.ThematicBreak:
		state.sb.WriteString("----")
		writePlainBlockBreak(node, state)
	case *ast.Image:
		alt := strings.TrimSpace(renderTelegramPlainTextChildrenToString(node, source))
		if alt != "" {
			state.sb.WriteString(alt)
		}
	default:
		renderTelegramPlainTextChildren(node, source, state)
	}
}

func renderTelegramPlainTextChildren(node ast.Node, source []byte, state *telegramRenderState) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		renderTelegramPlainTextNode(child, source, state)
	}
}

func renderTelegramPlainTextChildrenToString(node ast.Node, source []byte) string {
	state := &telegramRenderState{}
	renderTelegramPlainTextChildren(node, source, state)
	return state.sb.String()
}

func renderTelegramPlainTextList(list *ast.List, source []byte, state *telegramRenderState) {
	index := list.Start
	if index == 0 {
		index = 1
	}

	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		indent := strings.Repeat("  ", state.listDepth)
		marker := "• "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index)
			index++
		}

		state.sb.WriteString(indent)
		state.sb.WriteString(marker)

		itemState := &telegramRenderState{listDepth: state.listDepth + 1}
		renderTelegramPlainTextChildren(item, source, itemState)
		body := strings.Trim(itemState.sb.String(), "\n")
		state.sb.WriteString(indentContinuation(body, indent+strings.Repeat(" ", len(marker))))

		if item.NextSibling() != nil {
			state.sb.WriteString("\n")
		}
	}

	writePlainBlockBreak(list, state)
}

func writeHTMLBlockBreak(node ast.Node, state *telegramRenderState) {
	if node.NextSibling() != nil {
		state.sb.WriteString("\n\n")
	}
}

func writePlainBlockBreak(node ast.Node, state *telegramRenderState) {
	if node.NextSibling() != nil {
		state.sb.WriteString("\n\n")
	}
}

func escapeTelegramHTML(text string) string {
	return stdhtml.EscapeString(text)
}

func escapeTelegramHTMLAttr(text string) string {
	return stdhtml.EscapeString(text)
}

func sanitizeTelegramURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tg":
		return parsed.String()
	default:
		return ""
	}
}

func indentContinuation(text, indent string) string {
	if text == "" {
		return ""
	}
	return strings.ReplaceAll(text, "\n", "\n"+indent)
}

func prefixLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func formatTelegramTable(table *extensionast.Table, source []byte) string {
	rows := extractTelegramTableRows(table, source)
	if len(rows) == 0 {
		return ""
	}

	colCount := 0
	for _, row := range rows {
		if len(row) > colCount {
			colCount = len(row)
		}
	}
	if colCount == 0 {
		return ""
	}

	widths := make([]int, colCount)
	for _, row := range rows {
		for i, cell := range row {
			if width := utf8.RuneCountInString(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}

	var sb strings.Builder
	for ri, row := range rows {
		for i := 0; i < colCount; i++ {
			if i > 0 {
				sb.WriteString("  ")
			}
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			sb.WriteString(padTelegramTableCell(cell, widths[i]))
		}
		if ri < len(rows)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func extractTelegramTableRows(table *extensionast.Table, source []byte) [][]string {
	var rows [][]string
	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch row := child.(type) {
		case *extensionast.TableHeader, *extensionast.TableRow:
			cells := extractTelegramTableRowCells(row, source)
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
		}
	}
	return rows
}

func extractTelegramTableRowCells(row ast.Node, source []byte) []string {
	var cells []string
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		tc, ok := cell.(*extensionast.TableCell)
		if !ok {
			continue
		}
		cells = append(cells, strings.TrimSpace(renderTelegramPlainText(tc, source)))
	}
	return cells
}

func padTelegramTableCell(cell string, width int) string {
	padding := width - utf8.RuneCountInString(cell)
	if padding <= 0 {
		return cell
	}
	return cell + strings.Repeat(" ", padding)
}
