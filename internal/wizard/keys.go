package wizard

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

var (
	keyQuit = key.NewBinding(key.WithKeys("ctrl+c"))
	keyNext = key.NewBinding(key.WithKeys("ctrl+n", "ctrl+j", "alt+enter"))
	keyTab  = key.NewBinding(key.WithKeys("tab"))
	keyBack = key.NewBinding(key.WithKeys("shift+tab"))
)

// isNextStepKey reports keys that advance to the next wizard step.
// Terminals usually send plain "enter" for ctrl+enter, so includeEnter
// enables enter on steps without text inputs (e.g. the provider list).
func isNextStepKey(msg tea.KeyMsg, includeEnter bool) bool {
	if key.Matches(msg, keyNext) {
		return true
	}
	if includeEnter && msg.String() == "enter" {
		return true
	}
	return false
}