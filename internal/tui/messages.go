package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/bus"
)

// busEventMsg wraps a bus.Event for the Bubble Tea message loop.
type busEventMsg struct {
	Event bus.Event
}

// elapsedTickMsg is sent every second while waiting for the agent to update the elapsed time display.
type elapsedTickMsg struct{}

// elapsedTickCmd returns a command that sends elapsedTickMsg after 1 second.
func elapsedTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return elapsedTickMsg{}
	})
}

// errMsg represents a fatal error.
type errMsg struct {
	err error
}
