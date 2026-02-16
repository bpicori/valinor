package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/agent"
)

// startAgentCmd launches the agent in a goroutine and returns the first event.
// Subsequent events are received by chaining waitForAgentEvent commands.
func startAgentCmd(ag *agent.Agent, input string, eventCh chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		go func() {
			_ = ag.Send(context.Background(), input, func(ev agent.Event) {
				eventCh <- ev
			})
		}()
		return agentEventMsg{Event: <-eventCh}
	}
}

// waitForAgentEvent waits for the next event from the channel.
func waitForAgentEvent(eventCh chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		return agentEventMsg{Event: <-eventCh}
	}
}
