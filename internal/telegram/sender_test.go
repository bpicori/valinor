package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSendNewMessageRetriesPlainTextOnParseError(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		var req SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		attempt := atomic.AddInt32(&attempts, 1)
		switch attempt {
		case 1:
			if req.ParseMode != "HTML" {
				t.Fatalf("expected first attempt to use HTML parse mode, got %q", req.ParseMode)
			}
			if req.Text != "<b>broken</b>" {
				t.Fatalf("expected first attempt to use formatted text, got %q", req.Text)
			}
			_ = json.NewEncoder(w).Encode(APIResponse{
				OK:          false,
				ErrorCode:   400,
				Description: "Bad Request: can't parse entities: unsupported start tag",
			})
		case 2:
			if req.ParseMode != "" {
				t.Fatalf("expected fallback attempt without parse mode, got %q", req.ParseMode)
			}
			if req.Text != "plain fallback" {
				t.Fatalf("expected fallback text, got %q", req.Text)
			}
			_ = json.NewEncoder(w).Encode(APIResponse{
				OK:     true,
				Result: json.RawMessage(`{"message_id":123}`),
			})
		default:
			t.Fatalf("unexpected extra attempt %d", attempt)
		}
	}))
	defer server.Close()

	client := NewClient("token")
	client.endpoint = server.URL + "/"

	bot := &Bot{
		client: client,
		ctx:    context.Background(),
	}

	result := &RenderResult{
		Text:         "<b>broken</b>",
		ParseMode:    "HTML",
		FallbackText: "plain fallback",
	}

	if err := bot.sendNewMessage(ChatIdentity{ChatID: 42}, &messageTracker{}, result); err != nil {
		t.Fatalf("sendNewMessage returned error: %v", err)
	}

	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 send attempts, got %d", got)
	}
}
