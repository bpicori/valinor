package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Header bar - subtle dark, like reference "Π Build kimi-k2.5-free · 3.3s"
	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	// User message prefix "> "
	userPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Bold(true)

	// Assistant text (base)
	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			PaddingLeft(2)

	// Tool call - compact inline format
	toolCallStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			PaddingLeft(2)

	// Tool names - bright yellow-green like reference (e.g. bash, read, glob)
	toolNameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("154")).
			Bold(true)

	// File paths and actions - light cyan/blue
	pathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117"))

	// Spinner style - subtle
	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("154"))

	// Status bar - muted
	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	// Input - minimal border, dark theme
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(lipgloss.Color("238")).
				Padding(0, 1)

	// Error message style
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	// Right sidebar - session info
	sidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1).
			Width(24)

	sidebarHeadingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				Bold(true).
				MarginBottom(1)

	sidebarValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	// User message block - thick blue left bar only, section-like
	userMessageBlockStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("69")).
				Padding(0, 1).
				Margin(0, 0, 1, 0)

	// Assistant message block - no border/bar, just padding
	assistantMessageBlockStyle = lipgloss.NewStyle().
					Padding(0, 1).
					Margin(0, 0, 1, 0)

	// Footer (bottom bar) - model on the left
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	footerModelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Bold(true)

	// Tokens section - muted, compact
	tokensStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	tokensLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))

	tokensValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
)
