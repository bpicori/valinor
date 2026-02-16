package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/agent"
)

// agentEventMsg wraps an agent.Event for the Bubble Tea message loop.
type agentEventMsg struct {
	Event agent.Event
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
