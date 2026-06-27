package wizard

import "github.com/charmbracelet/lipgloss"

const accentChar = "❙"

type theme struct {
	bgBase        string
	bgLight       string
	bgHighlight   string
	textPrimary   string
	textSecondary string
	grayDim       string
	accent        string
	accentTool    string
	accentSuccess string
	accentError   string
	border        string
	borderActive  string
}

var grokNight = theme{
	bgBase:        "#0a0a0a",
	bgLight:       "#1e1e22",
	bgHighlight:   "#323237",
	textPrimary:   "#c8c8c8",
	textSecondary: "#787878",
	grayDim:       "#6c6c6c",
	accent:        "#bb9af7",
	accentTool:    "#7aa2f7",
	accentSuccess: "#9ece6a",
	accentError:   "#f7768e",
	border:        "#3a3a42",
	borderActive:  "#bb9af7",
}

type styles struct {
	title               lipgloss.Style
	label               lipgloss.Style
	hint                lipgloss.Style
	error               lipgloss.Style
	success             lipgloss.Style
	footerBar           lipgloss.Style
	footerKey           lipgloss.Style
	inputAccent         lipgloss.Style
	inputAccentIdle     lipgloss.Style
	inputBorder         lipgloss.Style
	inputBorderActive   lipgloss.Style
	inputContainer      lipgloss.Style
	pickerTitle         lipgloss.Style
	pickerNormalTitle   lipgloss.Style
	pickerNormalDesc    lipgloss.Style
	pickerSelectedTitle lipgloss.Style
	pickerSelectedDesc  lipgloss.Style
	panel               lipgloss.Style
	panelTitle          lipgloss.Style
	boolOn              lipgloss.Style
	boolOff             lipgloss.Style
	emptyIcon           lipgloss.Style
	emptyTitle          lipgloss.Style
	emptyBody           lipgloss.Style
	keyBadge            lipgloss.Style
	sidebar             lipgloss.Style
	sidebarActive       lipgloss.Style
	sidebarDone         lipgloss.Style
	sidebarConnector    lipgloss.Style
	sidebarTitle        lipgloss.Style
	value               lipgloss.Style
	dropdownMenu        lipgloss.Style
}

func newStyles(t theme) styles {
	c := func(hex string) lipgloss.Color { return lipgloss.Color(hex) }
	return styles{
		title: lipgloss.NewStyle().Foreground(c(t.accent)).Bold(true),
		label: lipgloss.NewStyle().Foreground(c(t.accentTool)).Bold(true),
		hint: lipgloss.NewStyle().Foreground(c(t.grayDim)),
		error: lipgloss.NewStyle().Foreground(c(t.accentError)).Bold(true),
		success: lipgloss.NewStyle().Foreground(c(t.accentSuccess)).Bold(true),
		footerBar: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(c(t.border)).
			Padding(0, 2),
		footerKey: lipgloss.NewStyle().
			Foreground(c(t.accent)).
			Background(c(t.bgHighlight)).
			Padding(0, 1),
		inputAccent: lipgloss.NewStyle().Foreground(c(t.accent)),
		inputAccentIdle: lipgloss.NewStyle().Foreground(c(t.border)),
		inputBorder: lipgloss.NewStyle().
			Background(c(t.bgLight)).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(c(t.border)).
			Padding(0, 1),
		inputBorderActive: lipgloss.NewStyle().
			Background(c(t.bgLight)).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(c(t.borderActive)).
			Padding(0, 1),
		inputContainer: lipgloss.NewStyle().Margin(0, 0, 1, 0),
		pickerTitle: lipgloss.NewStyle().Foreground(c(t.textPrimary)).Bold(true),
		pickerNormalTitle: lipgloss.NewStyle().Foreground(c(t.textPrimary)),
		pickerNormalDesc: lipgloss.NewStyle().Foreground(c(t.textSecondary)),
		pickerSelectedTitle: lipgloss.NewStyle().
			Foreground(c(t.accent)).
			Background(c(t.bgHighlight)).
			Bold(true),
		pickerSelectedDesc: lipgloss.NewStyle().
			Foreground(c(t.textSecondary)).
			Background(c(t.bgHighlight)),
		panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(t.border)).
			Background(c(t.bgLight)).
			Padding(1, 2),
		panelTitle: lipgloss.NewStyle().
			Foreground(c(t.textPrimary)).
			Bold(true),
		boolOn: lipgloss.NewStyle().Foreground(c(t.accentSuccess)).Bold(true),
		boolOff: lipgloss.NewStyle().Foreground(c(t.grayDim)),
		emptyIcon: lipgloss.NewStyle().Foreground(c(t.accent)).Bold(true),
		emptyTitle: lipgloss.NewStyle().Foreground(c(t.textPrimary)).Bold(true),
		emptyBody: lipgloss.NewStyle().Foreground(c(t.textSecondary)),
		keyBadge: lipgloss.NewStyle().
			Foreground(c(t.accent)).
			Background(c(t.bgHighlight)).
			Padding(0, 1).
			Bold(true),
		sidebar: lipgloss.NewStyle().
			Foreground(c(t.textSecondary)).
			Padding(0, 1),
		sidebarActive: lipgloss.NewStyle().
			Foreground(c(t.accent)).
			Background(c(t.bgHighlight)).
			Bold(true).
			Padding(0, 1),
		sidebarDone: lipgloss.NewStyle().
			Foreground(c(t.accentSuccess)).
			Padding(0, 1),
		sidebarConnector: lipgloss.NewStyle().
			Foreground(c(t.border)).
			Padding(0, 1),
		sidebarTitle: lipgloss.NewStyle().
			Foreground(c(t.accent)).
			Bold(true).
			Padding(1, 2),
		value: lipgloss.NewStyle().
			Foreground(c(t.textPrimary)),
		dropdownMenu: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(t.border)).
			Background(c(t.bgHighlight)).
			Padding(0, 1).
			Margin(0, 0, 1, 2),
	}
}

func applyBackground(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return content
	}
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		Background(lipgloss.Color(grokNight.bgBase)).
		Render(content)
}