package ui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"valinor/internal/agent"
)

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
	Role          messageRole
	Content       string     // Text content (may contain markdown for assistant)
	Rendered      string     // Cached rendered content (markdown applied when finalized)
	ToolCalls     []toolCall // Tool calls made during this assistant turn
	Streaming     bool       // True while content is still being received
	Error         string     // Error message if any
}

// toolCall represents a single tool invocation.
type toolCall struct {
	ID     string
	Name   string
	Args   string
	Result string
	Done   bool
}

// Model is the top-level Bubble Tea model.
type Model struct {
	agent      *agent.Agent
	modelName  string
	viewport   viewport.Model
	textarea   textarea.Model
	spinner    spinner.Model
	state      appState
	messages   []chatMessage
	eventCh    chan agent.Event
	totalUsage        agent.Usage
	lastUsage         agent.Usage
	width             int
	height            int
	ready             bool
	thinkingStartedAt time.Time
}

// New creates a new UI model.
func New(ag *agent.Agent, modelName string) Model {
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.Focus()
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.Prompt = "> "

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return Model{
		agent:     ag,
		modelName: modelName,
		textarea:  ta,
		spinner:   sp,
		state:     stateReady,
		eventCh:   make(chan agent.Event, 64),
	}
}

// Init initializes the model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
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
				input := strings.TrimSpace(m.textarea.Value())
				if input == "" {
					return m, tea.Batch(cmds...)
				}
				m.textarea.SetValue("")
				if input == "/exit" {
					return m, tea.Quit
				}
				if input == "/new" || input == "/clear" {
					m.agent.NewConversation()
					m.messages = nil
					m.rebuildViewportContent()
					return m, tea.Batch(cmds...)
				}

				m.messages = append(m.messages, chatMessage{Role: roleUser, Content: input})
				m.messages = append(m.messages, chatMessage{Role: roleAssistant, Streaming: true})
				m.state = stateWaiting
				m.thinkingStartedAt = time.Now()
				m.rebuildViewportContent()
				return m, tea.Batch(startAgentCmd(m.agent, input, m.eventCh), elapsedTickCmd())
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerHeight := 1
		spinnerHeight := 1
		inputHeight := 5
		statusHeight := 1
		viewportHeight := msg.Height - headerHeight - spinnerHeight - inputHeight - statusHeight
		if viewportHeight < 1 {
			viewportHeight = 1
		}

		if !m.ready {
			m.viewport = viewport.New(msg.Width, viewportHeight)
			m.viewport.MouseWheelEnabled = true
			m.viewport.MouseWheelDelta = 3
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = viewportHeight
		}
		m.textarea.SetWidth(msg.Width - 4)
		m.rebuildViewportContent()

	case elapsedTickMsg:
		if m.state == stateWaiting {
			cmds = append(cmds, elapsedTickCmd())
		}

	case agentEventMsg:
		ev := msg.Event
		if len(m.messages) == 0 {
			return m, nil
		}
		lastIdx := len(m.messages) - 1
		last := &m.messages[lastIdx]

		switch ev.Type {
		case agent.EventContentDelta:
			last.Content += ev.Content
			m.rebuildViewportContent()
			return m, tea.Batch(waitForAgentEvent(m.eventCh), elapsedTickCmd())
		case agent.EventRefusal:
			last.Content += ev.Content
			m.rebuildViewportContent()
			return m, tea.Batch(waitForAgentEvent(m.eventCh), elapsedTickCmd())
		case agent.EventToolCallStart:
			last.ToolCalls = append(last.ToolCalls, toolCall{
				ID:   ev.ToolCallID,
				Name: ev.ToolName,
				Args: ev.ToolArgs,
			})
			m.rebuildViewportContent()
			return m, tea.Batch(waitForAgentEvent(m.eventCh), elapsedTickCmd())
		case agent.EventToolCallDone:
			for i := range last.ToolCalls {
				if last.ToolCalls[i].ID == ev.ToolCallID && !last.ToolCalls[i].Done {
					last.ToolCalls[i].Result = ev.ToolResult
					last.ToolCalls[i].Done = true
					break
				}
			}
			m.rebuildViewportContent()
			return m, tea.Batch(waitForAgentEvent(m.eventCh), elapsedTickCmd())
		case agent.EventDone:
			last.Streaming = false
			last.Rendered = renderMarkdown(last.Content, m.width-4)
			m.lastUsage = ev.Usage
			m.totalUsage.PromptTokens += ev.Usage.PromptTokens
			m.totalUsage.CompletionTokens += ev.Usage.CompletionTokens
			m.totalUsage.TotalTokens += ev.Usage.TotalTokens
			m.state = stateReady
			m.rebuildViewportContent()
			return m, nil
		case agent.EventError:
			last.Streaming = false
			last.Error = ev.Error.Error()
			m.state = stateReady
			m.rebuildViewportContent()
			return m, nil
		}
		return m, nil

	case spinner.TickMsg:
		if m.state == stateWaiting {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	// Forward to viewport and textarea
	if m.ready {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}
	if m.state == stateReady {
		m.textarea, cmd = m.textarea.Update(msg)
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

	// Pick primary value for display (path, pattern, query, etc.)
	var primary string
	for _, k := range []string{"path", "file", "filename", "pattern", "query", "pathname"} {
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
	var b strings.Builder
	width := m.width
	if width < 10 {
		width = 80
	}
	for _, msg := range m.messages {
		switch msg.Role {
		case roleUser:
			b.WriteString(userPrefixStyle.Render("> "))
			b.WriteString(msg.Content)
			b.WriteString("\n\n")
		case roleAssistant:
			if msg.Error != "" {
				b.WriteString(assistantStyle.Render(""))
				b.WriteString(errorStyle.Render("Error: " + msg.Error))
				b.WriteString("\n\n")
				continue
			}
			// Tool calls in compact format (→ Read path, * Glob pattern)
			for _, tc := range msg.ToolCalls {
				b.WriteString(formatToolCall(tc))
			}
			if msg.Streaming {
				b.WriteString(assistantStyle.Render(""))
				b.WriteString(msg.Content)
				b.WriteString("\n\n")
			} else if msg.Rendered != "" {
				b.WriteString(assistantStyle.Render(""))
				b.WriteString(msg.Rendered)
				b.WriteString("\n\n")
			}
		}
	}
	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
}

// View renders the UI.
func (m Model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	headerText := "λ Build with: " + m.modelName
	if m.totalUsage.TotalTokens > 0 {
		headerText += " · " + strconv.FormatInt(m.totalUsage.TotalTokens, 10) + " tokens"
	}
	if m.lastUsage.TotalTokens > 0 {
		headerText += " · last: " + strconv.FormatInt(m.lastUsage.TotalTokens, 10)
	}
	header := headerStyle.Width(m.width).Render(headerText)

	spinnerLine := ""
	if m.state == stateWaiting {
		elapsed := int(time.Since(m.thinkingStartedAt).Seconds())
		spinnerLine = spinnerStyle.Render(m.spinner.View()) + " Thinking... " + strconv.Itoa(elapsed) + "s\n"
	}

	inputBox := inputBorderStyle.Render(m.textarea.View())
	statusBar := statusBarStyle.Render("/new  /clear  /exit  Enter=send")

	// Layout: header, conversation viewport, then input sticky at bottom
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.viewport.View(),
		spinnerLine,
		inputBox,
		statusBar,
	)
}
