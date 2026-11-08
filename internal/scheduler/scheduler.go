// Package scheduler provides a background cron job runner based on the cron service.
// It polls for due jobs and executes them in isolated sessions, publishing results
// to targeted clients via the event bus.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	agentcontext "valinor/internal/context"

	"github.com/google/uuid"

	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/event"
	"valinor/internal/logger"
	"valinor/internal/session"
	agenttools "valinor/internal/tools"
)

const (
	noReplyPrefix = "NO_REPLY"
	silentPrefix  = "[SILENT]"
	wrapSeparator = "─────────────────────────────"
	tickInterval  = 60 * time.Second
)

// cronAgentExcludedTools lists tools withheld from agents during scheduled job runs.
var cronAgentExcludedTools = []string{agenttools.CronjobName}

// Scheduler runs scheduled jobs from the jobs store.
type Scheduler struct {
	jobs          *cron.Jobs
	manager       *session.Manager
	scriptsDir    string
	workspaceDir  string
	scriptTimeout time.Duration
	interval      time.Duration

	mu        sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   bool
	execLocks sync.Map // lock key -> *sync.Mutex
}

// New creates a new scheduler with the given jobs store.
func New(jobs *cron.Jobs, manager *session.Manager) *Scheduler {
	cfg := config.Must().Scheduler
	scriptTimeout, _ := time.ParseDuration(cfg.ScriptTimeout)
	if scriptTimeout <= 0 {
		scriptTimeout = 120 * time.Second
	}
	paths := config.MustPaths()

	return &Scheduler{
		jobs:          jobs,
		manager:       manager,
		scriptsDir:    paths.ScriptsDir,
		workspaceDir:  paths.WorkspaceDir,
		scriptTimeout: scriptTimeout,
		interval:      tickInterval,
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

func (s *Scheduler) runLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

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

func (s *Scheduler) executeTick(ctx context.Context) {
	lockFile, acquired, err := tryAcquireTickLock(filepath.Join(s.jobs.Dir(), ".tick.lock"))
	if err != nil {
		logger.Warning("scheduler: tick lock error", logger.BuildMetadata("error", err))
		return
	}
	if !acquired {
		logger.Debug("scheduler: skipping tick, another instance holds lock", nil)
		return
	}
	defer releaseTickLock(lockFile)

	logger.Debug("scheduler tick started", nil)

	now := time.Now().UTC()
	dueJobs, err := s.jobs.Due(now)
	if err != nil {
		logger.Error("scheduler: failed to get due jobs", logger.BuildMetadata("error", err))
		return
	}

	if len(dueJobs) == 0 {
		logger.Debug("scheduler tick completed: no jobs due", nil)
		return
	}

	logger.Info("scheduler: executing due jobs", logger.BuildMetadata("count", len(dueJobs)))

	var wg sync.WaitGroup
	for _, job := range dueJobs {
		wg.Add(1)
		go func(job *cron.CronJob) {
			defer wg.Done()
			s.runJob(ctx, job, now)
		}(job)
	}
	wg.Wait()

	logger.Info("scheduler tick completed", logger.BuildMetadata("jobs_executed", len(dueJobs)))
}

func (s *Scheduler) runJob(ctx context.Context, job *cron.CronJob, at time.Time) {
	jobMu := s.execMutex("job:" + job.ID)
	jobMu.Lock()
	defer jobMu.Unlock()

	s.executeJob(ctx, job, at)
}

func (s *Scheduler) execMutex(key string) *sync.Mutex {
	v, _ := s.execLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *Scheduler) executeJob(ctx context.Context, job *cron.CronJob, at time.Time) {
	logger.Debug("scheduler: executing job",
		logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "no_agent", job.NoAgent))

	var resultContent string
	var execErr error

	switch {
	case job.NoAgent && job.Script == "":
		execErr = fmt.Errorf("script is required when no_agent is true")
	case job.NoAgent:
		resultContent, execErr = s.executeScriptJob(ctx, job)
	default:
		resultContent, execErr = s.executeAgentJob(ctx, job)
	}

	if execErr != nil {
		logger.Warning("scheduler: job execution failed",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "error", execErr))
		s.recordRun(job.ID, at, "error", fmt.Sprintf("Execution failed: %v", execErr))
		return
	}

	updatedJob, ok, err := s.jobs.MarkRun(job.ID, at)
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

	if updatedJob.Schedule.Kind == cron.ScheduleCron && !updatedJob.NextRunAt.IsZero() {
		logger.Debug("scheduler: job rescheduled",
			logger.BuildMetadata("job_id", job.ID, "next_run", updatedJob.NextRunAt.Format(time.RFC3339)))
	}

	resultContent = normalizeScheduledJobContent(resultContent)
	s.recordRun(job.ID, at, "ok", resultContent)

	if resultContent != "" {
		if err := s.jobs.SaveOutput(job.ID, resultContent); err != nil {
			logger.Warning("scheduler: failed to save job output",
				logger.BuildMetadata("job_id", job.ID, "error", err))
		}
	}

	if resultContent == "" {
		logger.Debug("scheduler: job completed with no output",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
	}

	if resultContent != "" && job.DeliverTo.Kind != cron.DeliverySilent {
		event.Publish(event.Event{
			Type: event.CronJobResult,
			Payload: event.CronJobResultPayload{
				JobID:     job.ID,
				JobName:   job.Name,
				Content:   wrapJobResponse(job.Name, resultContent),
				Timestamp: time.Now(),
				DeliverTo: job.DeliverTo,
				CreatedBy: s.resolveOriginClient(ctx, job),
			},
			SessionID: "",
			RequestID: job.ID,
		})

		logger.Info("scheduler: job completed with notification",
			logger.BuildMetadata("job_id", job.ID, "job_name", job.Name))
	}

	if job.Schedule.Kind == cron.ScheduleOnce {
		s.deleteOneTimeJob(job)
	}
}

func (s *Scheduler) executeScriptJob(ctx context.Context, job *cron.CronJob) (string, error) {
	return runScript(ctx, s.workspaceDir, s.scriptsDir, job.Script, s.scriptTimeout)
}

func (s *Scheduler) executeAgentJob(ctx context.Context, job *cron.CronJob) (string, error) {
	sess, err := s.manager.Create(ctx, session.CreateSessionOptions{
		Temporary: true,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	tools := agenttools.Exclude(sess.Tools(), cronAgentExcludedTools...)
	if job.Depth < cron.MaxNestedJobDepth {
		tools = append(tools, agenttools.BuildScheduledCronTool(
			sess.RunContext(),
			job.ID,
			job.CreatedInSessionID,
			job.DeliverTo,
			job.Depth,
		))
	}
	sess.SetTools(tools, sess.RunContext())
	defer func() {
		if err := sess.Close(); err != nil {
			logger.Warning("scheduler: failed to close session",
				logger.BuildMetadata("job_id", job.ID, "error", err))
		}
	}()

	requestID := uuid.New().String()
	jobMessage, err := s.buildJobMessage(ctx, job)
	if err != nil {
		return "", err
	}

	result, execErr := sess.RunInteractionWithOptions(ctx, requestID, jobMessage, session.RunInteractionOptions{
		EmitEvents: false,
	})
	if execErr != nil {
		return "", execErr
	}

	return lastAssistantContent(result.Messages), nil
}

func (s *Scheduler) buildJobMessage(ctx context.Context, job *cron.CronJob) (string, error) {
	workspaceDir := s.workspaceDir

	var sections []string

	if job.ContextFrom != "" {
		prior, err := s.jobs.GetOutput(job.ContextFrom)
		if err != nil {
			return "", fmt.Errorf("load context_from output: %w", err)
		}
		if strings.TrimSpace(prior) != "" {
			sections = append(sections, fmt.Sprintf("Previous output from job %s:\n%s", job.ContextFrom, prior))
		}
	}

	if len(job.Skills) > 0 {
		skillContent, err := agentcontext.LoadSkillBodies(workspaceDir, job.Skills)
		if err != nil {
			return "", fmt.Errorf("load skills: %w", err)
		}
		if skillContent != "" {
			sections = append(sections, skillContent)
		}
	}

	header := fmt.Sprintf(
		"[Scheduled Job: %q | ID: %s]\n\n"+
			"You are executing a scheduled job.\n"+
			"If the job should not notify the user, respond with exactly NO_REPLY or [SILENT].\n"+
			"If the job should notify the user, respond with only the alert text to send to the user.\n"+
			"Do not explain your decision.\n\n"+
			"Task:\n%s",
		job.Name, job.ID, job.Message,
	)

	if len(sections) > 0 {
		return strings.Join(sections, "\n\n") + "\n\n" + header, nil
	}
	return header, nil
}

func wrapJobResponse(jobName, content string) string {
	return fmt.Sprintf("Scheduled Job: %s\n%s\n%s", jobName, wrapSeparator, content)
}

func (s *Scheduler) recordRun(jobID string, at time.Time, status, output string) {
	run := cron.JobRun{
		JobID:   jobID,
		FiredAt: at,
		Status:  status,
		Output:  output,
	}
	if err := s.jobs.RecordRun(run); err != nil {
		logger.Warning("scheduler: failed to record job run",
			logger.BuildMetadata("job_id", jobID, "error", err))
	}
}

func (s *Scheduler) deleteOneTimeJob(job *cron.CronJob) {
	if _, err := s.jobs.Remove(job.ID); err != nil {
		logger.Warning("scheduler: failed to delete one-time job",
			logger.BuildMetadata("job_id", job.ID, "error", err))
	} else {
		logger.Debug("scheduler: deleted one-time job",
			logger.BuildMetadata("job_id", job.ID))
	}
}

func (s *Scheduler) resolveOriginClient(ctx context.Context, job *cron.CronJob) cron.ClientRef {
	if job.CreatedInSessionID == "" {
		return cron.ClientRef{}
	}

	meta, err := s.manager.GetMetadata(ctx, job.CreatedInSessionID)
	if err != nil {
		logger.Warning("scheduler: failed to resolve cron delivery client from session",
			logger.BuildMetadata("job_id", job.ID, "session_id", job.CreatedInSessionID, "error", err))
		return cron.ClientRef{}
	}
	if meta == nil || meta.Temporary {
		return cron.ClientRef{}
	}
	if meta.ClientType == "" || meta.ClientIdentity == "" {
		return cron.ClientRef{}
	}

	return cron.ClientRef{Type: meta.ClientType, Identity: meta.ClientIdentity}
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
	if hasSilentPrefix(content) {
		return ""
	}
	return content
}

func hasSilentPrefix(content string) bool {
	trimmed := strings.TrimLeftFunc(content, unicode.IsSpace)
	return strings.HasPrefix(trimmed, noReplyPrefix) || strings.HasPrefix(trimmed, silentPrefix)
}

func tryAcquireTickLock(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil
	}
	return f, true, nil
}

func releaseTickLock(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
