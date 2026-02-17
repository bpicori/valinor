package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/bus"
)

// waitForBusEvent waits for the next event from the bus subscription and
// returns it as a busEventMsg for the Bubble Tea message loop.
func waitForBusEvent(sub *bus.Subscription) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-sub.Ch()
		if !ok {
			return nil
		}
		return busEventMsg{Event: ev}
	}
}
