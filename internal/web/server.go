package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/session"
)

//go:embed static/*
var staticFS embed.FS

// Server serves HTTP endpoints and websocket updates for the active session.
type Server struct {
	addr            string
	bus             *bus.Bus
	sessionManager  *session.Manager
	activeSessionID string

	httpServer *http.Server
	hub        *wsHub
	hubSub     *bus.Subscription
}

// NewServer creates a new web server instance.
func NewServer(host string, port int, b *bus.Bus, sm *session.Manager, activeSessionID string) *Server {
	addr := host + ":" + strconv.Itoa(port)
	return &Server{
		addr:            addr,
		bus:             b,
		sessionManager:  sm,
		activeSessionID: activeSessionID,
		hub:             newWSHub(),
	}
}

// Start runs the web server until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("load embedded static files: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/active-session", s.handleActiveSession)
	mux.HandleFunc("/api/sessions", s.handleSessionsRoot)
	mux.HandleFunc("/api/sessions/", s.handleSessionRoutes)
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		s.hub.handleWS(ctx, w, r)
	})
	mux.Handle("/", http.FileServer(http.FS(staticRoot)))

	s.httpServer = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.hubSub = s.hub.start(ctx, s.bus)

	go func() {
		<-ctx.Done()
		if s.hubSub != nil {
			s.bus.Unsubscribe(s.hubSub)
			s.hubSub = nil
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil && err != http.ErrServerClosed {
			applog.Warning("web server shutdown error", applog.M("error", err))
		}
	}()

	go func() {
		applog.Info("web server started", applog.M("addr", s.addr))
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			applog.Error("web server error", applog.M("error", err))
		}
	}()

	return nil
}

func (s *Server) handleActiveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": s.activeSessionID})
}

func (s *Server) handleSessionsRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	list, err := s.sessionManager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list sessions failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": toSessionDTOs(list),
	})
}

func (s *Server) handleSessionRoutes(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	sessionID := parts[0]
	action := parts[1]
	switch {
	case action == "messages" && r.Method == http.MethodGet:
		s.handleSessionMessages(w, r, sessionID)
	case action == "messages" && r.Method == http.MethodPost:
		s.handlePostMessage(w, r, sessionID)
	case action == "new" && r.Method == http.MethodPost:
		s.handleNewConversation(w, sessionID)
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request, sessionID string) {
	events, err := s.sessionManager.LoadEvents(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	messages, err := toTimelineMessages(events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decode session messages failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"messages":   messages,
		"active":     sessionID == s.activeSessionID,
	})
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if sessionID != s.activeSessionID {
		writeError(w, http.StatusConflict, "selected session is not active")
		return
	}
	var req postMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	s.bus.Publish(bus.Event{
		Type:    bus.EventUserMessage,
		Payload: bus.UserMessagePayload{Content: content},
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleNewConversation(w http.ResponseWriter, sessionID string) {
	if sessionID != s.activeSessionID {
		writeError(w, http.StatusConflict, "selected session is not active")
		return
	}
	s.bus.Publish(bus.Event{Type: bus.EventNewConversation})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
