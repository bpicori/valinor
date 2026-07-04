package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Theme holds GrokNight-inspired terminal colors.
type Theme struct {
	BgBase             string
	BgLight            string
	BgHighlight        string
	TextPrimary        string
	TextSecondary      string
	GrayDim            string
	AccentUser         string
	AccentTool         string
	AccentSuccess      string
	AccentError        string
	AccentRunning      string
	PromptBorder       string
	PromptBorderActive string
	MarkdownText       string
	MarkdownCode       string
	MarkdownLink       string
}

// GrokNight is the default dark theme modeled after Grok Build.
var GrokNight = Theme{
	BgBase:             "#0a0a0a",
	BgLight:            "#1e1e22",
	BgHighlight:        "#323237",
	TextPrimary:        "#c8c8c8",
	TextSecondary:      "#787878",
	GrayDim:            "#6c6c6c",
	AccentUser:         "#bb9af7",
	AccentTool:           "#7aa2f7",
	AccentSuccess:        "#9ece6a",
	AccentError:          "#f7768e",
	AccentRunning:        "#e0af68",
	PromptBorder:       "#242424",
	PromptBorderActive: "#bb9af7",
	MarkdownText:       "#e1e1e1",
	MarkdownCode:       "#7aa2f7",
	MarkdownLink:         "#9d7cd8",
}

// currentTheme is the active TUI theme.
var currentTheme = GrokNight

func color(hex string) lipgloss.Color {
	return lipgloss.Color(hex)
}

// applyBackground wraps content in the base background and sizes it to the terminal.
func applyBackground(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return content
	}
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Background(color(currentTheme.BgBase)).
		Render(content)
}

// accentChar is the left accent marker used on user and prompt blocks.
const accentChar = "❙"

func renderAccentLine(active bool) string {
	accentColor := currentTheme.PromptBorder
	if active {
		accentColor = currentTheme.AccentUser
	}
	return lipgloss.NewStyle().Foreground(color(accentColor)).Render(accentChar)
}

func separatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(color(currentTheme.GrayDim))
}

func renderSeparator() string {
	return separatorStyle().Render(" │ ")
}

func truncateMiddle(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	keep := (max - 3) / 2
	return s[:keep] + "..." + s[len(s)-((max-3)-keep):]
}