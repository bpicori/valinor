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
	"unicode"

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

const noReplyPrefix = "NO_REPLY"

// buildJobMessage wraps the cron job message with identifying metadata.
// The agent runs with full context (system prompt, skills, memory, tools) and
// decides based on the job message whether to respond or stay silent.
func buildJobMessage(job *cron.CronJob) string {
	return fmt.Sprintf(
		"[Scheduled Job: %q | ID: %s]\n\n"+
			"You are executing a scheduled job.\n"+
			"If the job should not notify the user, respond with exactly NO_REPLY.\n"+
			"If the job should notify the user, respond with only the alert text to send to the user.\n"+
			"Do not explain your decision, and do not include any extra formatting before NO_REPLY.\n\n"+
			"Task:\n%s",
		job.Name,
		job.ID,
		job.Message,
	)
}

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
		s.recordRun(job.ID, at, "error", fmt.Sprintf("Failed to mark job executed: %v", err))
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
		s.recordRun(job.ID, at, "error", fmt.Sprintf("Failed to create session: %v", err))
		return
	}

	// Generate request ID for this execution
	requestID := uuid.New().String()

	// Build the job message with metadata for the agent
	jobMessage := buildJobMessage(job)

	result, execErr := sess.RunInteractionWithOptions(ctx, requestID, jobMessage, session.RunInteractionOptions{
		EmitEvents: false,
	})
	resultContent := lastAssistantContent(result.Messages)

	// Close the session
	if err := sess.Close(); err != nil {
		logger.Warning("scheduler: failed to close session",
			logger.BuildMetadata("job_id", job.ID, "error", err))
	}

	// If there was an execution error, record failure
	if execErr != nil {
		logger.Warning("scheduler: job execution failed",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "error", execErr))
		s.recordRun(job.ID, at, "error", fmt.Sprintf("Execution failed: %v", execErr))
		return
	}

	resultContent = normalizeScheduledJobContent(resultContent)

	// Record successful run
	s.recordRun(job.ID, at, "ok", resultContent)

	// If we got no content at all, the agent decided not to notify (condition not met)
	if resultContent == "" {
		logger.Debug("scheduler: job completed with no output (condition not met or silent run)",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
	}

	// Publish CronJobResult event if there's content to notify (no session ID = broadcast to all clients)
	if resultContent != "" {
		logger.Debug("scheduler: publishing job result",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "message_length", len(resultContent)))

		event_bus.Publish(event.Event{
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

		logger.Info("scheduler: job completed with notification",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
	}

	// Delete one-time jobs after execution (regardless of output)
	if job.Schedule.Kind == cron.ScheduleAt {
		s.deleteOneTimeJob(job)
	}
}

// recordRun records a job execution in the cron service.
func (s *Scheduler) recordRun(jobID string, at time.Time, status, output string) {
	run := cron.JobRun{
		JobID:   jobID,
		FiredAt: at,
		Status:  status,
		Output:  output,
	}
	if err := s.cronService.RecordRun(run); err != nil {
		logger.Warning("scheduler: failed to record job run",
			logger.BuildMetadata("job_id", jobID, "error", err))
	}
}

// deleteOneTimeJob permanently removes a one-time job after execution.
func (s *Scheduler) deleteOneTimeJob(job *cron.CronJob) {
	if err := s.cronService.DeleteJob(job.ID); err != nil {
		logger.Warning("scheduler: failed to delete one-time job",
			logger.BuildMetadata("job_id", job.ID, "error", err))
	} else {
		logger.Debug("scheduler: deleted one-time job",
			logger.BuildMetadata("job_id", job.ID))
	}
}

func lastAssistantContent(messages []agentcontext.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == agentcontext.RoleAssistant {
			return messages[i].Content
		}
	}
	return ""
}

func normalizeScheduledJobContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	if hasNoReplyPrefix(content) {
		return ""
	}

	return content
}

func hasNoReplyPrefix(content string) bool {
	trimmed := strings.TrimLeftFunc(content, unicode.IsSpace)
	return strings.HasPrefix(trimmed, noReplyPrefix)
}
