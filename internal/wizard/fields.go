package wizard

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

type fieldKind int

const (
	fieldString fieldKind = iota
	fieldInt
	fieldBool
)

type formField struct {
	label    string
	kind     fieldKind
	value    string
	optional bool
	hint     string
}

type fieldForm struct {
	fields []formField
	inputs []textinput.Model
	focus  int
	width  int
}

func newFieldForm(fields []formField, width int) fieldForm {
	inputs := make([]textinput.Model, len(fields))
	for i, f := range fields {
		ti := styledTextInput(f.hint, width)
		ti.CharLimit = 512
		switch f.kind {
		case fieldBool:
			if strings.EqualFold(f.value, "true") || f.value == "yes" {
				ti.SetValue("yes")
			} else {
				ti.SetValue("no")
			}
		default:
			ti.SetValue(f.value)
		}
		inputs[i] = ti
	}
	form := fieldForm{
		fields: fields,
		inputs: inputs,
		width:  width,
	}
	form.focusInput()
	return form
}

func (f *fieldForm) setWidth(width int) {
	f.width = width
	for i := range f.inputs {
		f.inputs[i].Width = width - 8
	}
}

func (f *fieldForm) focusInput() {
	for i := range f.inputs {
		if i == f.focus {
			f.inputs[i].Focus()
		} else {
			f.inputs[i].Blur()
		}
	}
}

func (f *fieldForm) nextFocus() {
	f.focus = (f.focus + 1) % len(f.inputs)
	f.focusInput()
}

func (f *fieldForm) prevFocus() {
	f.focus--
	if f.focus < 0 {
		f.focus = len(f.inputs) - 1
	}
	f.focusInput()
}

func (f *fieldForm) isLastFocus() bool {
	return f.focus == len(f.inputs)-1
}

func (f *fieldForm) toggleBool() {
	if f.fields[f.focus].kind != fieldBool {
		return
	}
	if strings.EqualFold(f.inputs[f.focus].Value(), "yes") {
		f.inputs[f.focus].SetValue("no")
	} else {
		f.inputs[f.focus].SetValue("yes")
	}
}

func (f *fieldForm) syncFields() {
	for i := range f.fields {
		f.fields[i].value = f.inputs[i].Value()
	}
}

func (f *fieldForm) fieldValue(label string) string {
	for _, field := range f.fields {
		if field.label == label {
			return field.value
		}
	}
	return ""
}

func (f *fieldForm) intValue(label string) (int, error) {
	v := strings.TrimSpace(f.fieldValue(label))
	if v == "" {
		return 0, fmt.Errorf("%s is required", label)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", label)
	}
	return n, nil
}

func (f *fieldForm) fieldBoolAt(i int) bool {
	return strings.EqualFold(f.inputs[i].Value(), "yes")
}

func (f *fieldForm) render(s styles) string {
	var b strings.Builder
	for i, field := range f.fields {
		label := s.label.Render(field.label)
		if field.optional {
			label += s.hint.Render(" optional")
		}
		b.WriteString(label)
		b.WriteString("\n")
		if field.kind == fieldBool {
			b.WriteString(renderBoolField(s, field.label, f.fieldBoolAt(i), i == f.focus))
			b.WriteString("\n\n")
			continue
		}
		b.WriteString(renderInputBox(s, f.inputs[i], i == f.focus, f.width))
		if field.hint != "" && field.kind != fieldBool {
			b.WriteString("\n")
			b.WriteString(s.hint.Render(field.hint))
		}
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}