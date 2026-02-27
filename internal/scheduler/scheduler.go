// Package scheduler provides a background cron job runner based on Cron.md.
// The scheduler reads Cron.md every configured interval and executes due jobs
// by creating a fresh agent session and broadcasting results to all clients.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/event"
	"valinor/internal/logger"
	"valinor/internal/session"
)

// cronPromptTemplate is the prompt sent to the agent during each cron tick.
// It includes the current timestamp and the contents of Cron.md.
const cronPromptTemplate = `The current date and time is: %s

Below are your scheduled jobs from Cron.md. Review each job's schedule and execute ONLY the jobs that are due right now. If no jobs are due, respond with "No jobs due." For each job you execute, report what you did.

---
%s`

// Scheduler runs scheduled jobs based on Cron.md in the workspace.
type Scheduler struct {
	workspaceDir string
	manager      *session.Manager
	sessionOpts  session.SessionOptions
	bus          *bus.Bus
	interval     time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// New creates a new scheduler with the given configuration.
func New(workspaceDir string, manager *session.Manager, sessionOpts session.SessionOptions, b *bus.Bus, cfg config.SchedulerConfig) *Scheduler {
	interval, _ := time.ParseDuration(cfg.CheckInterval)
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	return &Scheduler{
		workspaceDir: workspaceDir,
		manager:      manager,
		sessionOpts:  sessionOpts,
		bus:          b,
		interval:     interval,
	}
}

// Start begins the scheduler ticker loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true

	s.wg.Add(1)
	go s.runLoop(ctx)

	logger.Info("scheduler started", logger.BuildMetadata("interval", s.interval.String()))
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	s.wg.Wait()

	s.mu.Lock()
	s.running = false
	s.mu.Unlock()

	logger.Info("scheduler stopped", nil)
}

// runLoop is the main scheduler goroutine that ticks every interval.
func (s *Scheduler) runLoop(ctx context.Context) {
	defer s.wg.Done()

	// Create ticker
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Run immediately on start
	s.executeTick(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.Debug("scheduler loop exiting", nil)
			return
		case <-ticker.C:
			s.executeTick(ctx)
		}
	}
}

// executeTick performs a single cron check and execution.
func (s *Scheduler) executeTick(ctx context.Context) {
	logger.Debug("scheduler tick started", nil)

	// Read Cron.md from workspace
	cronPath := filepath.Join(s.workspaceDir, "Cron.md")
	content, err := os.ReadFile(cronPath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warning("scheduler: failed to read Cron.md", logger.BuildMetadata("path", cronPath, "error", err))
		}
		// File doesn't exist or error reading - skip this tick
		return
	}

	// Check if content is empty or just whitespace
	if len(content) == 0 || len(trimSpace(string(content))) == 0 {
		logger.Debug("scheduler: Cron.md is empty", nil)
		return
	}

	// Create a fresh session for this execution
	sess, err := s.manager.Create(ctx, s.sessionOpts)
	if err != nil {
		logger.Error("scheduler: failed to create session", logger.BuildMetadata("error", err))
		return
	}

	// Generate request ID for this execution
	requestID := uuid.New().String()

	// Build the cron prompt with current timestamp and Cron.md content
	timestamp := time.Now().Format(time.RFC3339)
	prompt := fmt.Sprintf(cronPromptTemplate, timestamp, string(content))

	// Subscribe to run events before starting execution so we can collect output
	// and know exactly when this request has completed.
	sub := s.bus.Subscribe(
		event.AgentContentFull,
		event.AgentDone,
		event.AgentError,
		event.AgentCancelled,
	)
	defer s.bus.Unsubscribe(sub)

	// Execute the cron prompt in the session
	errChan := make(chan error, 1)
	go func() {
		errChan <- sess.HandleUserMessage(ctx, requestID, prompt)
	}()

	// Collect the response for this specific session/request.
	var resultContent string
	var execErr error
	done := false
	runReturned := false
	runCompletedAt := time.Time{}
	runEventWait := 2 * time.Second

	for !done {
		select {
		case <-ctx.Done():
			logger.Debug("scheduler tick cancelled", nil)
			sess.Close()
			return

		case err := <-errChan:
			runReturned = true
			runCompletedAt = time.Now()
			execErr = err
			if err != nil {
				logger.Error("scheduler: session execution error", logger.BuildMetadata("error", err))
				done = true
				break
			}
			// Success path: normally AgentDone arrives and ends the loop.
			// If the event is missed for any reason, stop waiting after a short grace period.
			if !runCompletedAt.IsZero() && time.Since(runCompletedAt) >= runEventWait {
				done = true
			}

		case ev, ok := <-sub.Ch():
			if !ok {
				done = true
				break
			}
			// Filter to this scheduler run.
			if ev.SessionID == sess.Meta().ID && ev.RequestID == requestID {
				switch ev.Type {
				case event.AgentContentFull:
					if payload, ok := ev.Payload.(event.ContentFullPayload); ok {
						resultContent = payload.Content
					}
				case event.AgentDone:
					done = true
				case event.AgentError:
					if payload, ok := ev.Payload.(event.AgentErrorPayload); ok && payload.Error != nil {
						execErr = payload.Error
					}
					done = true
				case event.AgentCancelled:
					execErr = context.Canceled
					done = true
				}
			}
		case <-time.After(250 * time.Millisecond):
			// Poll-style fallback: if run already returned and no terminal event arrived,
			// stop waiting after a short grace period.
			if runReturned && !runCompletedAt.IsZero() && time.Since(runCompletedAt) >= runEventWait {
				done = true
			}
		}
	}

	// If there was an execution error and no content, report the error
	if execErr != nil && resultContent == "" {
		resultContent = fmt.Sprintf("Error executing scheduled jobs: %v", execErr)
	}

	// Close the session
	if err := sess.Close(); err != nil {
		logger.Warning("scheduler: failed to close session", logger.BuildMetadata("error", err))
	}

	// If we got no content at all, don't broadcast.
	// Note: "No jobs due." is still a meaningful scheduler result and should be
	// broadcast so clients can see that the scheduler fired.
	if resultContent == "" {
		logger.Debug("scheduler: empty scheduler result", nil)
		return
	}

	// Publish CronJobResult event (no session ID = broadcast to all clients)
	logger.Debug("scheduler: publishing cron job result",
		logger.BuildMetadata("result_length", len(resultContent)))
	s.bus.Publish(event.Event{
		Type: event.CronJobResult,
		Payload: event.CronJobResultPayload{
			Content:   resultContent,
			Timestamp: time.Now(),
		},
		// Explicitly broadcast to all clients (not session-targeted).
		SessionID: "",
	})

	logger.Info("scheduler tick completed", logger.BuildMetadata("result_length", len(resultContent)))
}

// trimSpace is a helper that trims whitespace and returns the string.
func trimSpace(s string) string {
	start := 0
	end := len(s)

	for start < end {
		c := s[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			start++
		} else {
			break
		}
	}

	for end > start {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			end--
		} else {
			break
		}
	}

	return s[start:end]
}
