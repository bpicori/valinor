// Package scheduler provides a background cron job runner based on the cron service.
// It polls for due jobs and executes them in isolated sessions, publishing results
// to all connected clients via the event bus.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	agentcontext "valinor/internal/context"

	"github.com/google/uuid"

	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/event"
	"valinor/internal/event_bus"
	"valinor/internal/logger"
	"valinor/internal/session"
)

// Scheduler runs scheduled jobs from the cron service.
type Scheduler struct {
	cronService *cron.Service
	manager     *session.Manager
	sessionOpts session.CreateSessionOptions
	interval    time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// cronPromptWrapper is the system instruction prepended to scheduled job prompts.
// It instructs the model to return alert text only when the condition is met.
const cronPromptWrapper = `You are a scheduled monitoring agent. Your task is to evaluate a condition and decide whether to alert.

INSTRUCTIONS:
1. Evaluate the user's request below
2. Decide if the condition is met (e.g., price threshold reached, status changed, etc.)
3. If the condition is met, return ONLY one line that starts with "ALERT: " followed by the alert message
4. If the condition is not met, return an empty response with no text at all
5. Never explain why there is no alert
6. Do not include JSON, markdown wrappers, explanations, status messages, or any extra text

USER REQUEST:
%s`

const cronAlertPrefix = "ALERT: "

// New creates a new scheduler with the given cron service.
func New(cronService *cron.Service, manager *session.Manager, sessionOpts session.CreateSessionOptions, cfg config.SchedulerConfig) *Scheduler {
	interval, _ := time.ParseDuration(cfg.CheckInterval)
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	return &Scheduler{
		cronService: cronService,
		manager:     manager,
		sessionOpts: sessionOpts,
		interval:    interval,
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
	sess, err := s.manager.Create(ctx, session.CreateSessionOptions{
		MaxAgentIterations: s.sessionOpts.MaxAgentIterations,
		Provider:           s.sessionOpts.Provider,
		Model:              s.sessionOpts.Model,
		Temporary:          true,
	})
	if err != nil {
		logger.Error("scheduler: failed to create session for job",
			logger.BuildMetadata("job_id", job.ID, "error", err))
		return
	}

	// Generate request ID for this execution
	requestID := uuid.New().String()

	// Wrap the job message with the cron decision contract
	wrappedMessage := fmt.Sprintf(cronPromptWrapper, job.Message)

	result, execErr := sess.RunInteractionWithOptions(ctx, requestID, wrappedMessage, session.RunInteractionOptions{
		EmitEvents: false,
	})
	resultContent := lastAssistantContent(result.Messages)

	// Close the session
	if err := sess.Close(); err != nil {
		logger.Warning("scheduler: failed to close session",
			logger.BuildMetadata("job_id", job.ID, "error", err))
	}

	// If there was an execution error, report it
	if execErr != nil {
		logger.Warning("scheduler: job execution failed without notification",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "error", execErr))
		return
	}

	resultContent = strings.TrimSpace(resultContent)

	// If we got no content at all, nothing to do
	if resultContent == "" {
		logger.Debug("scheduler: empty job result",
			logger.BuildMetadata("job_id", job.ID))
		return
	}

	if !strings.HasPrefix(resultContent, cronAlertPrefix) {
		logger.Warning("scheduler: ignoring non-alert cron output",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "raw", resultContent))
		return
	}

	alertMessage := strings.TrimSpace(strings.TrimPrefix(resultContent, cronAlertPrefix))
	if alertMessage == "" {
		logger.Warning("scheduler: ignoring empty alert payload",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
		return
	}

	// Publish CronJobResult event (no session ID = broadcast to all clients)
	logger.Debug("scheduler: publishing job result",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "message_length", len(alertMessage)))

	event_bus.Publish(event.Event{
		Type: event.CronJobResult,
		Payload: event.CronJobResultPayload{
			JobID:     job.ID,
			JobName:   job.Name,
			Content:   alertMessage,
			Timestamp: time.Now(),
		},
		// Explicitly broadcast to all clients (not session-targeted)
		SessionID: "",
		RequestID: job.ID, // Use job ID for correlation
	})

	logger.Info("scheduler: job completed with alert",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
}

func lastAssistantContent(messages []agentcontext.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == agentcontext.RoleAssistant {
			return messages[i].Content
		}
	}
	return ""
}
