package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/bus"
)

// busEventMsg wraps a bus.Event for the Bubble Tea message loop.
type busEventMsg struct {
	Event bus.Event
}

// waitForBusEvent waits for the next event from the subscription and
// forwards it into the Bubble Tea loop.
func waitForBusEvent(sub *bus.Subscription) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-sub.Ch()
		if !ok {
			return nil
		}
		return busEventMsg{Event: ev}
	}
}
