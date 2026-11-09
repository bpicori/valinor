package dream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	agentcontext "valinor/internal/context"
	"valinor/internal/llm"
	"valinor/internal/logger"
	"valinor/internal/session"
)

// TranscriptSource supplies persisted sessions to the consolidator.
type TranscriptSource interface {
	ListTranscripts(ctx context.Context) ([]session.TranscriptSummary, error)
	LoadTranscript(ctx context.Context, sessionID string) ([]agentcontext.Message, error)
}

// Activity reports whether a user turn is currently in flight.
type Activity interface {
	HasActiveRun() bool
}

// Options configures a dreaming runtime.
type Options struct {
	Provider     llm.Provider
	ProviderName string
	Model        string
	WorkspaceDir string
	StateDir     string
	Sessions     TranscriptSource
	Activity     Activity
}

// Runtime schedules and executes offline memory consolidation.
type Runtime struct {
	opts         Options
	workspaceDir string
	stateDir     string
	state        *state

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
}

// New creates a dreaming runtime and loads its persisted state.
func New(opts Options) (*Runtime, error) {
	if opts.Provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if strings.TrimSpace(opts.WorkspaceDir) == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, fmt.Errorf("state dir is required")
	}
	if opts.Sessions == nil {
		return nil, fmt.Errorf("session source is required")
	}
	if maxMemoryChars > agentcontext.MaxMemoryMainChars {
		return nil, fmt.Errorf("internal memory budget %d exceeds MEMORY.md limit %d",
			maxMemoryChars, agentcontext.MaxMemoryMainChars)
	}

	saved, err := loadState(statePath(opts.StateDir))
	if err != nil {
		badPath := statePath(opts.StateDir) + ".bad-" + time.Now().UTC().Format("20060102T150405Z")
		if renameErr := os.Rename(statePath(opts.StateDir), badPath); renameErr != nil {
			logger.Warning("dreaming state quarantine failed",
				logger.BuildMetadata("path", statePath(opts.StateDir), "error", renameErr))
		}
		logger.Warning("dreaming state unreadable; starting fresh",
			logger.BuildMetadata("path", statePath(opts.StateDir), "saved_as", badPath, "error", err))
		saved = newState()
	}

	return &Runtime{
		opts:         opts,
		workspaceDir: opts.WorkspaceDir,
		stateDir:     opts.StateDir,
		state:        saved,
	}, nil
}

// Start launches the background ticker. A single gateway process owns dreaming,
// so cycles are serialized by this one goroutine; no lock file is needed.
func (r *Runtime) Start(ctx context.Context) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.running = true
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(ctx)
	}()
}

// Stop cancels the background loop and waits for it to finish.
func (r *Runtime) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.running = false
	r.mu.Unlock()

	r.wg.Wait()
}

func (r *Runtime) loop(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runtime) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	if !r.due(now) {
		return
	}
	if !r.idle(ctx, now) {
		return
	}
	r.runOnce(ctx)
}

// due reports whether a consolidation cycle should run now. The first cycle
// runs shortly after enabling dreaming; later cycles run once per runInterval
// or when a retry is due after a failure.
func (r *Runtime) due(now time.Time) bool {
	if !r.state.NextAttemptAt.IsZero() {
		return !now.Before(r.state.NextAttemptAt)
	}
	if r.state.LastSuccessAt.IsZero() {
		return true
	}
	return now.Sub(r.state.LastSuccessAt) >= runInterval
}

// idle reports whether the instance has been quiet long enough to dream.
func (r *Runtime) idle(ctx context.Context, now time.Time) bool {
	if r.opts.Activity != nil && r.opts.Activity.HasActiveRun() {
		logger.Debug("dreaming deferred: session run in progress", nil)
		return false
	}
	latest, err := r.latestActivity(ctx)
	if err != nil {
		logger.Warning("dreaming activity check failed", logger.BuildMetadata("error", err))
		return true
	}
	if !latest.IsZero() && now.Sub(latest) < minIdle {
		logger.Debug("dreaming deferred: recent session activity", logger.BuildMetadata("last_activity", latest))
		return false
	}
	return true
}

// runOnce performs one consolidation cycle. It is only called from the ticker
// goroutine, so it needs no locking; a concurrent MEMORY.md edit is detected by
// comparing the file before and after the provider call.
func (r *Runtime) runOnce(ctx context.Context) {
	started := time.Now()
	logger.Info("dreaming run starting", logger.BuildMetadata(
		"provider", r.opts.ProviderName,
		"model", r.opts.Model,
	))

	selected, err := r.selectCandidates(ctx, r.state, started)
	if err != nil {
		r.recordFailure(err, started)
		return
	}
	r.state.pruneSessions(selected.existing)
	for id, updatedAt := range selected.refreshed {
		mark := r.state.Sessions[id]
		mark.UpdatedAt = updatedAt
		r.state.Sessions[id] = mark
	}

	memoryFilePath := memoryPath(r.workspaceDir)
	beforeData, err := readFileIfExists(memoryFilePath)
	if err != nil {
		r.recordFailure(fmt.Errorf("read memory: %w", err), started)
		return
	}
	memoryBefore := string(beforeData)
	memoryBytes := len(strings.TrimSpace(memoryBefore))

	digest, included := assembleDigest(selected.candidates, maxDigestChars)
	maintenance := len(selected.candidates) == 0 && memoryBytes > maxMemoryChars
	if len(included) == 0 && !maintenance {
		logger.Debug("dreaming no-op",
			logger.BuildMetadata("memory_bytes", memoryBytes, "candidates", len(selected.candidates)))
		r.markSuccess(started, time.Now())
		return
	}

	r.state.LastAttemptAt = started
	r.state.LastError = ""

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: buildSystemPrompt()},
		{Role: llm.RoleUser, Content: buildUserPrompt(memoryBefore, digest, started)},
	}

	var (
		newMemory string
		usage     llm.Usage
		lastErr   error
	)
	for attempt := 0; attempt < 2; attempt++ {
		output, callUsage, callErr := r.complete(ctx, messages)
		if callErr != nil {
			lastErr = callErr
			break
		}
		usage.PromptTokens += callUsage.PromptTokens
		usage.CompletionTokens += callUsage.CompletionTokens
		usage.TotalTokens += callUsage.TotalTokens

		parsed, parseErr := parseMemoryFile(output)
		if parseErr == nil {
			parseErr = validateMemory(memoryBefore, parsed)
		}
		if parseErr == nil {
			newMemory = parsed
			lastErr = nil
			break
		}
		lastErr = parseErr
		if attempt == 0 {
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: output},
				llm.Message{Role: llm.RoleUser, Content: correctionMessage(parseErr.Error())},
			)
		}
	}
	if newMemory == "" {
		if lastErr == nil {
			lastErr = errors.New("consolidation returned no memory")
		}
		r.recordFailure(fmt.Errorf("consolidation failed: %w", lastErr), started)
		return
	}

	// A live session (or the user) may have edited memory during the call.
	currentData, err := readFileIfExists(memoryFilePath)
	if err != nil {
		r.recordFailure(fmt.Errorf("re-read memory: %w", err), started)
		return
	}
	if string(currentData) != memoryBefore {
		r.markSuccess(started, time.Now())
		report := runReport{
			Status:       "skipped",
			SessionIDs:   sessionIDs(included),
			MemoryBefore: memoryBytes,
			MemoryAfter:  len(strings.TrimSpace(string(currentData))),
			Duration:     time.Since(started),
			Error:        "memory changed during dream; output discarded",
		}
		if _, journalErr := writeJournal(r.workspaceDir, time.Now(), report); journalErr != nil {
			logger.Warning("dreaming journal write failed", logger.BuildMetadata("error", journalErr))
		}
		logger.Info("dreaming skipped: memory changed during run", nil)
		return
	}

	trimmedNew := strings.TrimSpace(newMemory)
	if trimmedNew == strings.TrimSpace(memoryBefore) {
		r.markSessions(included)
		r.markSuccess(started, time.Now())
		report := runReport{
			Status:           "unchanged",
			SessionIDs:       sessionIDs(included),
			MemoryBefore:     memoryBytes,
			MemoryAfter:      len(trimmedNew),
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			Duration:         time.Since(started),
		}
		if _, journalErr := writeJournal(r.workspaceDir, time.Now(), report); journalErr != nil {
			logger.Warning("dreaming journal write failed", logger.BuildMetadata("error", journalErr))
		}
		logger.Info("dreaming completed with no memory change", logger.BuildMetadata("sessions", len(included)))
		return
	}

	backupPath := ""
	if len(beforeData) > 0 {
		backupPath, err = backupMemory(r.stateDir, beforeData, time.Now())
		if err != nil {
			r.recordFailure(fmt.Errorf("backup memory: %w", err), started)
			return
		}
	}

	if err := os.MkdirAll(r.workspaceDir, 0o755); err != nil {
		r.recordFailure(fmt.Errorf("create workspace dir: %w", err), started)
		return
	}
	if err := writeFileAtomic(memoryFilePath, []byte(trimmedNew+"\n"), 0o644); err != nil {
		r.recordFailure(fmt.Errorf("write memory: %w", err), started)
		return
	}

	r.markSessions(included)
	r.markSuccess(started, time.Now())

	added, removed := headingDelta(memoryBefore, trimmedNew)
	report := runReport{
		Status:           "ok",
		SessionIDs:       sessionIDs(included),
		MemoryBefore:     memoryBytes,
		MemoryAfter:      len(trimmedNew),
		HeadingsAdded:    added,
		HeadingsRemoved:  removed,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		Duration:         time.Since(started),
		BackupPath:       backupPath,
	}
	journalPath, journalErr := writeJournal(r.workspaceDir, time.Now(), report)
	if journalErr != nil {
		logger.Warning("dreaming journal write failed", logger.BuildMetadata("error", journalErr))
	}
	logger.Info("dreaming completed", logger.BuildMetadata(
		"sessions", len(included),
		"memory_before", memoryBytes,
		"memory_after", len(trimmedNew),
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"journal", journalPath,
	))
}

func (r *Runtime) complete(ctx context.Context, messages []llm.Message) (string, llm.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	var content, refusal strings.Builder
	result, err := r.opts.Provider.Stream(ctx, llm.StreamParams{
		Messages:  messages,
		Model:     r.opts.Model,
		MaxTokens: maxOutputTokens,
		OnDelta: func(delta llm.StreamDelta) {
			content.WriteString(delta.Content)
			refusal.WriteString(delta.Refusal)
		},
	})
	if err != nil {
		return "", llm.Usage{}, err
	}
	if strings.TrimSpace(content.String()) == "" && result != nil {
		content.WriteString(result.Message.Content)
	}
	if strings.TrimSpace(content.String()) == "" {
		if strings.TrimSpace(refusal.String()) != "" {
			return "", llm.Usage{}, fmt.Errorf("provider refusal: %s", truncate(refusal.String(), 200, "…"))
		}
		return "", llm.Usage{}, errors.New("provider returned empty content")
	}

	usage := llm.Usage{}
	if result != nil {
		usage = result.Usage
	}
	return content.String(), usage, nil
}

func (r *Runtime) latestActivity(ctx context.Context) (time.Time, error) {
	transcripts, err := r.opts.Sessions.ListTranscripts(ctx)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, transcript := range transcripts {
		if transcript.UpdatedAt.After(latest) {
			latest = transcript.UpdatedAt
		}
	}
	return latest, nil
}

func (r *Runtime) markSessions(included []candidate) {
	for _, item := range included {
		r.state.Sessions[item.summary.Meta.ID] = sessionMark{
			DigestHash: item.hash,
			UpdatedAt:  item.summary.UpdatedAt,
		}
	}
}

func (r *Runtime) markSuccess(attemptAt, completedAt time.Time) {
	r.state.LastAttemptAt = attemptAt
	r.state.LastSuccessAt = completedAt
	r.state.NextAttemptAt = time.Time{}
	r.state.ConsecutiveFailures = 0
	r.state.LastError = ""
	r.saveState()
}

func (r *Runtime) recordFailure(err error, at time.Time) {
	message := truncateError(err)
	now := time.Now()
	r.state.LastAttemptAt = at
	r.state.LastError = message
	r.state.ConsecutiveFailures++
	if r.state.ConsecutiveFailures < maxFailures {
		r.state.NextAttemptAt = now.Add(retryDelay)
	} else {
		r.state.NextAttemptAt = now.Add(runInterval)
	}
	r.saveState()

	report := runReport{
		Status:   "failed",
		Duration: now.Sub(at),
		Error:    message,
	}
	if _, journalErr := writeJournal(r.workspaceDir, at, report); journalErr != nil {
		logger.Warning("dreaming failure journal write failed", logger.BuildMetadata("error", journalErr))
	}
	logger.Warning("dreaming failed", logger.BuildMetadata(
		"error", message,
		"failures", r.state.ConsecutiveFailures,
		"next_attempt", r.state.NextAttemptAt,
	))
}

func (r *Runtime) saveState() {
	if err := r.state.save(statePath(r.stateDir)); err != nil {
		logger.Error("dreaming state save failed", logger.BuildMetadata("error", err))
	}
}

func sessionIDs(included []candidate) []string {
	ids := make([]string, 0, len(included))
	for _, item := range included {
		id := item.summary.Meta.ID
		if len(id) > 8 {
			id = id[:8]
		}
		ids = append(ids, id)
	}
	return ids
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	return truncate(err.Error(), maxErrorChars, "…")
}
