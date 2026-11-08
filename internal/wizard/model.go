package wizard

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"valinor/internal/config"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type step int

const (
	stepProviders step = iota
	stepProviderForm
	stepDirectories
	stepNetworking
	stepAgent
	stepTelegram
	stepReview
	stepDone
)

// Model is the Bubble Tea model for the setup wizard.
type Model struct {
	cfg        config.Config
	step       step
	styles     styles
	width      int
	height     int
	ready      bool
	errMsg     string
	successMsg string
	cancelled  bool
	saved      bool

	providerList list.Model
	providerForm *providerForm
	form         fieldForm
	review       viewport.Model
}

// New creates a wizard model seeded with the initial config.
func New(initial config.Config) Model {
	if initial.Providers == nil {
		initial.Providers = map[string]config.ProviderConfig{}
	}
	if initial.DefaultProvider == "" && len(initial.Providers) == 1 {
		for name := range initial.Providers {
			initial.DefaultProvider = name
		}
	}

	m := Model{
		cfg:    initial,
		step:   stepProviders,
		styles: newStyles(grokNight),
	}
	m.providerList = newProviderList(initial, maxPanelWidth, 12)
	m.form = m.buildStepForm(stepDirectories, maxPanelWidth-8)
	m.review = viewport.New(maxPanelWidth-8, 12)
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.ready = true
		innerW := m.panelWidth() - 8
		innerH := m.contentHeight() - 4
		m.providerList.SetWidth(innerW)
		m.providerList.SetHeight(providerListHeight(len(m.cfg.Providers), innerH))
		m.form.setWidth(innerW)
		if m.providerForm != nil {
			m.providerForm.setWidth(innerW)
		}
		m.review.Width = innerW
		m.review.Height = innerH
		return m, nil

	case tea.KeyMsg:
		if key.Matches(typed, keyQuit) {
			m.cancelled = true
			return m, tea.Quit
		}
		if handled, cmd := m.handleKey(typed); handled {
			return m, cmd
		}
	}

	if cmd := m.updateActive(msg); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(keyMsg tea.KeyMsg) (bool, tea.Cmd) {
	switch m.step {
	case stepProviders:
		return m.handleProvidersKey(keyMsg)
	case stepProviderForm:
		return m.handleProviderFormKey(keyMsg)
	case stepReview:
		return m.handleReviewKey(keyMsg)
	default:
		return m.handleFormKey(keyMsg)
	}
}

func (m *Model) updateActive(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.step {
	case stepProviders:
		m.providerList, cmd = m.providerList.Update(msg)
	case stepProviderForm:
		if m.providerForm == nil || m.providerForm.typeDropdownOpen() {
			return nil
		}
		switch m.providerForm.focus {
		case pfName:
			m.providerForm.name, cmd = m.providerForm.name.Update(msg)
		case pfAPIKey:
			m.providerForm.apiKey, cmd = m.providerForm.apiKey.Update(msg)
		case pfModel:
			m.providerForm.model, cmd = m.providerForm.model.Update(msg)
		case pfBaseURL:
			m.providerForm.baseURL, cmd = m.providerForm.baseURL.Update(msg)
		}
	case stepReview:
		m.review, cmd = m.review.Update(msg)
	default:
		if m.form.focus < len(m.form.inputs) && m.form.fields[m.form.focus].kind != fieldBool {
			m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		}
	}
	return cmd
}

func (m *Model) handleProvidersKey(keyMsg tea.KeyMsg) (bool, tea.Cmd) {
	m.errMsg = ""
	switch keyMsg.String() {
	case "a":
		form := newProviderForm("", config.ProviderConfig{}, m.panelWidth()-8)
		m.providerForm = &form
		m.step = stepProviderForm
		return true, nil
	case "e":
		item, ok := m.providerList.SelectedItem().(providerItem)
		if !ok {
			m.errMsg = "select a provider to edit"
			return true, nil
		}
		form := newProviderForm(item.name, item.provider, m.panelWidth()-8)
		m.providerForm = &form
		m.step = stepProviderForm
		return true, nil
	case "d":
		item, ok := m.providerList.SelectedItem().(providerItem)
		if !ok {
			m.errMsg = "select a provider to delete"
			return true, nil
		}
		delete(m.cfg.Providers, item.name)
		if m.cfg.DefaultProvider == item.name {
			m.cfg.DefaultProvider = ""
			if len(m.cfg.Providers) == 1 {
				for name := range m.cfg.Providers {
					m.cfg.DefaultProvider = name
				}
			}
		}
		m.providerList.SetItems(buildProviderItems(m.cfg))
		m.providerList.SetHeight(providerListHeight(len(m.cfg.Providers), m.contentHeight()-4))
		return true, nil
	case "s":
		item, ok := m.providerList.SelectedItem().(providerItem)
		if !ok {
			m.errMsg = "select a provider to set as default"
			return true, nil
		}
		m.cfg.DefaultProvider = item.name
		m.providerList.SetItems(buildProviderItems(m.cfg))
		m.providerList.SetHeight(providerListHeight(len(m.cfg.Providers), m.contentHeight()-4))
		return true, nil
	}
	if isNextStepKey(keyMsg, true) {
		if len(m.cfg.Providers) == 0 {
			m.errMsg = "add at least one provider"
			return true, nil
		}
		if m.cfg.DefaultProvider == "" {
			m.errMsg = "set a default provider with s"
			return true, nil
		}
		m.step = stepDirectories
		m.form = m.buildStepForm(stepDirectories, m.panelWidth()-8)
		return true, nil
	}
	return false, nil
}

func (m *Model) handleProviderFormKey(keyMsg tea.KeyMsg) (bool, tea.Cmd) {
	if m.providerForm == nil {
		return true, nil
	}
	m.errMsg = ""

	if m.providerForm.typeDropdownOpen() {
		switch keyMsg.String() {
		case "up", "ctrl+p":
			m.providerForm.typeSelect.CursorUp()
			return true, nil
		case "down", "ctrl+n":
			m.providerForm.typeSelect.CursorDown()
			return true, nil
		case "tab":
			m.providerForm.typeSelect.Confirm()
			m.providerForm.nextFocus()
			return true, nil
		case "enter", " ":
			m.providerForm.typeSelect.Confirm()
			return true, nil
		case "esc":
			m.providerForm.typeSelect.Close()
			return true, nil
		case "shift+tab":
			m.providerForm.typeSelect.Close()
			m.providerForm.prevFocus()
			return true, nil
		}
		return true, nil
	}

	switch keyMsg.String() {
	case "tab":
		m.providerForm.nextFocus()
		return true, nil
	case "shift+tab":
		m.providerForm.prevFocus()
		return true, nil
	case "enter", " ":
		if m.providerForm.focus == pfType {
			m.providerForm.typeSelect.Toggle()
			return true, nil
		}
		if !m.providerForm.isLastFocus() {
			m.providerForm.nextFocus()
			return true, nil
		}
		if err := m.saveProviderForm(); err != nil {
			m.errMsg = err.Error()
			return true, nil
		}
		return true, nil
	case "down":
		if m.providerForm.focus == pfType {
			m.providerForm.typeSelect.Toggle()
			return true, nil
		}
		return false, nil
	case "esc":
		m.providerForm = nil
		m.step = stepProviders
		return true, nil
	}
	return false, nil
}

func (m *Model) saveProviderForm() error {
	if m.providerForm == nil {
		return nil
	}
	providerCfg, name, err := m.providerForm.validate()
	if err != nil {
		return err
	}
	if m.providerForm.editingName != "" && m.providerForm.editingName != name {
		delete(m.cfg.Providers, m.providerForm.editingName)
		if m.cfg.DefaultProvider == m.providerForm.editingName {
			m.cfg.DefaultProvider = ""
		}
	}
	if _, exists := m.cfg.Providers[name]; exists && m.providerForm.editingName != name {
		return fmt.Errorf("provider %q already exists", name)
	}
	if m.cfg.Providers == nil {
		m.cfg.Providers = map[string]config.ProviderConfig{}
	}
	m.cfg.Providers[name] = providerCfg
	if m.cfg.DefaultProvider == "" {
		m.cfg.DefaultProvider = name
	}
	m.providerList.SetItems(buildProviderItems(m.cfg))
	m.providerList.SetHeight(providerListHeight(len(m.cfg.Providers), m.contentHeight()-4))
	m.providerForm = nil
	m.step = stepProviders
	return nil
}

func (m *Model) handleFormKey(keyMsg tea.KeyMsg) (bool, tea.Cmd) {
	m.errMsg = ""
	if isNextStepKey(keyMsg, false) {
		return m.advanceFormStep()
	}
	switch {
	case key.Matches(keyMsg, keyTab):
		m.form.nextFocus()
		return true, nil
	case key.Matches(keyMsg, keyBack):
		m.form.prevFocus()
		return true, nil
	case keyMsg.String() == " ":
		if m.form.fields[m.form.focus].kind == fieldBool {
			m.form.toggleBool()
			return true, nil
		}
		return false, nil
	case keyMsg.String() == "enter":
		if !m.form.isLastFocus() {
			m.form.nextFocus()
			return true, nil
		}
		return m.advanceFormStep()
	case keyMsg.String() == "esc":
		if m.step > stepDirectories {
			m.step--
			m.form = m.buildStepForm(m.step, m.panelWidth()-8)
		}
		return true, nil
	}
	return false, nil
}

func (m *Model) advanceFormStep() (bool, tea.Cmd) {
	m.form.syncFields()
	if err := m.applyForm(); err != nil {
		m.errMsg = err.Error()
		return true, nil
	}
	next := m.step + 1
	if next == stepReview {
		m.refreshReview()
		m.step = next
		return true, nil
	}
	m.step = next
	m.form = m.buildStepForm(m.step, m.panelWidth()-8)
	return true, nil
}

func (m *Model) handleReviewKey(keyMsg tea.KeyMsg) (bool, tea.Cmd) {
	m.errMsg = ""
	switch keyMsg.String() {
	case "enter":
		if err := config.Validate(m.cfg); err != nil {
			m.errMsg = err.Error()
			return true, nil
		}
		if err := config.Save(m.cfg); err != nil {
			m.errMsg = err.Error()
			return true, nil
		}
		m.saved = true
		m.successMsg = "Configuration saved to ~/.valinor/config.toml"
		m.step = stepDone
		return true, tea.Quit
	case "esc":
		m.step = stepTelegram
		m.form = m.buildStepForm(stepTelegram, m.panelWidth()-8)
		return true, nil
	}
	if isNextStepKey(keyMsg, true) {
		if err := config.Validate(m.cfg); err != nil {
			m.errMsg = err.Error()
			return true, nil
		}
		if err := config.Save(m.cfg); err != nil {
			m.errMsg = err.Error()
			return true, nil
		}
		m.saved = true
		m.successMsg = "Configuration saved to ~/.valinor/config.toml"
		m.step = stepDone
		return true, tea.Quit
	}
	return false, nil
}

// View implements tea.Model.
func (m *Model) View() string {
	if !m.ready {
		return applyBackground(
			lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
				m.styles.hint.Render("Initializing...")),
			m.width, m.height,
		)
	}
	if m.step == stepDone && m.successMsg != "" {
		return applyBackground(
			lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
				m.styles.success.Render(m.successMsg)),
			m.width, m.height,
		)
	}
	return applyBackground(m.renderScreen(), m.width, m.height)
}

func (m *Model) buildStepForm(s step, width int) fieldForm {
	switch s {
	case stepDirectories:
		return newFieldForm([]formField{
			{label: "Valinor Dir", kind: fieldString, value: m.cfg.ValinorDir, hint: "~/.valinor"},
		}, width)
	case stepNetworking:
		return newFieldForm([]formField{
			{label: "Gateway Host", kind: fieldString, value: m.cfg.GatewayHost},
			{label: "Gateway Port", kind: fieldInt, value: fmt.Sprintf("%d", m.cfg.GatewayPort)},
			{label: "Gateway Path", kind: fieldString, value: m.cfg.GatewayPath},
		}, width)
	case stepAgent:
		return newFieldForm([]formField{
			{label: "Max Agent Iterations", kind: fieldInt, value: fmt.Sprintf("%d", m.cfg.MaxAgentIterations)},
			{
				label: "Preserve Recent Messages",
				kind:  fieldInt,
				value: fmt.Sprintf("%d", m.cfg.Context.PreserveRecentMessages),
				hint:  "When context exceeds ~85% capacity, older middle messages are summarized; this many recent messages stay verbatim",
			},
		}, width)
	case stepTelegram:
		return newFieldForm([]formField{
			{label: "Bot Token", kind: fieldString, value: m.cfg.Telegram.BotToken, hint: "from @BotFather"},
			{
				label: "Allowed User IDs",
				kind:  fieldString,
				value: formatIDList(m.cfg.Telegram.AllowedUserIDs),
				hint:  "comma-separated numeric IDs from @userinfobot; empty denies everyone",
			},
		}, width)
	default:
		return fieldForm{}
	}
}

func (m *Model) applyForm() error {
	m.form.syncFields()
	switch m.step {
	case stepDirectories:
		m.cfg.ValinorDir = strings.TrimSpace(m.form.fieldValue("Valinor Dir"))
	case stepNetworking:
		m.cfg.GatewayHost = strings.TrimSpace(m.form.fieldValue("Gateway Host"))
		gwPort, err := m.form.intValue("Gateway Port")
		if err != nil {
			return err
		}
		m.cfg.GatewayPort = gwPort
		m.cfg.GatewayPath = strings.TrimSpace(m.form.fieldValue("Gateway Path"))
	case stepAgent:
		maxIter, err := m.form.intValue("Max Agent Iterations")
		if err != nil {
			return err
		}
		preserve, err := m.form.intValue("Preserve Recent Messages")
		if err != nil {
			return err
		}
		m.cfg.MaxAgentIterations = maxIter
		m.cfg.Context.PreserveRecentMessages = preserve
	case stepTelegram:
		m.cfg.Telegram.BotToken = strings.TrimSpace(m.form.fieldValue("Bot Token"))
		if m.cfg.Telegram.BotToken == "" {
			return fmt.Errorf("bot token is required")
		}
		ids, err := parseIDList(m.form.fieldValue("Allowed User IDs"))
		if err != nil {
			return err
		}
		m.cfg.Telegram.AllowedUserIDs = ids
	}
	if m.cfg.ValinorDir == "" {
		m.cfg.ValinorDir = config.DefaultValinorDir
	}
	if m.cfg.Scheduler.ScriptTimeout == "" {
		m.cfg.Scheduler.ScriptTimeout = config.DefaultSchedulerScriptTimeout
	}
	return nil
}

func (m *Model) refreshReview() {
	names := make([]string, 0, len(m.cfg.Providers))
	for name := range m.cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(m.styles.title.Render("Summary"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Default provider:  %s\n", m.cfg.DefaultProvider))
	b.WriteString(fmt.Sprintf("Providers:         %d configured\n", len(m.cfg.Providers)))
	for _, name := range names {
		p := m.cfg.Providers[name]
		marker := "  "
		if name == m.cfg.DefaultProvider {
			marker = "★ "
		}
		b.WriteString(fmt.Sprintf("%s%s (%s, %s)\n", marker, name, p.Type, p.DefaultModel))
	}
	b.WriteString(fmt.Sprintf("\nValinor dir:       %s\n", m.cfg.ValinorDir))
	b.WriteString(fmt.Sprintf("Gateway:           %s\n", m.cfg.GatewayWebSocketURL()))
	b.WriteString(fmt.Sprintf("Max iterations:    %d\n", m.cfg.MaxAgentIterations))
	b.WriteString(fmt.Sprintf("Preserve recent:   %d messages\n", m.cfg.Context.PreserveRecentMessages))
	b.WriteString(fmt.Sprintf("Telegram:          enabled (%d allowed users)\n", len(m.cfg.Telegram.AllowedUserIDs)))
	b.WriteString("\n")
	b.WriteString(m.styles.hint.Render("Press enter to save to ~/.valinor/config.toml"))
	m.review.SetContent(b.String())
}

// formatIDList renders numeric IDs as a comma-separated form value.
func formatIDList(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ", ")
}

// parseIDList parses comma or whitespace separated numeric IDs.
func parseIDList(value string) ([]int64, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	ids := make([]int64, 0, len(fields))
	for _, field := range fields {
		id, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid Telegram user ID %q", field)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
