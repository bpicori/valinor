package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/applog"
	"valinor/internal/bus"
)

// busEventMsg wraps a bus.Event for the Bubble Tea message loop.
type busEventMsg struct {
	Event bus.Event
}

// StartBusBridge subscribes to agent events and forwards them into Bubble Tea.
// Caller owns unsubscribing from the returned subscription.
func StartBusBridge(ctx context.Context, p *tea.Program, b *bus.Bus) *bus.Subscription {
	applog.Debug("tui bridge start", nil)
	sub := b.Subscribe(
		bus.EventUserMessage,
		bus.EventNewConversation,
		bus.EventChangeProviderModel,
		bus.EventAgentContentDelta,
		bus.EventAgentRefusal,
		bus.EventAgentToolCallStart,
		bus.EventAgentToolCallDone,
		bus.EventAgentDone,
		bus.EventAgentError,
	)

	go func() {
		for {
			select {
			case <-ctx.Done():
				applog.Debug("tui bridge shutdown", applog.M("reason", "context cancelled"))
				return
			case ev, ok := <-sub.Ch():
				if !ok {
					applog.Debug("tui bridge shutdown", applog.M("reason", "channel closed"))
					return
				}
				p.Send(busEventMsg{Event: ev})
			}
		}
	}()

	return sub
}
