// Package telegram provides the main bot orchestrator.
package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"valinor/internal/config"
	"valinor/internal/event"
	"valinor/internal/gateway"
	"valinor/internal/llm"
	"valinor/internal/logger"
)

// Bot is the main Telegram bot orchestrator.
// It coordinates the poller, bridge, and renderer through a single event loop.
type Bot struct {
	// Configuration
	config Config

	// Components
	client   *Client
	store    *offsetStore
	poller   *Poller
	bridges  map[string]*Bridge
	renderer *Renderer

	// Shared event channel - all bridges push events here
	events chan chatEvent

	// Per-chat message trackers (single goroutine access, no mutex needed)
	trackers map[int64]*messageTracker

	// Per-chat model picker state (single goroutine access, no mutex needed)
	modelPickers map[int64]*modelPicker

	// Per-chat pending ask_user questions (single goroutine access, no mutex needed)
	pendingQuestions map[int64]map[string]pendingQuestion

	bridgesMu sync.RWMutex

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ready  atomic.Bool
}

// Config holds the Telegram bot configuration.
type Config struct {
	// BotToken is the Telegram Bot API token (required)
	BotToken string
	// GatewayURL is the WebSocket URL for the Valinor gateway (required)
	GatewayURL string
	// StorageDir is the directory for persisting offset (optional, defaults to ~/.valinor)
	StorageDir string
	// WorkspaceDir is where downloaded attachments are saved.
	WorkspaceDir string
	// PollTimeout is the long-polling timeout in seconds (default: 30)
	PollTimeout int
	// Providers is the configured LLM providers for model selection.
	Providers map[string]config.ProviderConfig
	// Transcriber transcribes voice messages when configured; nil when disabled.
	Transcriber llm.AudioTranscriber
	// TranscriptionModel is the STT model slug from the transcription-enabled provider.
	TranscriptionModel string
}

const voiceTranscriptionDisabledMessage = "Voice messages are not enabled. Set transcription_enabled = true and transcription_model = \"openai/whisper-1\" (or another STT model) on your provider in config.toml."

// NewBot creates a new Telegram bot instance.
func NewBot(cfg Config) (*Bot, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("bot token is required")
	}
	if cfg.GatewayURL == "" {
		return nil, fmt.Errorf("gateway URL is required")
	}

	return &Bot{
		config:           cfg,
		bridges:          make(map[string]*Bridge),
		events:           make(chan chatEvent, 100),
		trackers:         make(map[int64]*messageTracker),
		modelPickers:     make(map[int64]*modelPicker),
		pendingQuestions: make(map[int64]map[string]pendingQuestion),
	}, nil
}

const questionTTL = 10 * time.Minute

type pendingQuestion struct {
	Question  string
	Options   []string
	CreatedAt time.Time
}

// Start initializes and starts all bot components.
func (b *Bot) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	// Initialize API client
	b.client = NewClient(b.config.BotToken)

	// Verify bot token by calling getMe
	me, err := b.client.GetMe(b.ctx)
	if err != nil {
		return fmt.Errorf("verify bot token: %w", err)
	}
	logger.Info("telegram bot: authenticated",
		logger.BuildMetadata("username", me.Username, "id", me.ID))

	// Initialize offset store
	store, err := newOffsetStore(b.config.StorageDir)
	if err != nil {
		return fmt.Errorf("initialize offset store: %w", err)
	}
	b.store = store

	// Initialize stateless renderer
	b.renderer = NewRenderer()

	// Initialize poller with persisted offset
	b.poller = NewPoller(b.client, DefaultPollLimit, b.store.Get())
	if b.config.PollTimeout > 0 {
		b.poller.SetPollTimeout(b.config.PollTimeout)
	}

	// Start poller and bot main loop
	b.poller.Start(b.ctx)
	b.wg.Add(1)
	go b.run()

	b.ready.Store(true)
	logger.Info("telegram bot: started", nil)

	return nil
}

// Stop gracefully stops all bot components.
func (b *Bot) Stop() {
	if !b.ready.Load() {
		return
	}

	logger.Info("telegram bot: stopping", nil)

	b.cancel()

	// Stop in reverse order of initialization
	if b.poller != nil {
		b.poller.Stop()
	}
	b.stopAllBridges()

	// Close events channel to signal run() to exit
	close(b.events)

	b.wg.Wait()
	b.ready.Store(false)

	logger.Info("telegram bot: stopped", nil)
}

// run is the single main event loop that processes both Telegram updates and gateway events.
// This ensures all per-chat state access is naturally serialized.
func (b *Bot) run() {
	defer b.wg.Done()

	for {
		select {
		case <-b.ctx.Done():
			return
		case update, ok := <-b.poller.Updates:
			if !ok {
				return
			}
			b.persistOffset(update)
			b.onUpdate(update)
		case ev, ok := <-b.events:
			if !ok {
				return
			}
			b.onEvent(ev)
		}
	}
}

// persistOffset saves the update ID to avoid reprocessing on restart.
func (b *Bot) persistOffset(update Update) {
	if err := b.store.Set(update.UpdateID); err != nil {
		logger.Warning("telegram bot: failed to persist offset",
			logger.BuildMetadata("error", err, "update_id", update.UpdateID))
	}
}

// getOrCreateTracker returns the message tracker for a chat, creating it if needed.
func (b *Bot) getOrCreateTracker(chatID int64) *messageTracker {
	if t, ok := b.trackers[chatID]; ok {
		return t
	}
	t := &messageTracker{}
	b.trackers[chatID] = t
	return t
}

// onUpdate handles a Telegram update from the poller.
func (b *Bot) onUpdate(update Update) {
	if update.CallbackQuery != nil {
		if err := b.processCallbackQuery(update.CallbackQuery); err != nil {
			logger.Error("telegram bot: failed to process callback",
				logger.BuildMetadata("error", err, "update_id", update.UpdateID))
		}
		return
	}

	if msg := extractMessage(update); msg != nil {
		if msg.Voice != nil {
			if err := b.processVoiceMessage(msg, update.UpdateID); err != nil {
				logger.Error("telegram bot: failed to process voice message",
					logger.BuildMetadata("error", err, "update_id", update.UpdateID))
			}
			return
		}
		if len(msg.Photo) > 0 || msg.Document != nil {
			if err := b.processAttachmentMessage(msg, update.UpdateID); err != nil {
				logger.Error("telegram bot: failed to process attachment",
					logger.BuildMetadata("error", err, "update_id", update.UpdateID))
			}
			return
		}
	}

	cmd, err := parseUpdate(update, b.config.Providers)
	if err != nil {
		logger.Error("telegram bot: failed to parse update",
			logger.BuildMetadata("error", err, "update_id", update.UpdateID))
		return
	}
	if cmd == nil {
		return
	}

	tracker := b.getOrCreateTracker(cmd.Identity.ChatID)

	if err := b.processCommand(cmd, tracker); err != nil {
		logger.Error("telegram bot: failed to process command",
			logger.BuildMetadata("error", err, "chat_id", cmd.Identity.ChatID))
	}
}

// onEvent handles a gateway event from a bridge.
func (b *Bot) onEvent(ev chatEvent) {
	if ev.identity.ChatID == 0 {
		logger.Warning("telegram bot: received event for unknown chat",
			logger.BuildMetadata("request_id", ev.event.RequestID, "event_type", ev.event.Type))
		return
	}

	tracker := b.getOrCreateTracker(ev.identity.ChatID)

	if err := b.processEvent(ev, tracker); err != nil {
		logger.Error("telegram bot: failed to process event",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
}

// processVoiceMessage downloads and transcribes a Telegram voice message.
func (b *Bot) processVoiceMessage(msg *Message, updateID int64) error {
	if msg == nil || msg.Voice == nil || msg.Chat == nil {
		return nil
	}

	identity := FromTelegramUpdate(msg.Chat)
	tracker := b.getOrCreateTracker(identity.ChatID)

	if b.config.Transcriber == nil || b.config.TranscriptionModel == "" {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{Text: voiceTranscriptionDisabledMessage})
	}

	if tracker.isBusy() {
		notice := &RenderResult{
			Text:      "\u23f3 Please wait for the current response to complete\\.",
			ParseMode: "MarkdownV2",
		}
		return b.sendTelegramMessage(identity, tracker, notice)
	}

	if err := b.sendTypingAction(identity, tracker); err != nil {
		logger.Warning("telegram bot: failed to send typing action for voice",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}

	file, err := b.client.GetFile(b.ctx, msg.Voice.FileID)
	if err != nil {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Could not fetch the voice message. Please try again.",
		})
	}

	audio, err := b.client.DownloadFile(b.ctx, file.FilePath)
	if err != nil {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Could not download the voice message. Please try again.",
		})
	}

	transcript, err := b.config.Transcriber.TranscribeAudio(b.ctx, llm.AudioTranscribeParams{
		Model:  b.config.TranscriptionModel,
		Audio:  audio,
		Format: "ogg",
	})
	if err != nil {
		logger.Error("telegram bot: voice transcription failed",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Could not transcribe the voice message. Please try again.",
		})
	}

	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)
	cmd := &Command{
		Identity:  identity,
		RequestID: requestID,
		Type:      gateway.CommandUserMessage,
		Payload: gateway.UserMessagePayload{
			Content: transcript,
		},
		RawText: transcript,
	}

	return b.processCommand(cmd, tracker)
}

const maxTelegramAttachmentBytes = 20 << 20

// processAttachmentMessage downloads a photo or document into the workspace and
// sends the model a user message pointing at it.
func (b *Bot) processAttachmentMessage(msg *Message, updateID int64) error {
	if msg == nil || msg.Chat == nil {
		return nil
	}

	identity := FromTelegramUpdate(msg.Chat)
	tracker := b.getOrCreateTracker(identity.ChatID)

	if tracker.isBusy() {
		notice := &RenderResult{
			Text:      "\u23f3 Please wait for the current response to complete\\.",
			ParseMode: "MarkdownV2",
		}
		return b.sendTelegramMessage(identity, tracker, notice)
	}
	if strings.TrimSpace(b.config.WorkspaceDir) == "" {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Attachment uploads are not configured.",
		})
	}

	fileID, fileName, mimeType, isImage := attachmentFromMessage(msg)
	if fileID == "" {
		return nil
	}

	if err := b.sendTypingAction(identity, tracker); err != nil {
		logger.Warning("telegram bot: failed to send typing action for attachment",
			logger.BuildMetadata("error", err, "chat_id", identity.ChatID))
	}

	file, err := b.client.GetFile(b.ctx, fileID)
	if err != nil {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Could not fetch the attachment. Please try again.",
		})
	}

	data, err := b.client.DownloadFile(b.ctx, file.FilePath)
	if err != nil {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: "Could not download the attachment. Please try again.",
		})
	}
	if len(data) > maxTelegramAttachmentBytes {
		return b.sendTelegramMessage(identity, tracker, &RenderResult{
			Text: fmt.Sprintf("The attachment is larger than %d MB.", maxTelegramAttachmentBytes/(1<<20)),
		})
	}

	ext := attachmentExtension(file.FilePath, fileName, mimeType)
	base := sanitizeAttachmentName(file.FileUniqueID)
	if base == "" {
		base = "file"
	}
	relPath := filepath.ToSlash(filepath.Join("uploads", fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext)))
	fullPath := filepath.Join(b.config.WorkspaceDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("create uploads directory: %w", err)
	}
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return fmt.Errorf("save attachment: %w", err)
	}

	var content string
	if isImage {
		content = fmt.Sprintf("The user sent an image saved at %q. Use read_image with that path to view it.", relPath)
	} else {
		content = fmt.Sprintf("The user sent a file saved at %q (%d bytes).", relPath, len(data))
	}
	if caption := strings.TrimSpace(msg.Caption); caption != "" {
		content += "\nCaption: " + caption
	}

	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)
	cmd := &Command{
		Identity:  identity,
		RequestID: requestID,
		Type:      gateway.CommandUserMessage,
		Payload: gateway.UserMessagePayload{
			Content: content,
		},
		RawText: content,
	}
	return b.processCommand(cmd, tracker)
}

// attachmentFromMessage selects the file to download. Telegram sends several
// photo sizes; the last entry is the largest.
func attachmentFromMessage(msg *Message) (fileID, fileName, mimeType string, isImage bool) {
	if len(msg.Photo) > 0 {
		largest := msg.Photo[len(msg.Photo)-1]
		return largest.FileID, "", "", true
	}
	if msg.Document != nil {
		doc := msg.Document
		mimeType = strings.ToLower(doc.MimeType)
		isImage = strings.HasPrefix(mimeType, "image/") || isImageExtension(doc.FileName)
		return doc.FileID, doc.FileName, mimeType, isImage
	}
	return "", "", "", false
}

func attachmentExtension(filePath, fileName, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(fileName))
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".pdf", ".docx", ".xlsx", ".pptx", ".txt", ".md", ".csv", ".zip":
		return ext
	}
	switch strings.ToLower(mimeType) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	}
	return ".bin"
}

func isImageExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func sanitizeAttachmentName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// processCommand handles a user command.
func (b *Bot) processCommand(cmd *Command, tracker *messageTracker) error {
	bridge, err := b.getOrCreateBridge(cmd.Identity)
	if err != nil {
		return fmt.Errorf("get bridge for chat: %w", err)
	}

	// Handle model picker and direct model switch before generic control dispatch.
	switch cmd.Type {
	case commandNewSession:
		return b.createNewSession(cmd, bridge)
	case commandModelPickerOpen:
		return b.openModelPicker(cmd.Identity, tracker, "")
	case commandModelPickerProvider:
		provider, _ := cmd.Payload.(string)
		return b.openModelPicker(cmd.Identity, tracker, provider)
	case commandModelPickerError:
		message, _ := cmd.Payload.(string)
		return b.sendTelegramMessage(cmd.Identity, tracker, pickerRenderResult(message, nil))
	case gateway.CommandChangeProviderModel:
		return b.applyDirectModelSwitch(cmd, bridge)
	}

	// Handle control commands directly
	if cmd.IsControlCommand {
		if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
			return fmt.Errorf("failed to send control command")
		}
		return nil
	}

	// Handle status command locally
	if cmd.Type == "status" {
		sessionKey, provider, model, contextTokens, tokensEstimated, _ := bridge.SessionInfo()
		result := b.renderer.RenderStatus(sessionKey, provider, model, contextTokens, tokensEstimated)
		return b.sendTelegramMessage(cmd.Identity, tracker, result)
	}

	// Check if chat is busy
	if tracker.isBusy() {
		notice := &RenderResult{
			Text:      "\u23f3 Please wait for the current response to complete\\.",
			ParseMode: "MarkdownV2",
		}
		return b.sendTelegramMessage(cmd.Identity, tracker, notice)
	}

	// Finalize any existing streaming before starting new conversation
	if cmd.Type == gateway.CommandNewConversation {
		tracker.markIdle()
	}

	// Mark as busy before sending
	tracker.markBusy(cmd.RequestID)

	if !bridge.SendCommand(cmd.Type, cmd.Payload, cmd.RequestID) {
		tracker.markIdle()
		return fmt.Errorf("failed to send command")
	}

	return nil
}

// processEvent handles a gateway event.
func (b *Bot) processEvent(ev chatEvent, tracker *messageTracker) error {
	switch ev.event.Type {
	case event.AgentContentDelta:
		return b.handleContentDelta(ev, tracker)
	case event.AgentContentFull:
		return b.handleContentFull(ev, tracker)
	case event.AgentImage:
		return b.handleImage(ev, tracker)
	case event.AgentFile:
		return b.handleFile(ev, tracker)
	case event.AgentQuestion:
		return b.handleQuestion(ev, tracker)
	case event.AgentDone:
		return b.handleAgentDone(ev, tracker)
	case event.AgentError:
		return b.handleAgentError(ev, tracker)
	case event.AgentCancelled:
		return b.handleAgentCancelled(ev, tracker)
	case event.SessionAttached:
		return b.handleSessionAttached(ev, tracker)
	case event.NewConversation:
		return b.handleNewConversation(ev, tracker)
	case event.ChangeModelEvent:
		return b.handleModelChanged(ev, tracker)
	case event.ProviderModelsResult:
		return b.handleProviderModelsResult(ev, tracker)
	case event.CronJobResult:
		return b.handleCronJobResult(ev, tracker)
	case event.AgentThinking:
		return b.handleThinking(ev, tracker)
	default:
		// Other events (tool calls, tokens) - render if needed
		result := b.renderer.RenderEvent(ev.event)
		if result != nil {
			return b.sendTelegramMessage(ev.identity, tracker, result)
		}
		return nil
	}
}

// handleThinking appends tool activity to the in-place status message.
func (b *Bot) handleThinking(ev chatEvent, tracker *messageTracker) error {
	payload, ok := ev.event.Payload.(gateway.ThinkingPayload)
	if !ok {
		return nil
	}

	line := b.renderer.renderThinkingLine(payload)
	if line == "" {
		return nil
	}

	tracker.appendThinkingLine(line)
	result := b.renderer.renderThinkingStatus(tracker.getThinkingLines())
	if result == nil {
		return nil
	}
	return b.sendTelegramMessage(ev.identity, tracker, result)
}

// handleContentDelta accumulates streamed content.
func (b *Bot) handleContentDelta(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ContentDeltaPayload); ok {
		tracker.appendContent(payload.Content)
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleContentFull replaces accumulated content.
func (b *Bot) handleContentFull(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ContentFullPayload); ok {
		tracker.contentBuf.Reset()
		tracker.appendContent(payload.Content)
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleImage sends a generated image payload immediately.
func (b *Bot) handleImage(ev chatEvent, tracker *messageTracker) error {
	if payload, ok := ev.event.Payload.(gateway.ImagePayload); ok {
		if err := b.sendTelegramMessage(ev.identity, tracker, &RenderResult{
			Images:  []gateway.ImagePayload{payload},
			IsFinal: true,
		}); err != nil {
			return fmt.Errorf("send image message: %w", err)
		}
	}
	if err := b.sendTypingAction(ev.identity, tracker); err != nil {
		logger.Warning("telegram bot: send typing action failed",
			logger.BuildMetadata("error", err, "chat_id", ev.identity.ChatID))
	}
	return nil
}

// handleFile sends a workspace file as a Telegram document.
func (b *Bot) handleFile(ev chatEvent, tracker *messageTracker) error {
	payload, ok := ev.event.Payload.(gateway.FilePayload)
	if !ok {
		return nil
	}

	path, ok := safeWorkspaceFilePath(b.config.WorkspaceDir, payload.Path)
	if !ok {
		logger.Warning("telegram bot: rejected file path",
			logger.BuildMetadata("path", payload.Path, "chat_id", ev.identity.ChatID))
		return b.sendTelegramMessage(ev.identity, tracker, &RenderResult{Text: "Could not send the file: invalid workspace path."})
	}

	data, err := os.ReadFile(path)
	if err != nil {
		logger.Warning("telegram bot: read file failed",
			logger.BuildMetadata("path", path, "error", err, "chat_id", ev.identity.ChatID))
		return b.sendTelegramMessage(ev.identity, tracker, &RenderResult{Text: "Could not read the file."})
	}
	if len(data) > maxSendDocumentBytes {
		return b.sendTelegramMessage(ev.identity, tracker, &RenderResult{
			Text: fmt.Sprintf("The file is larger than %d MB.", maxSendDocumentBytes/(1<<20)),
		})
	}

	fileName := payload.FileName
	if fileName == "" {
		fileName = filepath.Base(path)
	}

	if _, err := b.client.SendDocument(b.ctx, SendDocumentRequest{
		ChatID:       ev.identity.ChatID,
		DocumentName: fileName,
		DocumentData: data,
		Caption:      payload.Caption,
	}); err != nil {
		logger.Warning("telegram bot: send document failed",
			logger.BuildMetadata("path", path, "file_name", fileName, "error", err, "chat_id", ev.identity.ChatID))
		return b.sendTelegramMessage(ev.identity, tracker, &RenderResult{Text: "Could not send the file."})
	}

	logger.Debug("telegram bot: document sent",
		logger.BuildMetadata("chat_id", ev.identity.ChatID, "file_name", fileName, "bytes", len(data)))
	return nil
}

// handleQuestion sends a structured question with inline option buttons.
func (b *Bot) handleQuestion(ev chatEvent, tracker *messageTracker) error {
	payload, ok := ev.event.Payload.(gateway.QuestionPayload)
	if !ok || payload.QuestionID == "" || strings.TrimSpace(payload.Question) == "" {
		return nil
	}

	if len(payload.Options) > 0 {
		questions := b.pendingQuestions[ev.identity.ChatID]
		if questions == nil {
			questions = make(map[string]pendingQuestion)
			b.pendingQuestions[ev.identity.ChatID] = questions
		}
		questions[payload.QuestionID] = pendingQuestion{
			Question:  payload.Question,
			Options:   append([]string(nil), payload.Options...),
			CreatedAt: time.Now(),
		}
	}

	text := payload.Question
	var markup *InlineKeyboardMarkup
	if len(payload.Options) > 0 {
		text += "\n\nChoose an answer:"
		rows := make([][]InlineKeyboardButton, 0, len(payload.Options))
		for i, option := range payload.Options {
			rows = append(rows, []InlineKeyboardButton{{
				Text:         truncateButtonText(option),
				CallbackData: fmt.Sprintf("ask:%s:%d", payload.QuestionID, i),
			}})
		}
		markup = &InlineKeyboardMarkup{InlineKeyboard: rows}
	} else if payload.AllowFreeText {
		text += "\n\nType your answer."
	}

	return b.sendTelegramMessage(ev.identity, tracker, &RenderResult{
		Text:        text,
		ReplyMarkup: markup,
	})
}

// handleQuestionCallback resolves an ask_user button click into a user message.
func (b *Bot) handleQuestionCallback(query *CallbackQuery) (bool, error) {
	if query == nil || !strings.HasPrefix(query.Data, "ask:") {
		return false, nil
	}
	if query.Message == nil || query.Message.Chat == nil {
		_ = b.answerCallback(query.ID, "")
		return true, nil
	}

	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 {
		_ = b.answerCallback(query.ID, "Invalid answer")
		return true, nil
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil {
		_ = b.answerCallback(query.ID, "Invalid answer")
		return true, nil
	}

	chatID := query.Message.Chat.ID
	questions := b.pendingQuestions[chatID]
	pending, ok := questions[parts[1]]
	if !ok || time.Since(pending.CreatedAt) > questionTTL {
		delete(questions, parts[1])
		_ = b.answerCallback(query.ID, "Question expired")
		return true, nil
	}
	if index < 0 || index >= len(pending.Options) {
		_ = b.answerCallback(query.ID, "Invalid answer")
		return true, nil
	}

	identity := FromTelegramUpdate(query.Message.Chat)
	tracker := b.getOrCreateTracker(chatID)
	if tracker.isBusy() {
		_ = b.answerCallback(query.ID, "Please wait for the current response.")
		return true, nil
	}

	delete(questions, parts[1])
	if len(questions) == 0 {
		delete(b.pendingQuestions, chatID)
	}

	option := pending.Options[index]
	_ = b.answerCallback(query.ID, "Answer sent")
	if query.Message.MessageID != 0 {
		_, _ = b.client.EditMessageText(b.ctx, EditMessageTextRequest{
			ChatID:    chatID,
			MessageID: query.Message.MessageID,
			Text:      pending.Question + "\n\nAnswer: " + option,
		})
	}

	requestID := fmt.Sprintf("tg_ask_%d_%d", chatID, time.Now().UnixNano())
	cmd := &Command{
		Identity:  identity,
		RequestID: requestID,
		Type:      gateway.CommandUserMessage,
		Payload: gateway.UserMessagePayload{
			Content: "Answer: " + option,
		},
		RawText: "Answer: " + option,
	}
	return true, b.processCommand(cmd, tracker)
}

const maxSendDocumentBytes = 50 << 20

func safeWorkspaceFilePath(workspaceDir, path string) (string, bool) {
	if strings.TrimSpace(workspaceDir) == "" {
		return "", false
	}
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if cleaned == "" || cleaned == "." || filepath.IsAbs(cleaned) ||
		strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/..") {
		return "", false
	}

	workspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", false
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, filepath.FromSlash(cleaned)))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return resolved, true
}

// handleAgentDone processes the final agent completion.
func (b *Bot) handleAgentDone(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderFinalContent(tracker.getContent())
	tracker.markIdle()

	if result != nil {
		if err := b.sendTelegramMessage(ev.identity, tracker, result); err != nil {
			return fmt.Errorf("send final message: %w", err)
		}
	}
	return nil
}

// handleAgentError processes agent errors.
func (b *Bot) handleAgentError(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleAgentCancelled processes agent cancellation.
func (b *Bot) handleAgentCancelled(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleSessionAttached renders session attachment notification.
func (b *Bot) handleSessionAttached(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleNewConversation renders new conversation confirmation.
func (b *Bot) handleNewConversation(ev chatEvent, tracker *messageTracker) error {
	tracker.markIdle()
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleModelChanged renders model change notification.
func (b *Bot) handleModelChanged(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

// handleCronJobResult sends scheduled job results to the chat.
func (b *Bot) handleCronJobResult(ev chatEvent, tracker *messageTracker) error {
	result := b.renderer.RenderEvent(ev.event)
	if result != nil {
		return b.sendTelegramMessage(ev.identity, tracker, result)
	}
	return nil
}

func (b *Bot) getOrCreateModelPicker(chatID int64) *modelPicker {
	if picker, ok := b.modelPickers[chatID]; ok {
		return picker
	}
	picker := &modelPicker{}
	b.modelPickers[chatID] = picker
	return picker
}

func (b *Bot) clearModelPicker(chatID int64) {
	delete(b.modelPickers, chatID)
}

func (b *Bot) openModelPicker(identity ChatIdentity, tracker *messageTracker, provider string) error {
	bridge, err := b.getOrCreateBridge(identity)
	if err != nil {
		return fmt.Errorf("get bridge for model picker: %w", err)
	}

	_, currentProvider, _, _, _, _ := bridge.SessionInfo()
	picker := b.getOrCreateModelPicker(identity.ChatID)

	if provider == "" {
		picker.step = pickerStepProvider
		picker.provider = ""
		picker.models = nil
		picker.page = 0
		picker.requestID = ""
		result := pickerRenderResult(picker.title(), buildProviderKeyboard(b.config.Providers, currentProvider))
		return b.sendPickerMessage(identity, picker, result)
	}

	if _, ok := b.config.Providers[provider]; !ok {
		return b.sendTelegramMessage(identity, tracker, pickerRenderResult(fmt.Sprintf("Unknown provider: %s", provider), nil))
	}

	picker.step = pickerStepLoading
	picker.provider = provider
	picker.models = nil
	picker.page = 0
	picker.requestID = fmt.Sprintf("tg_mdl_%d_%d", identity.ChatID, time.Now().UnixNano())

	result := pickerRenderResult(picker.title(), &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "Cancel", CallbackData: callbackCancel},
		}},
	})
	if err := b.sendPickerMessage(identity, picker, result); err != nil {
		return err
	}

	if !sendRequestProviderModels(bridge, provider, picker.requestID) {
		b.clearModelPicker(identity.ChatID)
		return fmt.Errorf("failed to request provider models")
	}

	return nil
}

func (b *Bot) createNewSession(cmd *Command, bridge *Bridge) error {
	_, provider, model, _, _, ok := bridge.SessionInfo()
	if !ok {
		cfg := config.Must()
		provider = cfg.DefaultProvider
		if pc, exists := cfg.Providers[provider]; exists {
			model = pc.DefaultModel
		}
	}

	if !bridge.SendCommand(gateway.CommandCreateNewSession, gateway.CreateNewSessionPayload{
		Provider: provider,
		Model:    model,
		Force:    true,
	}, cmd.RequestID) {
		return fmt.Errorf("failed to send create_new_session command")
	}
	return nil
}

func (b *Bot) applyDirectModelSwitch(cmd *Command, bridge *Bridge) error {
	payload, ok := cmd.Payload.(gateway.ChangeProviderModelPayload)
	if !ok {
		return fmt.Errorf("invalid change_provider_model payload")
	}

	_, currentProvider, _, _, _, _ := bridge.SessionInfo()
	if !sendModelSwitch(bridge, currentProvider, payload.Provider, payload.Model, cmd.RequestID) {
		return fmt.Errorf("failed to send model switch command")
	}

	if payload.Provider == currentProvider {
		return b.sendTelegramMessage(cmd.Identity, b.getOrCreateTracker(cmd.Identity.ChatID),
			pickerRenderResult(pickerConfirmationText(payload.Provider, payload.Model), nil))
	}
	return nil
}

func (b *Bot) processCallbackQuery(query *CallbackQuery) error {
	if query == nil || query.Message == nil || query.Message.Chat == nil {
		return nil
	}

	if handled, err := b.handleQuestionCallback(query); handled {
		return err
	}

	action, arg, ok := parsePickerCallback(query.Data)
	if !ok {
		return b.answerCallback(query.ID, "")
	}

	identity := FromTelegramUpdate(query.Message.Chat)
	tracker := b.getOrCreateTracker(identity.ChatID)
	picker := b.modelPickers[identity.ChatID]

	bridge, err := b.getOrCreateBridge(identity)
	if err != nil {
		_ = b.answerCallback(query.ID, "Connection error")
		return fmt.Errorf("get bridge for callback: %w", err)
	}

	_, currentProvider, currentModel, _, _, _ := bridge.SessionInfo()

	switch action {
	case "cancel":
		b.clearModelPicker(identity.ChatID)
		if picker != nil && picker.messageID != 0 {
			_ = b.sendPickerMessage(identity, picker, pickerRenderResult("Model selection cancelled", nil))
		}
		return b.answerCallback(query.ID, "")

	case "back":
		if picker == nil {
			return b.answerCallback(query.ID, "")
		}
		picker.step = pickerStepProvider
		picker.provider = ""
		picker.models = nil
		picker.page = 0
		picker.requestID = ""
		if err := b.sendPickerMessage(identity, picker, pickerRenderResult(picker.title(), buildProviderKeyboard(b.config.Providers, currentProvider))); err != nil {
			_ = b.answerCallback(query.ID, "Failed to update picker")
			return err
		}
		return b.answerCallback(query.ID, "")

	case "page":
		if picker == nil || picker.step != pickerStepModel {
			return b.answerCallback(query.ID, "")
		}
		page, err := strconv.Atoi(arg)
		if err != nil {
			return b.answerCallback(query.ID, "")
		}
		picker.page = page
		markup := buildModelKeyboard(picker.models, picker.page, currentProvider, currentModel)
		if err := b.sendPickerMessage(identity, picker, pickerRenderResult(picker.title(), markup)); err != nil {
			_ = b.answerCallback(query.ID, "Failed to update picker")
			return err
		}
		return b.answerCallback(query.ID, "")

	case "provider":
		if _, ok := b.config.Providers[arg]; !ok {
			_ = b.answerCallback(query.ID, "Unknown provider")
			return nil
		}
		return b.openModelPicker(identity, tracker, arg)

	case "model":
		if picker == nil || picker.step != pickerStepModel {
			return b.answerCallback(query.ID, "")
		}
		idx, err := strconv.Atoi(arg)
		if err != nil || idx < 0 || idx >= len(picker.models) {
			return b.answerCallback(query.ID, "Invalid model")
		}

		selected := picker.models[idx]
		requestID := fmt.Sprintf("tg_mdl_%d_%d", identity.ChatID, time.Now().UnixNano())
		if !sendModelSwitch(bridge, currentProvider, selected.Provider, selected.Model, requestID) {
			_ = b.answerCallback(query.ID, "Failed to switch model")
			return fmt.Errorf("failed to send model switch command")
		}

		confirm := pickerConfirmationText(selected.Provider, selected.Model)
		if selected.Provider != currentProvider {
			confirm = fmt.Sprintf("Switching to %s / %s", selected.Provider, selected.Model)
		}
		_ = b.sendPickerMessage(identity, picker, pickerRenderResult(confirm, nil))
		b.clearModelPicker(identity.ChatID)
		return b.answerCallback(query.ID, "")

	default:
		return b.answerCallback(query.ID, "")
	}
}

func (b *Bot) handleProviderModelsResult(ev chatEvent, tracker *messageTracker) error {
	payload, ok := ev.event.Payload.(gateway.ProviderModelsResultPayload)
	if !ok {
		return nil
	}

	picker := b.modelPickers[ev.identity.ChatID]
	if picker == nil || picker.step != pickerStepLoading || picker.provider != payload.Provider {
		return nil
	}
	if picker.requestID != "" && ev.event.RequestID != "" && picker.requestID != ev.event.RequestID {
		return nil
	}

	bridge, err := b.getOrCreateBridge(ev.identity)
	if err != nil {
		return fmt.Errorf("get bridge for provider models: %w", err)
	}

	_, currentProvider, currentModel, _, _, _ := bridge.SessionInfo()
	picker.models = buildModelsFromResult(payload, b.config.Providers)
	picker.step = pickerStepModel
	picker.page = 0

	title := picker.title()
	if payload.Error != "" {
		title = fmt.Sprintf("Select model (%s) - fallback", payload.Provider)
		logger.Warning("telegram model picker: provider lookup failed",
			logger.BuildMetadata("provider", payload.Provider, "error", payload.Error))
	}

	markup := buildModelKeyboard(picker.models, picker.page, currentProvider, currentModel)
	return b.sendPickerMessage(ev.identity, picker, pickerRenderResult(title, markup))
}

func (b *Bot) sendPickerMessage(identity ChatIdentity, picker *modelPicker, result *RenderResult) error {
	if picker == nil {
		return b.sendTelegramMessage(identity, b.getOrCreateTracker(identity.ChatID), result)
	}

	if picker.messageID != 0 {
		req := EditMessageTextRequest{
			ChatID:      identity.ChatID,
			MessageID:   picker.messageID,
			Text:        result.Text,
			ParseMode:   result.ParseMode,
			ReplyMarkup: result.ReplyMarkup,
		}
		_, err := b.client.EditMessageText(b.ctx, req)
		if shouldRetryWithPlainText(err, result) {
			req.Text = result.FallbackText
			req.ParseMode = ""
			_, err = b.client.EditMessageText(b.ctx, req)
		}
		if isNotModifiedError(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("edit picker message: %w", err)
		}
		return nil
	}

	req := SendMessageRequest{
		ChatID:      identity.ChatID,
		Text:        result.Text,
		ParseMode:   result.ParseMode,
		ReplyMarkup: result.ReplyMarkup,
	}
	msg, err := b.client.SendMessage(b.ctx, req)
	if shouldRetryWithPlainText(err, result) {
		req.Text = result.FallbackText
		req.ParseMode = ""
		msg, err = b.client.SendMessage(b.ctx, req)
	}
	if err != nil {
		return fmt.Errorf("send picker message: %w", err)
	}
	picker.messageID = msg.MessageID
	return nil
}

func (b *Bot) answerCallback(callbackID, text string) error {
	if callbackID == "" {
		return nil
	}
	return b.client.AnswerCallbackQuery(b.ctx, AnswerCallbackQueryRequest{
		CallbackQueryID: callbackID,
		Text:            text,
	})
}

// getOrCreateBridge returns a started bridge bound to the specific chat identity.
func (b *Bot) getOrCreateBridge(identity ChatIdentity) (*Bridge, error) {
	identityKey := identity.ToClientIdentity()

	b.bridgesMu.RLock()
	bridge := b.bridges[identityKey]
	b.bridgesMu.RUnlock()
	if bridge != nil {
		return bridge, nil
	}

	b.bridgesMu.Lock()
	defer b.bridgesMu.Unlock()

	if existing := b.bridges[identityKey]; existing != nil {
		return existing, nil
	}

	return b.createBridgeLocked(identity, identityKey)
}

// createBridgeLocked creates a new bridge for the given identity.
func (b *Bot) createBridgeLocked(identity ChatIdentity, identityKey string) (*Bridge, error) {
	chatBridge := NewBridge(
		b.config.GatewayURL,
		identity,
		b.events,
	)
	if err := chatBridge.Start(b.ctx); err != nil {
		return nil, fmt.Errorf("start bridge for %s: %w", identityKey, err)
	}

	b.bridges[identityKey] = chatBridge
	return chatBridge, nil
}

// stopAllBridges stops and clears all per-chat bridges.
func (b *Bot) stopAllBridges() {
	b.bridgesMu.Lock()
	bridges := make([]*Bridge, 0, len(b.bridges))
	for key, bridge := range b.bridges {
		bridges = append(bridges, bridge)
		delete(b.bridges, key)
	}
	b.bridgesMu.Unlock()

	for _, bridge := range bridges {
		bridge.Stop()
	}
}

// ChatIdentity uniquely identifies a Telegram DM chat for session binding.
type ChatIdentity struct {
	ChatID   int64  `json:"chat_id"`            // Telegram chat ID
	Username string `json:"username,omitempty"` // Optional username for display
}

// ToClientIdentity converts ChatIdentity to gateway.ClientIdentity.
// The identity string is formatted as "{chatID}".
func (ci ChatIdentity) ToClientIdentity() string {
	return strconv.FormatInt(ci.ChatID, 10)
}

// FromTelegramUpdate extracts ChatIdentity from a Telegram update.
func FromTelegramUpdate(chat *Chat) ChatIdentity {
	if chat == nil {
		return ChatIdentity{}
	}
	return ChatIdentity{
		ChatID:   chat.ID,
		Username: chat.Username,
	}
}
