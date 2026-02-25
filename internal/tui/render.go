package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Display constants for tool call formatting.
const (
	maxToolArgsInlineLen    = 60  // max length for inline tool args display
	maxToolResultDisplayLen = 200 // threshold for truncating tool result display
)

// Viewport layout constants.
const (
	minContentWidth = 10 // minimum content width before falling back to default
	defaultWidth    = 80 // default width when content width is too small
)

// rendererCache stores glamour.TermRenderer instances keyed by width.
// This avoids the expensive allocation of creating a new renderer on every call.
var rendererCache sync.Map // map[int]*glamour.TermRenderer

// renderMarkdown converts a markdown string to styled ANSI terminal output.
// Uses glamour with "dark" style and word-wrapping to the given width.
// The renderer is cached per width to avoid repeated allocations.
func renderMarkdown(content string, width int) string {
	if content == "" {
		return ""
	}

	r, ok := rendererCache.Load(width)
	if !ok {
		newR, err := glamour.NewTermRenderer(
			glamour.WithStylePath("dark"),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return content // fallback to raw text
		}
		r, _ = rendererCache.LoadOrStore(width, newR)
	}

	renderer := r.(*glamour.TermRenderer)
	out, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return out
}

// formatToolCall renders a tool call in compact reference style:
// → Read cmd/valinor/main.go  or  * Glob "*" (100 matches)
func formatToolCall(tc toolCall) string {
	var b strings.Builder
	b.WriteString(toolCallStyle.Render(""))
	primary := extractPrimaryToolArg(tc)

	b.WriteString(toolCallStyle.Render("→ "))
	b.WriteString(toolNameStyle.Render(tc.Name))
	if primary != "" {
		b.WriteString(toolCallStyle.Render(" "))
		b.WriteString(pathStyle.Render(primary))
	}
	if tc.Done && tc.Result != "" {
		if summary := formatToolResultSummary(tc.Result); summary != "" {
			b.WriteString(toolCallStyle.Render(" " + summary))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func extractPrimaryToolArg(tc toolCall) string {
	var argsMap map[string]interface{}
	_ = json.Unmarshal([]byte(tc.Args), &argsMap)

	for _, key := range []string{"command", "path", "file", "filename", "pattern", "query", "pathname"} {
		value, ok := argsMap[key]
		if !ok {
			continue
		}
		asString, ok := value.(string)
		if ok && asString != "" {
			return asString
		}
	}

	if tc.Args != "" && len(tc.Args) < maxToolArgsInlineLen {
		return tc.Args
	}
	return ""
}

func formatToolResultSummary(result string) string {
	if len(result) > maxToolResultDisplayLen {
		return "(" + strconv.Itoa(len(result)) + " chars)"
	}
	if strings.Contains(result, "\n") {
		lines := strings.Split(result, "\n")
		return "(" + strconv.Itoa(len(lines)) + " lines)"
	}
	return ""
}

// rebuildViewportContent rebuilds the viewport content from messages.
func (m *Model) rebuildViewportContent() {
	if !m.ready {
		return
	}
	width := m.contentWidth
	if width < minContentWidth {
		width = defaultWidth
	}
	var b strings.Builder
	for _, msg := range m.messages {
		switch msg.Role {
		case roleUser:
			b.WriteString(renderUserMessageBlock(msg, width))
			b.WriteString("\n")
		case roleAssistant:
			b.WriteString(renderAssistantMessageBlock(msg, width))
			b.WriteString("\n")
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

// appendStreamingContent efficiently updates the viewport during streaming.
// Instead of rebuilding all messages, it re-renders only the last streaming message.
func (m *Model) appendStreamingContent() {
	if !m.ready {
		return
	}

	width := m.contentWidth
	if width < minContentWidth {
		width = defaultWidth
	}

	// Find the content up to (but not including) the last message
	// We'll rebuild just the last assistant message with the new content
	var previousContent string
	if len(m.messages) > 1 {
		var b strings.Builder
		for i := 0; i < len(m.messages)-1; i++ {
			msg := m.messages[i]
			switch msg.Role {
			case roleUser:
				b.WriteString(renderUserMessageBlock(msg, width))
				b.WriteString("\n")
			case roleAssistant:
				b.WriteString(renderAssistantMessageBlock(msg, width))
				b.WriteString("\n")
			}
		}
		previousContent = b.String()
	}

	// Render the last message (which should be the streaming assistant message)
	if last, ok := m.lastMessage(); ok {
		var b strings.Builder
		if previousContent != "" {
			b.WriteString(previousContent)
		}
		b.WriteString(renderAssistantMessageBlock(*last, width))
		b.WriteString("\n")
		m.viewport.SetContent(b.String())
	}
	m.viewport.GotoBottom()
}

func renderUserMessageBlock(msg chatMessage, width int) string {
	contentStyle := lipgloss.NewStyle().Background(lipgloss.Color("235"))
	body := userPrefixStyle.Render("> ") + contentStyle.Render(msg.Content)
	return userMessageBlockStyle.Width(width - 4).Render(body)
}

func renderAssistantMessageBlock(msg chatMessage, width int) string {
	return assistantMessageBlockStyle.Width(width - 4).Render(renderAssistantBody(msg))
}

func renderAssistantBody(msg chatMessage) string {
	var body strings.Builder
	if msg.Error != "" {
		body.WriteString(assistantStyle.Render(""))
		body.WriteString(errorStyle.Render("Error: " + msg.Error))
		return body.String()
	}

	for _, tc := range msg.ToolCalls {
		body.WriteString(formatToolCall(tc))
	}

	body.WriteString(assistantStyle.Render(""))
	if msg.Streaming {
		body.WriteString(msg.Content)
	} else if msg.Rendered != "" {
		body.WriteString(msg.Rendered)
	}

	return body.String()
}

// formatTokens formats n with thousands separators for display.
func formatTokens(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(",")
		}
		b.WriteRune(c)
	}
	return b.String()
}

// View renders the UI.
func (m Model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	if m.state == stateModelPicker {
		helpText := "↑/↓ select • enter choose • esc cancel"
		if m.modelPickerState == pickerSelectModel {
			helpText = "type to search • enter choose • esc back"
			if m.modelPickerBusy {
				helpText = "loading models... • esc cancel"
			}
		}
		if m.modelPickerErr != "" {
			helpText += " • using fallback list"
		}
		help := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(helpText)
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Padding(1, 2).Render(m.modelPicker.View()),
			help,
		)
	}

	spinnerLine := ""
	if m.state == stateWaiting {
		elapsed := int(time.Since(m.thinkingStartedAt).Seconds())
		spinnerLine = spinnerStyle.Render(m.spinner.View()) + " Thinking... " + strconv.Itoa(elapsed) + "s"
		cancelHint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(" (esc to cancel)")
		spinnerLine = spinnerLine + cancelHint + "\n"
	}

	inputBox := inputBorderStyle.Width(m.contentWidth - 2).Render(m.textinput.View())

	mainContent := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().PaddingTop(1).Render(m.viewport.View()),
		spinnerLine,
		inputBox,
	)
	topRow := lipgloss.JoinHorizontal(lipgloss.Left, mainContent)

	providerModel := m.modelName
	if m.providerName != "" {
		providerModel = m.providerName + " / " + m.modelName
	}
	sessionLabel := m.sessionKey
	if strings.TrimSpace(sessionLabel) == "" {
		sessionLabel = "(not attached)"
	}
	gatewayLabel := "Gateway: connected"
	gatewayStyle := gatewayConnectedStyle
	if !m.gatewayConnected {
		gatewayLabel = "Gateway: disconnected"
		gatewayStyle = gatewayDisconnectedStyle
		if status := strings.TrimSpace(m.gatewayStatus); status != "" {
			gatewayLabel += " (" + status + ")"
		}
	}
	gatewayRow := footerStyle.Width(m.width).Render(gatewayStyle.Render(gatewayLabel))
	sessionRow := footerStyle.Width(m.width).Render(footerModelStyle.Render("Session: "+sessionLabel))
	modelRow := footerStyle.Width(m.width).Render(footerModelStyle.Render("Model: "+providerModel) + " (ctrl+m)")
	contextLabel := "Context: "
	if m.contextEstimated {
		contextLabel = "Context (~): "
	}
	tokensText := tokensLabelStyle.Render(contextLabel) +
		tokensValueStyle.Render(formatTokens(m.contextPrompt))
	tokensRow := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(lipgloss.Color("238")).
		Foreground(lipgloss.Color("241")).
		Padding(0, 1).
		Width(m.width).
		Render(tokensText)

	footer := lipgloss.JoinVertical(lipgloss.Left, gatewayRow, sessionRow, modelRow, tokensRow)
	return lipgloss.JoinVertical(lipgloss.Left, topRow, footer)
}
