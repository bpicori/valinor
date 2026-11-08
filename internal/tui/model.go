package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"valinor/internal/config"
	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/logger"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CommandSender is the interface for sending commands to the gateway.
type CommandSender interface {
	SendUserMessage(content string) bool
	SendNewConversation() bool
	SendChangeProviderModel(provider, model string) bool
	SendCreateNewSession(provider, model string, force bool) bool
	SendRequestProviderModels(provider string) bool
	SendCancelAgentRun() bool
}

// Layout constants
const (
	topPadding   = 1
	inputHeight  = 3
	footerHeight = 1
)

// Key bindings
var (
	keymap = struct {
		Submit      key.Binding
		Quit        key.Binding
		ChangeModel key.Binding
	}{
		Submit:      key.NewBinding(key.WithKeys("enter")),
		Quit:        key.NewBinding(key.WithKeys("ctrl+c")),
		ChangeModel: key.NewBinding(key.WithKeys("ctrl+m")),
	}
)

// appState tracks what the app is currently doing.
type appState int

const (
	stateReady       appState = iota // Waiting for user input
	stateWaiting                     // Waiting for agent response
	stateModelPicker                 // Showing provider/model picker
)

type modelPickerState int

const (
	pickerSelectProvider modelPickerState = iota
	pickerSelectModel
)

// providerItem is a list item for provider selection.
type providerItem struct {
	Provider     string
	DefaultModel string
	Current      bool
}

func (i providerItem) Title() string { return i.Provider }
func (i providerItem) Description() string {
	if i.Current {
		return "current • default " + i.DefaultModel
	}
	return "default " + i.DefaultModel
}
func (i providerItem) FilterValue() string { return i.Provider + " " + i.DefaultModel }

// providerModelItem is a list item for model selection.
type providerModelItem struct {
	Provider string
	Model    string
	Display  string
}

func (i providerModelItem) Title() string {
	if i.Display != "" {
		return i.Display
	}
	return i.Model
}
func (i providerModelItem) Description() string { return i.Model }
func (i providerModelItem) FilterValue() string {
	return i.Provider + " " + i.Model + " " + i.Display
}

// messageRole identifies who authored a chat message.
type messageRole int

const (
	roleUser messageRole = iota
	roleAssistant
	roleSystem
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
	sender            CommandSender
	gatewayConnected  bool
	gatewayStatus     string
	sessionKey        string
	providerName      string
	modelName         string
	viewport          viewport.Model
	textinput         textinput.Model
	spinner           spinner.Model
	modelPicker       list.Model
	modelPickerState  modelPickerState
	modelPickerHost   string
	modelPickerBusy   bool
	modelPickerErr    string
	state             appState
	messages          []chatMessage
	contextPrompt     int64
	contextEstimated  bool
	width             int
	height            int
	contentWidth      int // width minus sidebar for viewport/chat
	ready             bool
	thinkingStartedAt time.Time
}

// New creates a new UI model.
func New(sender CommandSender, providerName, modelName string) Model {
	ti := textinput.New()
	ti.Placeholder = "Send a message..."
	ti.Prompt = "> "
	ti.CharLimit = 0
	ti.SetSuggestions([]string{"/new", "/clear", "/model", "/exit"})
	ti.ShowSuggestions = true
	ti.PromptStyle = themeStyles.UserPrefix
	ti.TextStyle = lipgloss.NewStyle().Foreground(color(currentTheme.TextPrimary))
	ti.PlaceholderStyle = themeStyles.Hint
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(color(currentTheme.AccentUser))

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = themeStyles.Spinner

	items := buildProviderItems(config.Must(), providerName)
	delegate := list.NewDefaultDelegate()
	delegate.Styles.NormalTitle = themeStyles.PickerNormalTitle
	delegate.Styles.NormalDesc = themeStyles.PickerNormalDesc
	delegate.Styles.SelectedTitle = themeStyles.PickerSelectedTitle
	delegate.Styles.SelectedDesc = themeStyles.PickerSelectedDesc
	l := list.New(items, delegate, 40, 14)
	l.Title = "Select Provider"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.Styles.Title = themeStyles.PickerTitle
	l.Styles.HelpStyle = themeStyles.Hint
	l.Styles.FilterPrompt = themeStyles.Hint
	l.Styles.FilterCursor = lipgloss.NewStyle().Foreground(color(currentTheme.AccentUser))

	return Model{
		sender:           sender,
		gatewayConnected: true,
		providerName:     providerName,
		modelName:        modelName,
		textinput:        ti,
		spinner:          sp,
		modelPicker:      l,
		modelPickerState: pickerSelectProvider,
		state:            stateReady,
	}
}

// buildProviderItems returns provider list items from config.
func buildProviderItems(cfg config.Config, currentProvider string) []list.Item {
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	items := make([]list.Item, 0, len(names))
	for _, name := range names {
		pc := cfg.Providers[name]
		items = append(items, providerItem{
			Provider:     name,
			DefaultModel: pc.DefaultModel,
			Current:      name == currentProvider,
		})
	}
	return items
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
	// case for WebSocket events from gateway
	case wsEventMsg:
		return m.handleWSEventMsg(typed)
	// case for connection status changes
	case connectionStatusMsg:
		// Connection status updates UI footer state.
		if !typed.Connected && typed.Error != nil {
			logger.Warning("tui connection lost", logger.BuildMetadata("error", typed.Error))
			m.gatewayConnected = false
			m.gatewayStatus = typed.Error.Error()
			return m, nil
		}
		if typed.Connected {
			m.gatewayConnected = true
			m.gatewayStatus = ""
			if typed.SessionKey != "" {
				m.sessionKey = typed.SessionKey
			}
			if typed.Provider != "" {
				m.providerName = typed.Provider
			}
			if typed.Model != "" {
				m.modelName = typed.Model
			}
			return m, nil
		}

		// Handle generic disconnected messages even when no explicit error is attached.
		if !typed.Connected {
			m.gatewayConnected = false
			if m.gatewayStatus == "" {
				m.gatewayStatus = "connection lost"
			}
		}
		return m, nil
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
	if m.state == stateModelPicker {
		return m.handleModelPickerKey(msg)
	}
	if m.state == stateWaiting {
		if msg.String() == "esc" {
			if m.sender != nil {
				m.sender.SendCancelAgentRun()
			}
			return m, nil, true, false
		}
		return m, nil, false, false
	}
	if m.state != stateReady {
		return m, nil, false, false
	}
	if key.Matches(msg, keymap.ChangeModel) {
		return m.openProviderPicker(), nil, true, false
	}
	if !key.Matches(msg, keymap.Submit) {
		return m, nil, false, false
	}

	return m.handleSubmit()
}

// handleModelPickerKey handles keys when the model picker is open.
func (m Model) handleModelPickerKey(msg tea.KeyMsg) (Model, tea.Cmd, bool, bool) {
	if msg.String() == "esc" {
		if m.modelPickerState == pickerSelectModel && !m.modelPickerBusy {
			return m.openProviderPicker(), nil, true, false
		}
		m.state = stateReady
		m.modelPickerErr = ""
		m.modelPickerBusy = false
		return m, nil, true, false
	}
	var cmd tea.Cmd
	m.modelPicker, cmd = m.modelPicker.Update(msg)
	// Check if user selected an item (Enter on a selected item)
	if msg.String() == "enter" {
		if m.modelPickerBusy {
			return m, nil, true, false
		}
		if m.modelPickerState == pickerSelectProvider {
			if i, ok := m.modelPicker.SelectedItem().(providerItem); ok {
				return m.openModelPickerForProvider(i.Provider), nil, true, false
			}
			return m, nil, true, false
		}
		if i, ok := m.modelPicker.SelectedItem().(providerModelItem); ok {
			if m.sender != nil {
				if i.Provider != m.providerName {
					m.sender.SendCreateNewSession(i.Provider, i.Model, false)
				} else {
					m.sender.SendChangeProviderModel(i.Provider, i.Model)
				}
			}
			m.providerName = i.Provider
			m.modelName = i.Model
			m.state = stateReady
			m.modelPickerErr = ""
			m.modelPickerBusy = false
			return m, nil, true, false
		}
	}
	return m, cmd, true, false
}

func (m Model) openProviderPicker() Model {
	m.state = stateModelPicker
	m.modelPickerState = pickerSelectProvider
	m.modelPickerHost = ""
	m.modelPickerBusy = false
	m.modelPickerErr = ""
	m.modelPicker.Title = "Select Provider"
	m.modelPicker.SetItems(buildProviderItems(config.Must(), m.providerName))
	m.modelPicker.ResetFilter()
	items := m.modelPicker.Items()
	for i := range items {
		item, ok := items[i].(providerItem)
		if ok && item.Provider == m.providerName {
			m.modelPicker.Select(i)
			break
		}
	}
	if m.contentWidth > 4 && m.height > 6 {
		m.modelPicker.SetSize(m.contentWidth-4, m.height-6)
	}
	return m
}

func (m Model) openModelPickerForProvider(provider string) Model {
	if strings.TrimSpace(provider) == "" {
		return m.openProviderPicker()
	}
	if _, ok := config.Must().Providers[provider]; !ok {
		logger.Warning("tui unknown provider for model picker", logger.BuildMetadata("provider", provider))
		return m.openProviderPicker()
	}

	m.state = stateModelPicker
	m.modelPickerState = pickerSelectModel
	m.modelPickerHost = provider
	m.modelPickerBusy = true
	m.modelPickerErr = ""
	m.modelPicker.Title = "Loading models for " + provider + "..."
	m.modelPicker.SetItems(nil)
	m.modelPicker.ResetFilter()
	if m.contentWidth > 4 && m.height > 6 {
		m.modelPicker.SetSize(m.contentWidth-4, m.height-6)
	}
	if m.sender == nil || !m.sender.SendRequestProviderModels(provider) {
		m.modelPickerBusy = false
		m.modelPickerErr = "failed to request provider models"
		m.modelPicker.Title = "Select Model (" + provider + ") - request failed"
	}
	return m
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
		if input == "/new" && m.sender != nil {
			m.sender.SendCreateNewSession(m.providerName, m.modelName, true)
		}
		if input == "/clear" && m.sender != nil {
			m.sender.SendNewConversation()
		}
		return m, nil, true, false
	}
	if strings.HasPrefix(input, "/model") {
		return m.handleModelCommand(input)
	}

	m.messages = append(m.messages, chatMessage{Role: roleUser, Content: input})
	m.messages = append(m.messages, chatMessage{Role: roleAssistant, Streaming: true})
	m.state = stateWaiting
	m.thinkingStartedAt = time.Now()
	m.rebuildViewportContent()
	if m.sender == nil || !m.sender.SendUserMessage(input) {
		if last, ok := m.lastMessage(); ok {
			last.Streaming = false
			last.Error = "failed to send message to gateway"
		}
		m.state = stateReady
		m.rebuildViewportContent()
		return m, nil, true, false
	}
	return m, tea.Batch(m.spinner.Tick), true, false
}

// handleModelCommand processes /model, /model <provider>, or /model <provider> <model>.
// With provider only, it opens searchable model selection for that provider.
func (m Model) handleModelCommand(input string) (Model, tea.Cmd, bool, bool) {
	parts := strings.Fields(strings.TrimPrefix(input, "/model"))
	if len(parts) == 0 {
		return m.openProviderPicker(), nil, true, false
	}
	provider := strings.TrimSpace(parts[0])
	if provider == "" {
		return m.openProviderPicker(), nil, true, false
	}
	pc, ok := config.Must().Providers[provider]
	if !ok {
		logger.Warning("tui unknown provider", logger.BuildMetadata("provider", provider))
		return m, nil, true, false
	}
	if len(parts) == 1 {
		return m.openModelPickerForProvider(provider), nil, true, false
	}
	model := pc.DefaultModel
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		model = strings.TrimSpace(parts[1])
	}
	if provider != m.providerName {
		if m.sender != nil {
			m.sender.SendCreateNewSession(provider, model, false)
		}
		m.providerName = provider
		m.modelName = model
		return m, nil, true, false
	}
	if m.sender != nil {
		m.sender.SendChangeProviderModel(provider, model)
	}
	m.providerName = provider
	m.modelName = model
	return m, nil, true, false
}

func (m *Model) handleWindowSizeMsg(msg tea.WindowSizeMsg) {
	m.width = msg.Width
	m.height = msg.Height
	m.contentWidth = msg.Width
	if m.contentWidth < 20 {
		m.contentWidth = 20
	}

	spinnerHeight := 1
	viewportHeight := msg.Height - topPadding - spinnerHeight - inputHeight - footerHeight
	if viewportHeight < 1 {
		viewportHeight = 1
	}

	if !m.ready {
		m.viewport = viewport.New(m.contentWidth, viewportHeight)
		m.viewport.MouseWheelEnabled = true
		m.viewport.MouseWheelDelta = 3
		m.viewport.KeyMap.Up.SetKeys("up")
		m.viewport.KeyMap.Down.SetKeys("down")
		m.viewport.KeyMap.HalfPageUp.SetKeys("")
		m.viewport.KeyMap.HalfPageDown.SetKeys("")
		m.viewport.KeyMap.PageUp.SetKeys("")
		m.viewport.KeyMap.PageDown.SetKeys("")
		m.ready = true
	} else {
		m.viewport.Width = m.contentWidth
		m.viewport.Height = viewportHeight
	}

	m.textinput.Width = m.contentWidth - 10
	if m.state == stateModelPicker && m.contentWidth > 4 && m.height > 6 {
		m.modelPicker.SetSize(m.contentWidth-4, m.height-6)
	}
	m.rebuildViewportContent()
}

func (m Model) handleWSEventMsg(msg wsEventMsg) (tea.Model, tea.Cmd) {
	// Parse the WebSocket event into a router Event
	ev, err := ParseWSEvent(msg)
	if err != nil {
		logger.Warning("tui failed to parse ws event", logger.BuildMetadata("error", err, "type", msg.Type))
		return m, nil
	}

	switch ev.Type {
	case event.EventType(gateway.EventSessionAttached):
		payload, ok := ev.Payload.(gateway.SessionAttachedPayload)
		if !ok {
			return m, nil
		}
		// A changed session key means the identity switched sessions (e.g. /new).
		if m.sessionKey != "" && payload.SessionKey != "" && payload.SessionKey != m.sessionKey {
			m.handleNewConversation()
		}
		m.sessionKey = payload.SessionKey
		m.providerName = payload.Provider
		m.modelName = payload.Model
		logger.Info("tui session attached", logger.BuildMetadata(
			"session_key", payload.SessionKey,
			"provider", payload.Provider,
			"model", payload.Model,
		))
		return m, nil
	case event.EventType(gateway.EventNewConversation):
		return m, m.handleNewConversation()
	case event.EventType(gateway.EventChangeProviderModel):
		payload, ok := ev.Payload.(gateway.ChangeProviderModelPayload)
		if !ok {
			return m, nil
		}
		m.providerName = payload.Provider
		m.modelName = payload.Model
		return m, nil
	case event.EventType(gateway.EventProviderModelsResult):
		payload, ok := ev.Payload.(gateway.ProviderModelsResultPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleProviderModelsResult(payload)
	case event.EventType(gateway.EventContextTokens):
		payload, ok := ev.Payload.(gateway.ContextTokensPayload)
		if !ok {
			return m, nil
		}
		m.contextPrompt = payload.PromptTokens
		m.contextEstimated = payload.Estimated
		return m, nil
	case event.EventType(gateway.EventContextCompressed):
		payload, ok := ev.Payload.(gateway.ContextCompressedPayload)
		if !ok {
			return m, nil
		}
		logger.Info("tui context compressed", logger.BuildMetadata(
			"messages_compressed", payload.MessagesCompressed,
			"tokens_before", payload.TokensBefore,
			"tokens_after", payload.TokensAfter,
		))
		return m, nil
	case event.EventType(gateway.EventContextCompressionFailed):
		payload, ok := ev.Payload.(gateway.ContextCompressionFailedPayload)
		if !ok {
			return m, nil
		}
		logger.Warning("tui context compression failed", logger.BuildMetadata(
			"estimated_tokens", payload.EstimatedTokens,
			"context_limit", payload.ContextLimit,
			"error", payload.Error,
		))
		return m, nil
	case event.CronJobResult:
		payload, ok := ev.Payload.(event.CronJobResultPayload)
		if !ok {
			return m, nil
		}
		logger.Debug("tui: received cron job result",
			logger.BuildMetadata("content_length", len(payload.Content), "timestamp", payload.Timestamp))
		return m, m.handleCronJobResult(payload)
	case event.EventType(gateway.EventAgentQuestion):
		payload, ok := ev.Payload.(gateway.QuestionPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentQuestion(payload)
	case event.EventType(gateway.EventAgentFile):
		payload, ok := ev.Payload.(gateway.FilePayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentFile(payload)
	}

	last, ok := m.lastMessage()
	if !ok {
		return m, nil
	}

	switch ev.Type {
	case event.EventType(gateway.EventAgentContentDelta):
		payload, ok := ev.Payload.(gateway.ContentDeltaPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleContentDelta(last, payload)
	case event.EventType(gateway.EventAgentContentFull):
		payload, ok := ev.Payload.(gateway.ContentFullPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleContentFull(last, payload)
	case event.EventType(gateway.EventAgentRefusal):
		payload, ok := ev.Payload.(gateway.RefusalPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleRefusal(last, payload)
	case event.EventType(gateway.EventAgentToolCallStart):
		payload, ok := ev.Payload.(gateway.ToolCallStartPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleToolCallStart(last, payload)
	case event.EventType(gateway.EventAgentToolCallDone):
		payload, ok := ev.Payload.(gateway.ToolCallDonePayload)
		if !ok {
			return m, nil
		}
		return m, m.handleToolCallDone(last, payload)
	case event.EventType(gateway.EventAgentDone):
		payload, ok := ev.Payload.(gateway.AgentDonePayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentDone(last, payload)
	case event.EventType(gateway.EventAgentError):
		payload, ok := ev.Payload.(gateway.AgentErrorPayload)
		if !ok {
			return m, nil
		}
		return m, m.handleAgentError(last, payload)
	case event.EventType(gateway.EventAgentCancelled):
		return m, m.handleAgentCancelled(last)
	default:
		return m, nil
	}
}

func (m *Model) handleProviderModelsResult(payload gateway.ProviderModelsResultPayload) tea.Cmd {
	if m.state != stateModelPicker || m.modelPickerState != pickerSelectModel {
		return nil
	}
	if payload.Provider != m.modelPickerHost {
		return nil
	}

	m.modelPickerBusy = false
	m.modelPickerErr = payload.Error
	m.modelPicker.Title = "Select Model (" + payload.Provider + ")"
	if payload.Error != "" {
		logger.Warning("provider model lookup failed", logger.BuildMetadata("provider", payload.Provider, "error", payload.Error))
		m.modelPicker.Title = "Select Model (" + payload.Provider + ") - fallback"
	}

	seen := map[string]struct{}{}
	items := make([]providerModelItem, 0, len(payload.Models)+1)
	for _, model := range payload.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		items = append(items, providerModelItem{
			Provider: payload.Provider,
			Model:    id,
			Display:  strings.TrimSpace(model.DisplayName),
		})
	}
	if providerCfg, ok := config.Must().Providers[payload.Provider]; ok && providerCfg.DefaultModel != "" {
		if _, exists := seen[providerCfg.DefaultModel]; !exists {
			items = append(items, providerModelItem{
				Provider: payload.Provider,
				Model:    providerCfg.DefaultModel,
				Display:  providerCfg.DefaultModel,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		left := strings.ToLower(items[i].Title())
		right := strings.ToLower(items[j].Title())
		if left == right {
			return items[i].Model < items[j].Model
		}
		return left < right
	})

	listItems := make([]list.Item, 0, len(items))
	selectedIdx := 0
	for idx, item := range items {
		listItems = append(listItems, item)
		if payload.Provider == m.providerName && item.Model == m.modelName {
			selectedIdx = idx
		}
	}
	m.modelPicker.SetItems(listItems)
	m.modelPicker.ResetFilter()
	if len(listItems) > 0 {
		m.modelPicker.Select(selectedIdx)
	}
	return nil
}

func (m *Model) handleNewConversation() tea.Cmd {
	m.messages = nil
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleCronJobResult(payload event.CronJobResultPayload) tea.Cmd {
	// Build display title - use job name if available
	title := "Scheduled Task"
	if payload.JobName != "" {
		title = fmt.Sprintf("Scheduled: %s", payload.JobName)
	}

	// Add a system message showing the cron job result
	msg := chatMessage{
		Role:      roleSystem,
		Content:   fmt.Sprintf("[%s %s]\n\n%s", title, payload.Timestamp.Format("15:04"), payload.Content),
		Rendered:  renderMarkdown(fmt.Sprintf("**%s** (%s)\n\n%s", title, payload.Timestamp.Format("15:04"), payload.Content), m.width-4),
		Streaming: false,
	}
	m.messages = append(m.messages, msg)
	m.rebuildViewportContent()
	// Auto-scroll to show the new message
	m.viewport.GotoBottom()
	return nil
}

func (m *Model) lastMessage() (*chatMessage, bool) {
	if len(m.messages) == 0 {
		return nil, false
	}
	return &m.messages[len(m.messages)-1], true
}

func (m *Model) handleAgentQuestion(payload gateway.QuestionPayload) tea.Cmd {
	content := payload.Question
	if len(payload.Options) > 0 {
		content += "\n"
		for _, option := range payload.Options {
			content += "\n- " + option
		}
	}
	switch {
	case payload.AllowFreeText:
		content += "\n\nType an option or your own answer."
	case len(payload.Options) > 0:
		content += "\n\nReply with one of the options above."
	}

	msg := chatMessage{
		Role:     roleSystem,
		Content:  content,
		Rendered: renderMarkdown(content, m.width-4),
	}
	m.messages = append(m.messages, msg)
	m.rebuildViewportContent()
	m.viewport.GotoBottom()
	return nil
}

func (m *Model) handleAgentFile(payload gateway.FilePayload) tea.Cmd {
	content := fmt.Sprintf("File ready: %s", payload.Path)
	if payload.Caption != "" {
		content += "\n\n" + payload.Caption
	}

	msg := chatMessage{
		Role:     roleSystem,
		Content:  content,
		Rendered: renderMarkdown(content, m.width-4),
	}
	m.messages = append(m.messages, msg)
	m.rebuildViewportContent()
	m.viewport.GotoBottom()
	return nil
}

func (m *Model) handleContentDelta(last *chatMessage, payload gateway.ContentDeltaPayload) tea.Cmd {
	last.Content += payload.Content
	m.appendStreamingContent()
	return nil
}

func (m *Model) handleContentFull(last *chatMessage, payload gateway.ContentFullPayload) tea.Cmd {
	last.Content = payload.Content
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleRefusal(last *chatMessage, payload gateway.RefusalPayload) tea.Cmd {
	last.Content += payload.Content
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleToolCallStart(last *chatMessage, payload gateway.ToolCallStartPayload) tea.Cmd {
	last.ToolCalls = append(last.ToolCalls, toolCall{
		ID:   payload.ToolCallID,
		Name: payload.ToolName,
		Args: payload.ToolArgs,
	})
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleToolCallDone(last *chatMessage, payload gateway.ToolCallDonePayload) tea.Cmd {
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

func (m *Model) handleAgentDone(last *chatMessage, payload gateway.AgentDonePayload) tea.Cmd {
	last.Streaming = false
	last.Rendered = renderMarkdown(last.Content, m.width-4)
	m.contextPrompt = payload.PromptTokens
	m.contextEstimated = false
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleAgentError(last *chatMessage, payload gateway.AgentErrorPayload) tea.Cmd {
	logger.Error("tui agent error", logger.BuildMetadata("error", payload.Error))
	last.Streaming = false
	if payload.Error != nil {
		last.Error = payload.Error.Error()
	}
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m *Model) handleAgentCancelled(last *chatMessage) tea.Cmd {
	last.Streaming = false
	last.Rendered = renderMarkdown(last.Content, m.width-4)
	m.state = stateReady
	m.rebuildViewportContent()
	return nil
}

func (m Model) updateChildComponents(msg tea.Msg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	if m.state == stateModelPicker {
		m.modelPicker, cmd = m.modelPicker.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}
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
