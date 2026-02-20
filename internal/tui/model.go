package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"valinor/internal/bus"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Layout constants
const sidebarWidth = 26

// Key bindings
var (
	keymap = struct {
		Submit key.Binding
		Quit   key.Binding
	}{
		Submit: key.NewBinding(key.WithKeys("enter")),
		Quit:   key.NewBinding(key.WithKeys("ctrl+c")),
	}
)

// appState tracks what the app is currently doing.
type appState int

const (
	stateReady   appState = iota // Waiting for user input
	stateWaiting                 // Waiting for agent response
)

// messageRole identifies who authored a chat message.
type messageRole int

const (
	roleUser messageRole = iota
	roleAssistant
)

// chatMessage is a single message in the conversation display.
type chatMessage struct {
	Role      messageRole
	Content   string     // Text content (may contain markdown for assistant)
	Rendered  string     // Cached rendered content (markdown applied when finalized)
	ToolCalls []toolCall // Tool calls made during this assistant turn
	Streaming bool       // True while content is still being received
	Error     string     // Error message if any
}

// toolCall represents a single tool invocation.
type toolCall struct {
	ID     string
	Name   string
	Args   string
	Result string
	Done   bool
}

// Usage holds token usage statistics for display.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Model is the top-level Bubble Tea model.
type Model struct {
	bus               *bus.Bus
	sub               *bus.Subscription
	modelName         string
	viewport          viewport.Model
	textinput         textinput.Model
	spinner           spinner.Model
	state             appState
	messages          []chatMessage
	totalUsage        Usage
	lastUsage         Usage
	width             int
	height            int
	contentWidth      int // width minus sidebar for viewport/chat
	ready             bool
	thinkingStartedAt time.Time
}

// New creates a new UI model.
func New(b *bus.Bus, modelName string) Model {
	ti := textinput.New()
	ti.Placeholder = "Send a message..."
	ti.Prompt = "> "
	ti.CharLimit = 0
	ti.SetSuggestions([]string{"/new", "/clear", "/exit"})
	ti.ShowSuggestions = true

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	sub := b.Subscribe(
		bus.EventAgentContentDelta,
		bus.EventAgentRefusal,
		bus.EventAgentToolCallStart,
		bus.EventAgentToolCallDone,
		bus.EventAgentDone,
		bus.EventAgentError,
	)

	return Model{
		bus:       b,
		sub:       sub,
		modelName: modelName,
		textinput: ti,
		spinner:   sp,
		state:     stateReady,
	}
}

// Init initializes the model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.textinput.Focus(),
		textinput.Blink,
		m.spinner.Tick,
		waitForBusEvent(m.sub),
	)
}

// Update handles incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if key.Matches(msg, keymap.Quit) {
			return m, tea.Quit
		}
		if m.state == stateReady {
			if key.Matches(msg, keymap.Submit) {
				input := strings.TrimSpace(m.textinput.Value())
				if input == "" {
					return m, tea.Batch(cmds...)
				}
				m.textinput.SetValue("")
				if input == "/exit" {
					return m, tea.Quit
				}
				if input == "/new" || input == "/clear" {
					m.bus.Publish(bus.Event{Type: bus.EventNewConversation})
					m.messages = nil
					m.rebuildViewportContent()
					return m, tea.Batch(cmds...)
				}

				m.messages = append(m.messages, chatMessage{Role: roleUser, Content: input})
				m.messages = append(m.messages, chatMessage{Role: roleAssistant, Streaming: true})
				m.state = stateWaiting
				m.thinkingStartedAt = time.Now()
				m.rebuildViewportContent()
				m.bus.Publish(bus.Event{
					Type:    bus.EventUserMessage,
					Payload: bus.UserMessagePayload{Content: input},
				})
				return m, tea.Batch(elapsedTickCmd())
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.contentWidth = msg.Width - sidebarWidth
		if m.contentWidth < 20 {
			m.contentWidth = 20
		}
		spinnerHeight := 1
		inputHeight := 5
		footerHeight := 2
		viewportHeight := msg.Height - spinnerHeight - inputHeight - footerHeight
		if viewportHeight < 1 {
			viewportHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(m.contentWidth, viewportHeight)
			m.viewport.MouseWheelEnabled = true
			m.viewport.MouseWheelDelta = 3
			m.ready = true
		} else {
			m.viewport.Width = m.contentWidth
			m.viewport.Height = viewportHeight
		}
		m.textinput.Width = m.contentWidth - 4
		m.rebuildViewportContent()

	case elapsedTickMsg:
		if m.state == stateWaiting {
			cmds = append(cmds, elapsedTickCmd())
		}

	case busEventMsg:
		ev := msg.Event
		if len(m.messages) == 0 {
			return m, nil
		}
		lastIdx := len(m.messages) - 1
		last := &m.messages[lastIdx]

		switch ev.Type {
		case bus.EventAgentContentDelta:
			p := ev.Payload.(bus.ContentDeltaPayload)
			last.Content += p.Content
			m.rebuildViewportContent()
			return m, tea.Batch(waitForBusEvent(m.sub), elapsedTickCmd())
		case bus.EventAgentRefusal:
			p := ev.Payload.(bus.RefusalPayload)
			last.Content += p.Content
			m.rebuildViewportContent()
			return m, tea.Batch(waitForBusEvent(m.sub), elapsedTickCmd())
		case bus.EventAgentToolCallStart:
			p := ev.Payload.(bus.ToolCallStartPayload)
			last.ToolCalls = append(last.ToolCalls, toolCall{
				ID:   p.ToolCallID,
				Name: p.ToolName,
				Args: p.ToolArgs,
			})
			m.rebuildViewportContent()
			return m, tea.Batch(waitForBusEvent(m.sub), elapsedTickCmd())
		case bus.EventAgentToolCallDone:
			p := ev.Payload.(bus.ToolCallDonePayload)
			for i := range last.ToolCalls {
				if last.ToolCalls[i].ID == p.ToolCallID && !last.ToolCalls[i].Done {
					last.ToolCalls[i].Result = p.ToolResult
					last.ToolCalls[i].Done = true
					break
				}
			}
			m.rebuildViewportContent()
			return m, tea.Batch(waitForBusEvent(m.sub), elapsedTickCmd())
		case bus.EventAgentDone:
			p := ev.Payload.(bus.AgentDonePayload)
			last.Streaming = false
			last.Rendered = renderMarkdown(last.Content, m.width-4)
			m.lastUsage = Usage{
				PromptTokens:     p.PromptTokens,
				CompletionTokens: p.CompletionTokens,
				TotalTokens:      p.TotalTokens,
			}
			m.totalUsage.PromptTokens += p.PromptTokens
			m.totalUsage.CompletionTokens += p.CompletionTokens
			m.totalUsage.TotalTokens += p.TotalTokens
			m.state = stateReady
			m.rebuildViewportContent()
			return m, waitForBusEvent(m.sub)
		case bus.EventAgentError:
			p := ev.Payload.(bus.AgentErrorPayload)
			last.Streaming = false
			last.Error = p.Error.Error()
			m.state = stateReady
			m.rebuildViewportContent()
			return m, waitForBusEvent(m.sub)
		}
		return m, nil

	case spinner.TickMsg:
		if m.state == stateWaiting {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Forward to viewport and textinput
	if m.ready {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.state == stateReady {
		if !m.textinput.Focused() {
			cmds = append(cmds, m.textinput.Focus())
		}
		m.textinput, cmd = m.textinput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// formatToolCall renders a tool call in compact reference style:
// → Read cmd/valinor/main.go  or  * Glob "*" (100 matches)
func formatToolCall(tc toolCall) string {
	var b strings.Builder
	b.WriteString(toolCallStyle.Render(""))

	// Extract displayable args from JSON
	var argsMap map[string]interface{}
	_ = json.Unmarshal([]byte(tc.Args), &argsMap)

	// Pick primary value for display (path, pattern, query, command, etc.)
	var primary string
	for _, k := range []string{"command", "path", "file", "filename", "pattern", "query", "pathname"} {
		if v, ok := argsMap[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				primary = s
				break
			}
		}
	}
	if primary == "" && tc.Args != "" && len(tc.Args) < 60 {
		primary = tc.Args
	}

	b.WriteString(toolCallStyle.Render("→ "))
	b.WriteString(toolNameStyle.Render(tc.Name))
	if primary != "" {
		b.WriteString(toolCallStyle.Render(" "))
		b.WriteString(pathStyle.Render(primary))
	}
	if tc.Done && tc.Result != "" {
		// Append short result hint e.g. "(100 matches)"
		if summary := summarizeToolResult(tc.Result); summary != "" {
			b.WriteString(toolCallStyle.Render(" " + summary))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func summarizeToolResult(result string) string {
	if len(result) > 200 {
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
	if width < 10 {
		width = 80
	}
	var b strings.Builder
	for _, msg := range m.messages {
		switch msg.Role {
		case roleUser:
			body := userPrefixStyle.Render("> ") + msg.Content
			block := userMessageBlockStyle.Width(width - 4).Render(body)
			b.WriteString(block)
			b.WriteString("\n")
		case roleAssistant:
			var body strings.Builder
			if msg.Error != "" {
				body.WriteString(assistantStyle.Render(""))
				body.WriteString(errorStyle.Render("Error: " + msg.Error))
			} else {
				for _, tc := range msg.ToolCalls {
					body.WriteString(formatToolCall(tc))
				}
				if msg.Streaming {
					body.WriteString(assistantStyle.Render(""))
					body.WriteString(msg.Content)
				} else if msg.Rendered != "" {
					body.WriteString(assistantStyle.Render(""))
					body.WriteString(msg.Rendered)
				}
			}
			block := assistantMessageBlockStyle.Width(width - 4).Render(body.String())
			b.WriteString(block)
			b.WriteString("\n")
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
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

	spinnerLine := ""
	if m.state == stateWaiting {
		elapsed := int(time.Since(m.thinkingStartedAt).Seconds())
		spinnerLine = spinnerStyle.Render(m.spinner.View()) + " Thinking... " + strconv.Itoa(elapsed) + "s\n"
	}

	inputBox := inputBorderStyle.Width(m.contentWidth - 2).Render(m.textinput.View())

	mainContent := lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().PaddingTop(1).Render(m.viewport.View()),
		spinnerLine,
		inputBox,
	)
	topRow := lipgloss.JoinHorizontal(lipgloss.Left, mainContent)

	modelRow := footerStyle.Width(m.width).Render(footerModelStyle.Render("Model: " + m.modelName))
	tokensText := tokensLabelStyle.Render("Context: ") +
		tokensValueStyle.Render(formatTokens(m.totalUsage.TotalTokens))
	tokensRow := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(lipgloss.Color("238")).
			Foreground(lipgloss.Color("241")).
			Padding(0, 1).
			Width(m.width).
			Render(tokensText)

	footer := lipgloss.JoinVertical(lipgloss.Left, modelRow, tokensRow)
	return lipgloss.JoinVertical(lipgloss.Left, topRow, footer)
}
