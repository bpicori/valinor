package tui

import "github.com/charmbracelet/glamour"

// renderMarkdown converts a markdown string to styled ANSI terminal output.
// Uses glamour with "dark" style and word-wrapping to the given width.
func renderMarkdown(content string, width int) string {
	if content == "" {
		return ""
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return content // fallback to raw text
	}
	out, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return out
}
