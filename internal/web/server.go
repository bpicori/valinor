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
	"valinor/internal/config"
	"valinor/internal/llm/factory"
	"valinor/internal/session"
)

//go:embed static/*
var staticFS embed.FS

// Server serves HTTP endpoints and websocket updates for the active session.
type Server struct {
	addr            string
	config          config.Config
	bus             *bus.Bus
	sessionManager  *session.Manager
	activeSessionID string

	httpServer *http.Server
	hub        *wsHub
	hubSub     *bus.Subscription
}

// NewServer creates a new web server instance.
func NewServer(host string, port int, cfg config.Config, b *bus.Bus, sm *session.Manager, activeSessionID string) *Server {
	addr := host + ":" + strconv.Itoa(port)
	return &Server{
		addr:            addr,
		config:          cfg,
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
	mux.HandleFunc("/api/providers", s.handleProvidersRoot)
	mux.HandleFunc("/api/providers/", s.handleProviderRoutes)
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
	case action == "model" && r.Method == http.MethodGet:
		s.handleGetSessionModel(w, r, sessionID)
	case action == "model" && r.Method == http.MethodPatch:
		s.handlePatchSessionModel(w, r, sessionID)
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

func (s *Server) handleProvidersRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	names := make([]string, 0, len(s.config.Providers))
	for name := range s.config.Providers {
		names = append(names, name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": names})
}

func (s *Server) handleProviderRoutes(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 || parts[1] != "models" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	providerName := parts[0]
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	s.handleProviderModels(w, r, providerName)
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request, providerName string) {
	providerCfg, ok := s.config.Providers[providerName]
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	provider, err := factory.CreateProvider(providerCfg, s.bus)
	if err != nil {
		applog.Error("web create provider error", applog.M("provider", providerName, "error", err))
		writeError(w, http.StatusInternalServerError, "failed to create provider")
		return
	}
	models, err := provider.ListModels(r.Context())
	if err != nil {
		applog.Error("web list models error", applog.M("provider", providerName, "error", err))
		writeError(w, http.StatusInternalServerError, "failed to list models")
		return
	}
	dtos := make([]modelInfoDTO, len(models))
	for i, m := range models {
		dtos[i] = modelInfoDTO{ID: m.ID, DisplayName: m.DisplayName}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": dtos})
}

func (s *Server) handleGetSessionModel(w http.ResponseWriter, r *http.Request, sessionID string) {
	meta, err := s.sessionManager.GetMetadata(r.Context(), sessionID)
	if err != nil || meta == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	provider := meta.Provider
	model := meta.Model
	if provider == "" {
		provider = s.config.DefaultProvider
	}
	if model == "" {
		if pc, ok := s.config.Providers[provider]; ok {
			model = pc.DefaultModel
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"provider": provider, "model": model})
}

func (s *Server) handlePatchSessionModel(w http.ResponseWriter, r *http.Request, sessionID string) {
	if sessionID != s.activeSessionID {
		writeError(w, http.StatusConflict, "selected session is not active")
		return
	}
	var req patchSessionModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		writeError(w, http.StatusBadRequest, "provider and model are required")
		return
	}
	if _, ok := s.config.Providers[provider]; !ok {
		writeError(w, http.StatusBadRequest, "provider not found")
		return
	}
	s.bus.Publish(bus.Event{
		Type:    bus.EventChangeProviderModel,
		Payload: bus.ChangeProviderModelPayload{Provider: provider, Model: model},
	})
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
