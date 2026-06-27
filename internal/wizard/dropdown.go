package wizard

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type dropdown struct {
	options  []string
	selected int
	cursor   int
	open     bool
	width    int
}

func newDropdown(options []string, value string, width int) dropdown {
	d := dropdown{
		options:  options,
		width:    width,
		selected: 0,
		cursor:   0,
	}
	for i, opt := range options {
		if opt == value {
			d.selected = i
			d.cursor = i
			break
		}
	}
	return d
}

func (d *dropdown) setWidth(width int) {
	d.width = width
}

func (d *dropdown) Value() string {
	if d.selected < 0 || d.selected >= len(d.options) {
		return ""
	}
	return d.options[d.selected]
}

func (d *dropdown) Toggle() {
	d.open = !d.open
	if d.open {
		d.cursor = d.selected
	}
}

func (d *dropdown) Close() {
	d.open = false
}

func (d *dropdown) Confirm() {
	d.selected = d.cursor
	d.open = false
}

func (d *dropdown) CursorUp() {
	if !d.open || len(d.options) == 0 {
		return
	}
	d.cursor--
	if d.cursor < 0 {
		d.cursor = len(d.options) - 1
	}
}

func (d *dropdown) CursorDown() {
	if !d.open || len(d.options) == 0 {
		return
	}
	d.cursor = (d.cursor + 1) % len(d.options)
}

func (d *dropdown) render(s styles, focused bool) string {
	border := s.inputBorder
	accent := s.inputAccentIdle
	if focused {
		border = s.inputBorderActive
		accent = s.inputAccent
	}

	value := d.Value()
	if value == "" {
		value = s.hint.Render("select type")
	} else {
		value = s.value.Render(value)
	}

	chevron := s.hint.Render(" ▾")
	if d.open {
		chevron = s.hint.Render(" ▴")
	}

	trigger := lipgloss.JoinHorizontal(lipgloss.Top,
		accent.Render(accentChar),
		" ",
		value,
		chevron,
	)
	lines := []string{
		s.inputContainer.Render(border.Width(d.width - 4).Render(trigger)),
	}

	if d.open {
		menu := d.renderMenu(s, focused)
		lines = append(lines, menu)
	}
	return strings.Join(lines, "\n")
}

func (d *dropdown) renderMenu(s styles, focused bool) string {
	if len(d.options) == 0 {
		return ""
	}
	var rows []string
	for i, opt := range d.options {
		style := s.pickerNormalTitle
		prefix := "  "
		if i == d.cursor && focused {
			style = s.pickerSelectedTitle
			prefix = s.inputAccent.Render("❙") + " "
		}
		rows = append(rows, style.Render(prefix+opt))
	}
	menu := strings.Join(rows, "\n")
	return s.dropdownMenu.Width(d.width - 4).Render(menu)
}