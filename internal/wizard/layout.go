package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

const (
	sidebarWidth = 22
	maxPanelWidth = 64
	footerBarH    = 2
	headerH       = 3
)

func (m *Model) contentAreaWidth() int {
	w := m.width - sidebarWidth - 4
	if w < 40 {
		return 40
	}
	return w
}

func (m *Model) panelWidth() int {
	w := m.contentAreaWidth() - 6
	if w > maxPanelWidth {
		return maxPanelWidth
	}
	if w < 40 {
		return 40
	}
	return w
}

func (m *Model) contentHeight() int {
	h := m.height - headerH - footerBarH - 2
	if h < 10 {
		return 10
	}
	return h
}

func (m *Model) renderScreen() string {
	topBar := m.renderTopBar()
	main := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderSidebar(),
		m.renderContentColumn(),
	)
	errLine := m.renderError()
	footer := m.renderFooter()

	mid := lipgloss.JoinVertical(lipgloss.Left, topBar, main, errLine)
	remaining := m.height - lipgloss.Height(mid) - footerBarH
	if remaining < 0 {
		remaining = 0
	}
	spacer := lipgloss.NewStyle().Height(remaining).Render("")

	return lipgloss.JoinVertical(lipgloss.Left, mid, spacer, footer)
}

func (m *Model) renderTopBar() string {
	return m.styles.sidebarTitle.Width(m.width).Render("Valinor Setup") + "\n"
}

func (m *Model) renderSidebar() string {
	current := m.progressIndex()
	var lines []string
	for i, ws := range wizardSteps {
		num := fmt.Sprintf("%d", i+1)
		label := ws.label
		var line string
		switch {
		case i < current:
			line = m.styles.sidebarDone.Render("✓ " + num + "  " + label)
		case i == current:
			line = m.styles.sidebarActive.Render("◉ " + num + "  " + label)
		default:
			line = m.styles.sidebar.Render("○ " + num + "  " + label)
		}
		lines = append(lines, line)
		if i < len(wizardSteps)-1 {
			lines = append(lines, m.styles.sidebarConnector.Render("  │"))
		}
	}
	body := strings.Join(lines, "\n")
	return m.styles.panel.
		Width(sidebarWidth).
		Height(m.contentHeight()).
		Render(body)
}

func (m *Model) renderContentColumn() string {
	panel := m.renderPanel()
	return lipgloss.Place(m.contentAreaWidth(), m.contentHeight(), lipgloss.Center, lipgloss.Center, panel)
}

func (m *Model) progressIndex() int {
	switch m.step {
	case stepProviders, stepProviderForm:
		return 0
	case stepDirectories:
		return 1
	case stepNetworking:
		return 2
	case stepAgent:
		return 3
	case stepTelegram:
		return 4
	case stepScheduler:
		return 5
	case stepSandbox:
		return 6
	case stepReview, stepDone:
		return 7
	default:
		return 0
	}
}

func (m *Model) renderPanel() string {
	w := m.panelWidth()
	inner := m.renderPanelContent()
	title := m.panelTitle()
	header := m.styles.panelTitle.Render(title)
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", inner)
	return m.styles.panel.Width(w).Render(body)
}

func (m *Model) panelTitle() string {
	switch m.step {
	case stepProviders:
		return "LLM Providers"
	case stepProviderForm:
		if m.providerForm != nil && m.providerForm.editingName != "" {
			return "Edit Provider"
		}
		return "Add Provider"
	case stepDirectories:
		return "Directories"
	case stepNetworking:
		return "Gateway & Web"
	case stepAgent:
		return "Agent & Context"
	case stepTelegram:
		return "Telegram Bot"
	case stepScheduler:
		return "Scheduled Jobs"
	case stepSandbox:
		return "Sandbox Runtime"
	case stepReview:
		return "Review Configuration"
	default:
		return ""
	}
}

func (m *Model) renderPanelContent() string {
	switch m.step {
	case stepProviders:
		if len(m.cfg.Providers) == 0 {
			return m.renderEmptyProviders()
		}
		return m.providerList.View()
	case stepProviderForm:
		if m.providerForm != nil {
			return m.providerForm.render(m.styles)
		}
	case stepReview:
		return m.review.View()
	default:
		return m.form.render(m.styles)
	}
	return ""
}

func (m *Model) renderEmptyProviders() string {
	keyA := m.styles.keyBadge.Render(" a ")
	innerW := m.panelWidth() - 6
	lines := []string{
		"",
		lipgloss.Place(innerW, 1, lipgloss.Center, lipgloss.Top, m.styles.emptyIcon.Render("◇")),
		"",
		lipgloss.Place(innerW, 1, lipgloss.Center, lipgloss.Top, m.styles.emptyTitle.Render("No providers yet")),
		"",
		lipgloss.Place(innerW, 1, lipgloss.Center, lipgloss.Top,
			m.styles.emptyBody.Render("Add OpenAI, Anthropic, Gemini,")),
		lipgloss.Place(innerW, 1, lipgloss.Center, lipgloss.Top,
			m.styles.emptyBody.Render("or compatible APIs.")),
		"",
		lipgloss.Place(innerW, 1, lipgloss.Center, lipgloss.Top,
			lipgloss.JoinHorizontal(lipgloss.Center,
				m.styles.hint.Render("Press"),
				keyA,
				m.styles.hint.Render("to add"),
			)),
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderFooter() string {
	parts := m.footerParts()
	if len(parts) == 0 {
		return m.styles.footerBar.Width(m.width).Render("")
	}
	line := strings.Join(parts, m.styles.hint.Render("  ·  "))
	return m.styles.footerBar.Width(m.width).Render(
		lipgloss.Place(m.width-4, 1, lipgloss.Left, lipgloss.Top, "  "+line),
	)
}

func (m *Model) footerParts() []string {
	switch m.step {
	case stepProviders:
		return footerKeys(m.styles,
			[2]string{"a", "add"},
			[2]string{"e", "edit"},
			[2]string{"d", "delete"},
			[2]string{"s", "default"},
			[2]string{"enter", "continue"},
		)
	case stepProviderForm:
		if m.providerForm != nil && m.providerForm.typeDropdownOpen() {
			return footerKeys(m.styles,
				[2]string{"↑↓", "navigate"},
				[2]string{"enter", "select"},
				[2]string{"esc", "close"},
			)
		}
		if m.providerForm != nil && m.providerForm.focus == pfType {
			return footerKeys(m.styles,
				[2]string{"enter", "open menu"},
				[2]string{"tab", "next field"},
				[2]string{"esc", "back"},
			)
		}
		return footerKeys(m.styles,
			[2]string{"tab", "next field"},
			[2]string{"enter", "next/save"},
			[2]string{"esc", "back"},
		)
	case stepReview:
		return footerKeys(m.styles,
			[2]string{"enter", "save"},
			[2]string{"esc", "back"},
		)
	default:
		return footerKeys(m.styles,
			[2]string{"tab", "next field"},
			[2]string{"enter", "next field"},
			[2]string{"space", "toggle"},
			[2]string{"ctrl+n", "next step"},
			[2]string{"esc", "back"},
		)
	}
}

func footerKeys(s styles, pairs ...[2]string) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, s.footerKey.Render(p[0])+" "+s.hint.Render(p[1]))
	}
	out = append(out, s.hint.Render("ctrl+c cancel"))
	return out
}

func (m *Model) renderError() string {
	if m.errMsg == "" {
		return ""
	}
	return "  " + m.styles.error.Render(m.errMsg) + "\n"
}

func renderInputBox(s styles, ti textinput.Model, focused bool, width int) string {
	border := s.inputBorder
	accent := s.inputAccentIdle
	if focused {
		border = s.inputBorderActive
		accent = s.inputAccent
	}
	ti.Width = width - 8
	row := lipgloss.JoinHorizontal(lipgloss.Top,
		accent.Render(accentChar),
		" ",
		ti.View(),
	)
	return s.inputContainer.Render(border.Width(width - 4).Render(row))
}

func renderBoolField(s styles, label string, value bool, focused bool) string {
	state := s.boolOff.Render("off")
	if value {
		state = s.boolOn.Render("on")
	}
	marker := " "
	if focused {
		marker = s.inputAccent.Render(accentChar)
	}
	hint := s.hint.Render("space")
	return fmt.Sprintf("%s %s %s  %s", marker, s.label.Render(label+":"), state, hint)
}