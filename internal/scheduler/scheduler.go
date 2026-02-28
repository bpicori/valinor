// Package scheduler provides a background cron job runner based on the cron service.
// It polls for due jobs and executes them in isolated sessions, publishing results
// to all connected clients via the event bus.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/event"
	"valinor/internal/logger"
	"valinor/internal/session"
)

// Scheduler runs scheduled jobs from the cron service.
type Scheduler struct {
	cronService  *cron.Service
	manager      *session.Manager
	sessionOpts  session.SessionOptions
	bus          *bus.Bus
	interval     time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// New creates a new scheduler with the given cron service.
func New(cronService *cron.Service, manager *session.Manager, sessionOpts session.SessionOptions, b *bus.Bus, cfg config.SchedulerConfig) *Scheduler {
	interval, _ := time.ParseDuration(cfg.CheckInterval)
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	return &Scheduler{
		cronService:  cronService,
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

// executeTick checks for due jobs and executes them.
func (s *Scheduler) executeTick(ctx context.Context) {
	logger.Debug("scheduler tick started", nil)

	now := time.Now().UTC()

	// Get due jobs from cron service
	dueJobs, err := s.cronService.Tick(now)
	if err != nil {
		logger.Error("scheduler: failed to get due jobs", logger.BuildMetadata("error", err))
		return
	}

	if len(dueJobs) == 0 {
		logger.Debug("scheduler tick completed: no jobs due", nil)
		return
	}

	logger.Info("scheduler: executing due jobs", logger.BuildMetadata("count", len(dueJobs)))

	// Execute each due job
	for _, job := range dueJobs {
		select {
		case <-ctx.Done():
			logger.Debug("scheduler tick cancelled", nil)
			return
		default:
			s.executeJob(ctx, job, now)
		}
	}

	logger.Info("scheduler tick completed", logger.BuildMetadata("jobs_executed", len(dueJobs)))
}

// executeJob runs a single cron job in an isolated session.
func (s *Scheduler) executeJob(ctx context.Context, job *cron.CronJob, at time.Time) {
	logger.Debug("scheduler: executing job",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))

	// Mark job as running - compute next run time
	updatedJob, ok, err := s.cronService.MarkJobExecuted(job.ID, at)
	if err != nil {
		logger.Error("scheduler: failed to mark job executed",
			logger.BuildMetadata("job_id", job.ID, "error", err))
		return
	}
	if !ok {
		logger.Warning("scheduler: job not found during execution",
			logger.BuildMetadata("job_id", job.ID))
		return
	}

	// Log next scheduled run for recurring jobs
	if updatedJob.Schedule.Kind == cron.ScheduleEvery && !updatedJob.NextRunAt.IsZero() {
		logger.Debug("scheduler: job rescheduled",
			logger.BuildMetadata("job_id", job.ID, "next_run", updatedJob.NextRunAt.Format(time.RFC3339)))
	}

	// Create a fresh isolated temporary session for this job execution
	sess, err := s.manager.CreateTemporary(ctx, s.sessionOpts)
	if err != nil {
		logger.Error("scheduler: failed to create session for job",
			logger.BuildMetadata("job_id", job.ID, "error", err))
		s.publishError(job, fmt.Errorf("failed to create session: %w", err))
		return
	}

	// Generate request ID for this execution
	requestID := uuid.New().String()

	// Subscribe to run events before starting execution
	sub := s.bus.Subscribe(
		event.AgentContentFull,
		event.AgentDone,
		event.AgentError,
		event.AgentCancelled,
	)
	defer s.bus.Unsubscribe(sub)

	// Execute the job message in the session
	errChan := make(chan error, 1)
	go func() {
		errChan <- sess.HandleUserMessage(ctx, requestID, job.Message)
	}()

	// Collect the response for this specific session/request
	var resultContent string
	var execErr error
	done := false
	runReturned := false
	runCompletedAt := time.Time{}
	runEventWait := 2 * time.Second

	for !done {
		select {
		case <-ctx.Done():
			logger.Debug("scheduler job cancelled", logger.BuildMetadata("job_id", job.ID))
			sess.Close()
			return

		case err := <-errChan:
			runReturned = true
			runCompletedAt = time.Now()
			execErr = err
			if err != nil {
				logger.Error("scheduler: job execution error",
					logger.BuildMetadata("job_id", job.ID, "error", err))
				done = true
				break
			}
			// Success path: normally AgentDone arrives and ends the loop.
			if !runCompletedAt.IsZero() && time.Since(runCompletedAt) >= runEventWait {
				done = true
			}

		case ev, ok := <-sub.Ch():
			if !ok {
				done = true
				break
			}
			// Filter to this job's session
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
			// Poll-style fallback
			if runReturned && !runCompletedAt.IsZero() && time.Since(runCompletedAt) >= runEventWait {
				done = true
			}
		}
	}

	// If there was an execution error and no content, report the error
	if execErr != nil && resultContent == "" {
		resultContent = fmt.Sprintf("Error executing scheduled job '%s': %v", job.Name, execErr)
	}

	// Close the session
	if err := sess.Close(); err != nil {
		logger.Warning("scheduler: failed to close session",
			logger.BuildMetadata("job_id", job.ID, "error", err))
	}

	// If we got no content at all, don't broadcast
	if resultContent == "" {
		logger.Debug("scheduler: empty job result",
			logger.BuildMetadata("job_id", job.ID))
		return
	}

	// Publish CronJobResult event (no session ID = broadcast to all clients)
	logger.Debug("scheduler: publishing job result",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "result_length", len(resultContent)))

	s.bus.Publish(event.Event{
		Type: event.CronJobResult,
		Payload: event.CronJobResultPayload{
			JobID:     job.ID,
			JobName:   job.Name,
			Content:   resultContent,
			Timestamp: time.Now(),
		},
		// Explicitly broadcast to all clients (not session-targeted)
		SessionID: "",
		RequestID: job.ID, // Use job ID for correlation
	})

	logger.Info("scheduler: job completed",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
}

// publishError broadcasts an error result for a job.
func (s *Scheduler) publishError(job *cron.CronJob, execErr error) {
	content := fmt.Sprintf("Scheduled job '%s' failed: %v", job.Name, execErr)
	s.bus.Publish(event.Event{
		Type: event.CronJobResult,
		Payload: event.CronJobResultPayload{
			JobID:     job.ID,
			JobName:   job.Name,
			Content:   content,
			Timestamp: time.Now(),
		},
		SessionID: "",
		RequestID: job.ID,
	})
}
