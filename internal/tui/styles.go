package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Message styles.
	userPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Bold(true)

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			PaddingLeft(2)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	userMessageBlockStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("235")).
				Border(lipgloss.ThickBorder(), false, false, false, true).
				BorderBackground(lipgloss.Color("235")).
				Padding(1, 1).
				Margin(0, 0, 1, 0).
				Align(lipgloss.Left, lipgloss.Center)

	assistantMessageBlockStyle = lipgloss.NewStyle().
					Padding(0, 1).
					Margin(0, 0, 1, 0)

	// Tool rendering styles.
	toolCallStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			PaddingLeft(2)

	toolNameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("154")).
			Bold(true)

	pathStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117"))

	// Input and status styles.
	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("154"))

	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(lipgloss.Color("238")).
				Padding(0, 1)

	// Footer styles.
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	footerModelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Bold(true)

	gatewayConnectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("108")).
				Bold(true)

	gatewayDisconnectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("203")).
					Bold(true)

	// Token usage styles.
	tokensLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))

	tokensValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
)
