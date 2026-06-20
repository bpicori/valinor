package tui

import "github.com/charmbracelet/lipgloss"

// Styles groups lipgloss styles derived from the active theme.
type Styles struct {
	UserPrefix             lipgloss.Style
	Assistant              lipgloss.Style
	Error                  lipgloss.Style
	UserMessageBlock       lipgloss.Style
	UserMessageBody        lipgloss.Style
	AssistantMessageBlock  lipgloss.Style
	SystemMessage          lipgloss.Style
	ToolCall               lipgloss.Style
	ToolName               lipgloss.Style
	ToolPath               lipgloss.Style
	Spinner                lipgloss.Style
	InputContainer         lipgloss.Style
	InputAccent            lipgloss.Style
	InputAccentInactive    lipgloss.Style
	InputBorder            lipgloss.Style
	InputBorderActive      lipgloss.Style
	Footer                 lipgloss.Style
	FooterStrong           lipgloss.Style
	GatewayConnected       lipgloss.Style
	GatewayDisconnected    lipgloss.Style
	TokensLabel            lipgloss.Style
	TokensValue            lipgloss.Style
	Hint                   lipgloss.Style
	Thinking               lipgloss.Style
	PickerTitle            lipgloss.Style
	PickerNormalTitle      lipgloss.Style
	PickerNormalDesc       lipgloss.Style
	PickerSelectedTitle    lipgloss.Style
	PickerSelectedDesc     lipgloss.Style
}

func newStyles(t Theme) Styles {
	return Styles{
		UserPrefix: lipgloss.NewStyle().
			Foreground(color(t.AccentUser)).
			Bold(true),
		Assistant: lipgloss.NewStyle().
			Foreground(color(t.TextPrimary)),
		Error: lipgloss.NewStyle().
			Foreground(color(t.AccentError)).
			Bold(true),
		UserMessageBlock: lipgloss.NewStyle().
			Margin(0, 0, 1, 0),
		UserMessageBody: lipgloss.NewStyle().
			Background(color(t.BgLight)).
			Foreground(color(t.TextPrimary)).
			Padding(0, 1),
		AssistantMessageBlock: lipgloss.NewStyle().
			Padding(0, 1).
			Margin(0, 0, 1, 0),
		SystemMessage: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)).
			Italic(true),
		ToolCall: lipgloss.NewStyle().
			Foreground(color(t.GrayDim)),
		ToolName: lipgloss.NewStyle().
			Foreground(color(t.AccentTool)).
			Bold(true),
		ToolPath: lipgloss.NewStyle().
			Foreground(color(t.AccentTool)),
		Spinner: lipgloss.NewStyle().
			Foreground(color(t.AccentRunning)),
		InputContainer: lipgloss.NewStyle().
			Margin(0, 1, 1, 1),
		InputAccent: lipgloss.NewStyle().
			Foreground(color(t.AccentUser)),
		InputAccentInactive: lipgloss.NewStyle().
			Foreground(color(t.PromptBorder)),
		InputBorder: lipgloss.NewStyle().
			Background(color(t.BgLight)).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(color(t.PromptBorder)).
			Padding(0, 2),
		InputBorderActive: lipgloss.NewStyle().
			Background(color(t.BgLight)).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(color(t.PromptBorderActive)).
			Padding(0, 2),
		Footer: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)).
			Padding(0, 1),
		FooterStrong: lipgloss.NewStyle().
			Foreground(color(t.TextPrimary)).
			Bold(false),
		GatewayConnected: lipgloss.NewStyle().
			Foreground(color(t.AccentSuccess)).
			Bold(true),
		GatewayDisconnected: lipgloss.NewStyle().
			Foreground(color(t.AccentError)).
			Bold(true),
		TokensLabel: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)),
		TokensValue: lipgloss.NewStyle().
			Foreground(color(t.TextPrimary)),
		Hint: lipgloss.NewStyle().
			Foreground(color(t.GrayDim)),
		Thinking: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)),
		PickerTitle: lipgloss.NewStyle().
			Foreground(color(t.TextPrimary)).
			Bold(true),
		PickerNormalTitle: lipgloss.NewStyle().
			Foreground(color(t.TextPrimary)),
		PickerNormalDesc: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)),
		PickerSelectedTitle: lipgloss.NewStyle().
			Foreground(color(t.AccentUser)).
			Background(color(t.BgHighlight)).
			Bold(true),
		PickerSelectedDesc: lipgloss.NewStyle().
			Foreground(color(t.TextSecondary)).
			Background(color(t.BgHighlight)),
	}
}

var themeStyles = newStyles(GrokNight)