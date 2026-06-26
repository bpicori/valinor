package wizard

import (
	"fmt"
	"sort"
	"strings"

	"valinor/internal/config"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

const (
	pfName    = 0
	pfType    = 1
	pfAPIKey  = 2
	pfModel   = 3
	pfBaseURL = 4
)

type providerItem struct {
	name      string
	provider  config.ProviderConfig
	isDefault bool
}

func (i providerItem) Title() string {
	if i.isDefault {
		return "★ " + i.name
	}
	return i.name
}

func (i providerItem) Description() string {
	desc := i.provider.Type + " · " + i.provider.DefaultModel
	if i.provider.Type == "openai-compatible" && i.provider.BaseURL != "" {
		desc += " · " + i.provider.BaseURL
	}
	return desc
}

func (i providerItem) FilterValue() string {
	return i.name + " " + i.provider.Type + " " + i.provider.DefaultModel
}

type providerForm struct {
	editingName string
	name        textinput.Model
	typeSelect  dropdown
	apiKey      textinput.Model
	model       textinput.Model
	baseURL     textinput.Model
	focus       int
	width       int
}

func styledTextInput(placeholder string, width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Width = width - 8
	ti.Prompt = ""
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(grokNight.accent))
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(grokNight.textPrimary))
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(grokNight.grayDim))
	return ti
}

func newProviderForm(editingName string, existing config.ProviderConfig, width int) providerForm {
	name := styledTextInput("e.g. openai", width)
	name.SetValue(editingName)

	typeSelect := newDropdown(config.ProviderTypes, existing.Type, width)

	apiKey := styledTextInput("sk-...", width)
	apiKey.SetValue(existing.APIKey)

	modelInput := styledTextInput("e.g. gpt-4o", width)
	modelInput.SetValue(existing.DefaultModel)

	baseURL := styledTextInput("https://api.example.com/v1", width)
	baseURL.SetValue(existing.BaseURL)

	pf := providerForm{
		editingName: editingName,
		name:        name,
		typeSelect:  typeSelect,
		apiKey:      apiKey,
		model:       modelInput,
		baseURL:     baseURL,
		focus:       pfName,
		width:       width,
	}
	pf.syncFocus()
	return pf
}

func (p *providerForm) setWidth(width int) {
	p.width = width
	p.name.Width = width - 8
	p.typeSelect.setWidth(width)
	p.apiKey.Width = width - 8
	p.model.Width = width - 8
	p.baseURL.Width = width - 8
}

func (p *providerForm) fieldOrder() []int {
	if p.typeSelect.Value() == "openai-compatible" {
		return []int{pfName, pfType, pfAPIKey, pfModel, pfBaseURL}
	}
	return []int{pfName, pfType, pfAPIKey, pfModel}
}

func (p *providerForm) syncFocus() {
	p.name.Blur()
	p.apiKey.Blur()
	p.model.Blur()
	p.baseURL.Blur()
	p.typeSelect.Close()

	switch p.focus {
	case pfName:
		p.name.Focus()
	case pfAPIKey:
		p.apiKey.Focus()
	case pfModel:
		p.model.Focus()
	case pfBaseURL:
		p.baseURL.Focus()
	}
}

func (p *providerForm) nextFocus() {
	order := p.fieldOrder()
	for i, f := range order {
		if f == p.focus {
			p.focus = order[(i+1)%len(order)]
			p.syncFocus()
			return
		}
	}
}

func (p *providerForm) prevFocus() {
	order := p.fieldOrder()
	for i, f := range order {
		if f == p.focus {
			p.focus = order[(i-1+len(order))%len(order)]
			p.syncFocus()
			return
		}
	}
}

func (p *providerForm) isLastFocus() bool {
	order := p.fieldOrder()
	return len(order) > 0 && p.focus == order[len(order)-1]
}

func (p *providerForm) typeDropdownOpen() bool {
	return p.focus == pfType && p.typeSelect.open
}

func (p *providerForm) validate() (config.ProviderConfig, string, error) {
	name := strings.TrimSpace(p.name.Value())
	if name == "" {
		return config.ProviderConfig{}, "", fmt.Errorf("provider name is required")
	}
	typ := p.typeSelect.Value()
	if typ == "" {
		return config.ProviderConfig{}, "", fmt.Errorf("provider type is required")
	}
	apiKey := strings.TrimSpace(p.apiKey.Value())
	if apiKey == "" {
		return config.ProviderConfig{}, "", fmt.Errorf("api key is required")
	}
	model := strings.TrimSpace(p.model.Value())
	if model == "" {
		return config.ProviderConfig{}, "", fmt.Errorf("default model is required")
	}
	baseURL := strings.TrimSpace(p.baseURL.Value())
	if typ == "openai-compatible" && baseURL == "" {
		return config.ProviderConfig{}, "", fmt.Errorf("base URL is required for openai-compatible providers")
	}
	return config.ProviderConfig{
		Type:         typ,
		APIKey:       apiKey,
		BaseURL:      baseURL,
		DefaultModel: model,
	}, name, nil
}

func (p *providerForm) render(s styles) string {
	var b strings.Builder

	b.WriteString(s.label.Render("Name"))
	b.WriteString("\n")
	b.WriteString(renderInputBox(s, p.name, p.focus == pfName, p.width))
	b.WriteString("\n\n")

	b.WriteString(s.label.Render("Type"))
	b.WriteString(s.hint.Render("  ↵ or space to open"))
	b.WriteString("\n")
	b.WriteString(p.typeSelect.render(s, p.focus == pfType))
	b.WriteString("\n\n")

	b.WriteString(s.label.Render("API Key"))
	b.WriteString("\n")
	b.WriteString(renderInputBox(s, p.apiKey, p.focus == pfAPIKey, p.width))
	b.WriteString("\n\n")

	b.WriteString(s.label.Render("Default Model"))
	b.WriteString("\n")
	b.WriteString(renderInputBox(s, p.model, p.focus == pfModel, p.width))
	b.WriteString("\n")

	if p.typeSelect.Value() == "openai-compatible" {
		b.WriteString("\n")
		b.WriteString(s.label.Render("Base URL"))
		b.WriteString("\n")
		b.WriteString(renderInputBox(s, p.baseURL, p.focus == pfBaseURL, p.width))
	}

	return b.String()
}

func buildProviderItems(cfg config.Config) []list.Item {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]list.Item, 0, len(names))
	for _, name := range names {
		items = append(items, providerItem{
			name:      name,
			provider:  cfg.Providers[name],
			isDefault: name == cfg.DefaultProvider,
		})
	}
	return items
}

func newListDelegate(s styles) list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.SetHeight(2)
	d.SetSpacing(0)
	d.Styles.NormalTitle = s.pickerNormalTitle
	d.Styles.NormalDesc = s.pickerNormalDesc
	d.Styles.SelectedTitle = s.pickerSelectedTitle
	d.Styles.SelectedDesc = s.pickerSelectedDesc
	return d
}

func configureList(l *list.Model, s styles) {
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	l.SetFilteringEnabled(false)
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0)
	l.Styles.Title = s.pickerTitle
	l.Styles.NoItems = s.hint
	l.Styles.PaginationStyle = s.hint
	l.Styles.HelpStyle = s.hint
}

func newStyledList(items []list.Item, title string, width, height int) list.Model {
	s := newStyles(grokNight)
	delegate := newListDelegate(s)
	l := list.New(items, delegate, width, height)
	l.Title = title
	configureList(&l, s)
	return l
}

func providerListHeight(itemCount, maxHeight int) int {
	h := itemCount*2 + 1
	if h < 4 {
		h = 4
	}
	if h > maxHeight {
		h = maxHeight
	}
	return h
}

func newProviderList(cfg config.Config, width, maxHeight int) list.Model {
	items := buildProviderItems(cfg)
	h := providerListHeight(len(items), maxHeight)
	l := newStyledList(items, "", width-8, h)
	return l
}