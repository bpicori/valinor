package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"

	"github.com/coder/websocket"
)

const (
	wsClientBufferSize = 64
	wsWriteTimeout     = 5 * time.Second
)

type wsHub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

func newWSHub() *wsHub {
	return &wsHub{
		clients: make(map[chan []byte]struct{}),
	}
}

func (h *wsHub) start(ctx context.Context, b *bus.Bus) *bus.Subscription {
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
		defer applog.Debug("web ws hub stopped", nil)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.Ch():
				if !ok {
					return
				}
				dto, err := toBusEventDTO(ev)
				if err != nil {
					applog.Warning("web ws drop invalid event", applog.M("error", err))
					continue
				}
				raw, err := json.Marshal(dto)
				if err != nil {
					applog.Warning("web ws marshal event failed", applog.M("error", err))
					continue
				}
				h.broadcast(raw)
			}
		}
	}()

	return sub
}

func (h *wsHub) broadcast(payload []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- payload:
		default:
			// Avoid letting one slow client block all others.
		}
	}
}

func (h *wsHub) addClient() chan []byte {
	ch := make(chan []byte, wsClientBufferSize)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *wsHub) removeClient(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

func (h *wsHub) handleWS(wctx context.Context, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Localhost app; disable origin checks for simplicity.
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	client := h.addClient()
	defer h.removeClient(client)

	for {
		select {
		case <-wctx.Done():
			return
		case msg, ok := <-client:
			if !ok {
				return
			}
			writeCtx, cancel := context.WithTimeout(wctx, wsWriteTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				return
			}
		}
	}
}
