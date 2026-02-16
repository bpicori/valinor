package ui

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
)
