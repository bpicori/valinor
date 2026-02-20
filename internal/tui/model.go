package tui

import (
	"strings"
	"time"

	"valinor/internal/bus"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
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

	return Model{
		bus:       b,
		modelName: modelName,
		textinput: ti,
		spinner:   sp,
		state:     stateReady,
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.textinput.Focus(),
		textinput.Blink,
	)
}

// Update handles incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch typed := msg.(type) {
	// case for keypresses
	case tea.KeyMsg:
		updatedModel, cmd, handled, shouldQuit := m.handleKeyMsg(typed)
		if shouldQuit {
			return updatedModel, tea.Quit
		}
		if handled { // if handled stop processing further messages
			return updatedModel, cmd
		}
		m = updatedModel
	// case for window size changes
	case tea.WindowSizeMsg:
		m.handleWindowSizeMsg(typed)
	// case for bus events
	case busEventMsg:
		return m.handleBusEventMsg(typed)
	// case for spinner tick
	case spinner.TickMsg:
		if m.state == stateWaiting {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(typed)
			cmds = append(cmds, cmd)
		}
	}

	return m.updateChildComponents(msg, cmds)
}

// handleKeyMsg handles key events and returns model, cmd, handled, shouldQuit.
func (m Model) handleKeyMsg(msg tea.KeyMsg) (Model, tea.Cmd, bool, bool) {
	if key.Matches(msg, keymap.Quit) {
		return m, nil, true, true
	}
	if m.state != stateReady {
		return m, nil, false, false
	}
	if !key.Matches(msg, keymap.Submit) {
		return m, nil, false, false
	}

	return m.handleSubmit()
}

// handleSubmit handles Enter/submit while the model is ready.
func (m Model) handleSubmit() (Model, tea.Cmd, bool, bool) {
	input := strings.TrimSpace(m.textinput.Value())
	if input == "" {
		return m, nil, true, false
	}
	m.textinput.SetValue("")
	if input == "/exit" {
		return m, nil, true, true
	}
	if input == "/new" || input == "/clear" {
		m.bus.Publish(bus.Event{Type: bus.EventNewConversation})
		m.messages = nil
		m.rebuildViewportContent()
		return m, nil, true, false
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
	return m, tea.Batch(m.spinner.Tick), true, false
}

func (m *Model) handleWindowSizeMsg(msg tea.WindowSizeMsg) {
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
}

func (m Model) handleBusEventMsg(msg busEventMsg) (tea.Model, tea.Cmd) {
	last, ok := m.lastMessage()
	if !ok {
		return m, nil
	}

	switch msg.Event.Type {
	case bus.EventAgentContentDelta:
		payload, ok := msg.Event.Payload.(bus.ContentDeltaPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleContentDelta(last, payload)
	case bus.EventAgentRefusal:
		payload, ok := msg.Event.Payload.(bus.RefusalPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleRefusal(last, payload)
	case bus.EventAgentToolCallStart:
		payload, ok := msg.Event.Payload.(bus.ToolCallStartPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleToolCallStart(last, payload)
	case bus.EventAgentToolCallDone:
		payload, ok := msg.Event.Payload.(bus.ToolCallDonePayload)
		if !ok {
			return m, nil
		}
		return m, m.handleToolCallDone(last, payload)
	case bus.EventAgentDone:
		payload, ok := msg.Event.Payload.(bus.AgentDonePayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentDone(last, payload)
	case bus.EventAgentError:
		payload, ok := msg.Event.Payload.(bus.AgentErrorPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentError(last, payload)
	default:
		return m, nil
	}
}

func (m *Model) lastMessage() (*chatMessage, bool) {
	if len(m.messages) == 0 {
		return nil, false
	}
	return &m.messages[len(m.messages)-1], true
}

func (m *Model) handleContentDelta(last *chatMessage, payload bus.ContentDeltaPayload) tea.Cmd {
	last.Content += payload.Content
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleRefusal(last *chatMessage, payload bus.RefusalPayload) tea.Cmd {
	last.Content += payload.Content
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleToolCallStart(last *chatMessage, payload bus.ToolCallStartPayload) tea.Cmd {
	last.ToolCalls = append(last.ToolCalls, toolCall{
		ID:   payload.ToolCallID,
		Name: payload.ToolName,
		Args: payload.ToolArgs,
	})
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleToolCallDone(last *chatMessage, payload bus.ToolCallDonePayload) tea.Cmd {
	for i := range last.ToolCalls {
		if last.ToolCalls[i].ID == payload.ToolCallID && !last.ToolCalls[i].Done {
			last.ToolCalls[i].Result = payload.ToolResult
			last.ToolCalls[i].Done = true
			break
		}
	}
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleAgentDone(last *chatMessage, payload bus.AgentDonePayload) tea.Cmd {
	last.Streaming = false
	last.Rendered = renderMarkdown(last.Content, m.width-4)
	m.lastUsage = Usage{
		PromptTokens:     payload.PromptTokens,
		CompletionTokens: payload.CompletionTokens,
		TotalTokens:      payload.TotalTokens,
	}
	m.totalUsage.PromptTokens += payload.PromptTokens
	m.totalUsage.CompletionTokens += payload.CompletionTokens
	m.totalUsage.TotalTokens += payload.TotalTokens
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleAgentError(last *chatMessage, payload bus.AgentErrorPayload) tea.Cmd {
	last.Streaming = false
	last.Error = payload.Error.Error()
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m Model) updateChildComponents(msg tea.Msg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

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
