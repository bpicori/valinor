package tui

import (
	_ "embed"
	"sync"

	"github.com/charmbracelet/glamour"
)

//go:embed groknight.json
var groknightMarkdownStyle []byte

var markdownRendererCache sync.Map // map[int]*glamour.TermRenderer

func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(groknightMarkdownStyle),
		glamour.WithWordWrap(width),
	)
}