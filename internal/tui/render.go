package tui

import (
	"encoding/json"
	"strconv"
	"strings"
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

// renderMarkdown converts a markdown string to styled ANSI terminal output.
// Uses the GrokNight glamour style and word-wrapping to the given width.
// The renderer is cached per width to avoid repeated allocations.
func renderMarkdown(content string, width int) string {
	if content == "" {
		return ""
	}

	r, ok := markdownRendererCache.Load(width)
	if !ok {
		newR, err := newMarkdownRenderer(width)
		if err != nil {
			return content // fallback to raw text
		}
		r, _ = markdownRendererCache.LoadOrStore(width, newR)
	}

	renderer := r.(*glamour.TermRenderer)
	out, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return out
}

// formatToolCall renders a tool call in compact reference style:
// ◆ Read cmd/valinor/main.go  or  ◆ Glob "*" (100 matches)
func formatToolCall(tc toolCall) string {
	var b strings.Builder
	primary := extractPrimaryToolArg(tc)

	b.WriteString(themeStyles.ToolCall.Render("◆ "))
	b.WriteString(themeStyles.ToolName.Render(tc.Name))
	if primary != "" {
		b.WriteString(themeStyles.ToolCall.Render(" "))
		b.WriteString(themeStyles.ToolPath.Render(primary))
	}
	if tc.Done && tc.Result != "" {
		if summary := formatToolResultSummary(tc.Result); summary != "" {
			b.WriteString(themeStyles.ToolCall.Render(" " + summary))
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
		case roleSystem:
			b.WriteString(renderSystemMessageBlock(msg, width))
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
			case roleSystem:
				b.WriteString(renderSystemMessageBlock(msg, width))
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
	body := themeStyles.UserPrefix.Render("> ") + msg.Content
	content := lipgloss.JoinHorizontal(lipgloss.Top,
		renderAccentLine(true),
		" ",
		themeStyles.UserMessageBody.Render(body),
	)
	return themeStyles.UserMessageBlock.Width(width - 2).Render(content)
}

func renderAssistantMessageBlock(msg chatMessage, width int) string {
	return themeStyles.AssistantMessageBlock.Width(width - 2).Render(renderAssistantBody(msg))
}

func renderSystemMessageBlock(msg chatMessage, width int) string {
	body := msg.Content
	if msg.Rendered != "" {
		body = msg.Rendered
	}
	return themeStyles.AssistantMessageBlock.Width(width - 2).Render(themeStyles.SystemMessage.Render(body))
}

func renderAssistantBody(msg chatMessage) string {
	var body strings.Builder
	if msg.Error != "" {
		body.WriteString(themeStyles.Error.Render("Error: " + msg.Error))
		return body.String()
	}

	for _, tc := range msg.ToolCalls {
		body.WriteString(formatToolCall(tc))
	}

	if msg.Streaming {
		body.WriteString(themeStyles.Assistant.Render(msg.Content))
	} else if msg.Rendered != "" {
		body.WriteString(msg.Rendered)
	} else if msg.Content != "" {
		body.WriteString(themeStyles.Assistant.Render(msg.Content))
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

func (m Model) renderInputBox() string {
	inputStyle := themeStyles.InputBorder
	accentStyle := themeStyles.InputAccentInactive
	if m.textinput.Focused() {
		inputStyle = themeStyles.InputBorderActive
		accentStyle = themeStyles.InputAccent
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top,
		accentStyle.Render(accentChar),
		" ",
		m.textinput.View(),
	)
	return themeStyles.InputContainer.
		Width(m.contentWidth).
		Render(inputStyle.Width(m.contentWidth - 2).Render(row))
}

func (m Model) renderStatusBar() string {
	gatewayDot := themeStyles.GatewayConnected.Render("●")
	gatewayLabel := "connected"
	if !m.gatewayConnected {
		gatewayDot = themeStyles.GatewayDisconnected.Render("●")
		gatewayLabel = "disconnected"
		if status := strings.TrimSpace(m.gatewayStatus); status != "" {
			gatewayLabel += " (" + truncateMiddle(status, 24) + ")"
		}
	}

	providerModel := m.modelName
	if m.providerName != "" {
		providerModel = m.providerName + " / " + m.modelName
	}
	if strings.TrimSpace(providerModel) == "" {
		providerModel = "no model"
	}

	contextLabel := "ctx "
	if m.contextEstimated {
		contextLabel = "ctx ~ "
	}

	var parts []string
	parts = append(parts, gatewayDot+" "+themeStyles.FooterStrong.Render(gatewayLabel))
	parts = append(parts, themeStyles.FooterStrong.Render(truncateMiddle(providerModel, 40)))
	parts = append(parts, themeStyles.TokensLabel.Render(contextLabel)+themeStyles.TokensValue.Render(formatTokens(m.contextPrompt)))
	parts = append(parts, themeStyles.Hint.Render("ctrl+m"))

	status := parts[0]
	for i := 1; i < len(parts); i++ {
		status += renderSeparator() + parts[i]
	}
	return themeStyles.Footer.Width(m.width).Render(status)
}

// View renders the UI.
func (m Model) View() string {
	if !m.ready {
		return applyBackground(themeStyles.Hint.Render("\n  Initializing..."), m.width, m.height)
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
		content := lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Padding(1, 2).Render(m.modelPicker.View()),
			themeStyles.Hint.Render(helpText),
		)
		return applyBackground(content, m.width, m.height)
	}

	spinnerLine := ""
	if m.state == stateWaiting {
		elapsed := int(time.Since(m.thinkingStartedAt).Seconds())
		spinnerLine = lipgloss.JoinHorizontal(lipgloss.Top,
			renderAccentLine(true),
			" ",
			themeStyles.Spinner.Render(m.spinner.View())+
				themeStyles.Thinking.Render(" Thinking... "+strconv.Itoa(elapsed)+"s")+
				themeStyles.Hint.Render(" (esc to cancel)"),
		) + "\n"
	}

	mainContent := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().PaddingTop(1).Render(m.viewport.View()),
		spinnerLine,
		m.renderInputBox(),
		m.renderStatusBar(),
	)
	return applyBackground(mainContent, m.width, m.height)
}
