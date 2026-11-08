// Package cron provides structured cron job scheduling with persistence.
// Jobs are stored in JSON format and support cron (recurring) and once (one-time) schedules.
package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	robfigcron "github.com/robfig/cron/v3"

	"valinor/internal/logger"
)

var cronExprParser = robfigcron.NewParser(
	robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow | robfigcron.Descriptor,
)

// ParseCronExpression validates and parses a standard 5-field cron expression.
func ParseCronExpression(expr string) (robfigcron.Schedule, error) {
	sched, err := cronExprParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	return sched, nil
}

// NextCronRun returns the next run time strictly after from for the given cron expression.
func NextCronRun(expr string, from time.Time) (time.Time, error) {
	sched, err := ParseCronExpression(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

// ScheduleKind identifies the type of schedule.
type ScheduleKind string

const (
	// ScheduleCron runs the job on a cron expression (e.g., "0 9 * * 1-5", "@every 5m").
	ScheduleCron ScheduleKind = "cron"
	// ScheduleOnce runs the job once at a specific time.
	ScheduleOnce ScheduleKind = "once"
)

// Schedule defines when a job should run.
type Schedule struct {
	Kind  ScheduleKind `json:"kind"`
	Cron  string       `json:"cron,omitempty"`   // Cron expression for recurring jobs
	RunAt time.Time    `json:"run_at,omitempty"` // One-time run time
}

// Validate checks the schedule configuration and returns an error if invalid.
func (s Schedule) Validate() error {
	switch s.Kind {
	case ScheduleCron:
		if s.Cron == "" {
			return fmt.Errorf("schedule.cron is required for 'cron' kind")
		}
		if _, err := ParseCronExpression(s.Cron); err != nil {
			return err
		}
	case ScheduleOnce:
		if s.RunAt.IsZero() {
			return fmt.Errorf("schedule.run_at is required for 'once' kind")
		}
	default:
		return fmt.Errorf("invalid schedule.kind: %s (must be 'cron' or 'once')", s.Kind)
	}
	return nil
}

// Delivery kind constants.
const (
	DeliveryOrigin    = "origin"
	DeliveryClient    = "client"
	DeliveryBroadcast = "broadcast"
	DeliverySilent    = "silent"
)

// ClientRef identifies the client that created or should receive a job result.
type ClientRef struct {
	Type     string `json:"type"`
	Identity string `json:"identity"`
}

// DeliveryTarget specifies where cron job results should be delivered.
type DeliveryTarget struct {
	Kind           string `json:"kind"`                      // origin | client | broadcast | silent
	ClientType     string `json:"client_type,omitempty"`     // for kind=client
	ClientIdentity string `json:"client_identity,omitempty"` // for kind=client
}

// Validate checks the delivery target configuration.
func (t DeliveryTarget) Validate() error {
	switch t.Kind {
	case "", DeliveryOrigin, DeliveryBroadcast, DeliverySilent:
		return nil
	case DeliveryClient:
		if t.ClientType == "" || t.ClientIdentity == "" {
			return fmt.Errorf("deliver_to.client_type and deliver_to.client_identity are required for 'client' kind")
		}
		return nil
	default:
		return fmt.Errorf("invalid deliver_to.kind: %s (must be origin, client, broadcast, or silent)", t.Kind)
	}
}

// DefaultDeliveryTarget returns the default delivery target for new jobs.
func DefaultDeliveryTarget() DeliveryTarget {
	return DeliveryTarget{Kind: DeliveryOrigin}
}

// CronJob represents a scheduled task that persists across restarts.
type CronJob struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Message            string         `json:"message"`
	Schedule           Schedule       `json:"schedule"`
	Enabled            bool           `json:"enabled"`
	NextRunAt          time.Time      `json:"next_run_at"`
	LastRunAt          time.Time      `json:"last_run_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	DeliverTo          DeliveryTarget `json:"deliver_to,omitempty"`
	CreatedInSessionID string         `json:"created_in_session_id,omitempty"`
	Skills             []string       `json:"skills,omitempty"`
	NoAgent            bool           `json:"no_agent,omitempty"`
	Script             string         `json:"script,omitempty"`
	ContextFrom        string         `json:"context_from,omitempty"`
}

// Validate checks the job configuration and returns an error if invalid.
func (j *CronJob) Validate() error {
	if j.ID == "" {
		return fmt.Errorf("cron job id is required")
	}
	return validateJobContent(j)
}

func validateScheduleNotPast(s Schedule, now time.Time) error {
	if s.Kind == ScheduleOnce && !s.RunAt.IsZero() && s.RunAt.Before(now) {
		return fmt.Errorf("scheduled time is in the past")
	}
	return nil
}

func validateJobContent(j *CronJob) error {
	if j.Name == "" {
		return fmt.Errorf("cron job name is required")
	}
	if j.Message == "" && !(j.NoAgent && j.Script != "") {
		return fmt.Errorf("cron job message is required")
	}
	if err := j.Schedule.Validate(); err != nil {
		return err
	}
	if err := j.DeliverTo.Validate(); err != nil {
		return err
	}
	if j.NoAgent && j.Script == "" {
		return fmt.Errorf("script is required when no_agent is true")
	}
	return nil
}

// ComputeNextRun calculates when the job should next run based on its schedule.
func (j *CronJob) ComputeNextRun(from time.Time) time.Time {
	if !j.Enabled {
		return time.Time{}
	}

	switch j.Schedule.Kind {
	case ScheduleOnce:
		if j.Schedule.RunAt.After(from) {
			return j.Schedule.RunAt
		}
		return time.Time{}
	case ScheduleCron:
		next, err := NextCronRun(j.Schedule.Cron, from)
		if err != nil {
			return time.Time{}
		}
		return next
	}
	return time.Time{}
}

// ShouldRun checks if the job is due to run at the given time.
func (j *CronJob) ShouldRun(at time.Time) bool {
	if !j.Enabled {
		return false
	}
	if j.NextRunAt.IsZero() {
		return false
	}
	return !at.Before(j.NextRunAt)
}

// MarkRun updates the job after execution, setting LastRunAt and computing NextRunAt.
func (j *CronJob) MarkRun(at time.Time) {
	j.LastRunAt = at

	switch j.Schedule.Kind {
	case ScheduleCron:
		j.NextRunAt = j.ComputeNextRun(at)
	case ScheduleOnce:
		j.Enabled = false
		j.NextRunAt = time.Time{}
	}
	j.UpdatedAt = at
}

// JobRun represents a record of a job execution for audit and debugging.
type JobRun struct {
	JobID   string    `json:"job_id"`
	FiredAt time.Time `json:"fired_at"`
	Status  string    `json:"status"` // "ok" | "error"
	Output  string    `json:"output"` // truncated to 2000 chars
}

// TruncateOutput limits the output to maxLength characters.
func (r *JobRun) TruncateOutput(maxLength int) {
	if len(r.Output) > maxLength {
		r.Output = r.Output[:maxLength] + "... [truncated]"
	}
}

// CreateJobInput holds all fields for creating a new cron job.
type CreateJobInput struct {
	Name               string
	Message            string
	Schedule           Schedule
	CreatedInSessionID string
	DeliverTo          DeliveryTarget
	Skills             []string
	NoAgent            bool
	Script             string
	ContextFrom        string
}

// JobPatch holds optional fields for partial job updates.
type JobPatch struct {
	Name        *string
	Message     *string
	Schedule    *Schedule
	DeliverTo   *DeliveryTarget
	Skills      *[]string
	NoAgent     *bool
	Script      *string
	ContextFrom *string
}

// Apply mutates job with non-nil patch fields.
func (p JobPatch) Apply(job *CronJob) error {
	if p.Name != nil {
		job.Name = *p.Name
	}
	if p.Message != nil {
		job.Message = *p.Message
	}
	if p.Schedule != nil {
		now := time.Now().UTC()
		if err := validateScheduleNotPast(*p.Schedule, now); err != nil {
			return err
		}
		job.Schedule = *p.Schedule
		if job.Enabled {
			job.NextRunAt = job.ComputeNextRun(now)
		}
	}
	if p.DeliverTo != nil {
		job.DeliverTo = *p.DeliverTo
	}
	if p.Skills != nil {
		job.Skills = *p.Skills
	}
	if p.NoAgent != nil {
		job.NoAgent = *p.NoAgent
	}
	if p.Script != nil {
		job.Script = *p.Script
	}
	if p.ContextFrom != nil {
		job.ContextFrom = *p.ContextFrom
	}
	return job.Validate()
}

// Jobs provides persistent storage and operations for cron jobs.
type Jobs struct {
	path     string
	runsPath string
	mu       sync.RWMutex
}

// NewJobs creates a job store at the given directory.
// Jobs are stored in <dir>/jobs.json; run history in <dir>/runs.json.
func NewJobs(dir string) (*Jobs, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create cron directory: %w", err)
	}
	return &Jobs{
		path:     filepath.Join(dir, "jobs.json"),
		runsPath: filepath.Join(dir, "runs.json"),
	}, nil
}

// Dir returns the cron storage directory.
func (j *Jobs) Dir() string {
	return filepath.Dir(j.path)
}

type jobsFile struct {
	Jobs      []*CronJob `json:"jobs"`
	UpdatedAt time.Time  `json:"updated_at"`
	Version   int        `json:"version"`
}

type runsFile struct {
	Runs      []JobRun  `json:"runs"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

// Create creates and stores a new job.
func (j *Jobs) Create(in CreateJobInput) (*CronJob, error) {
	deliverTo := in.DeliverTo
	if deliverTo.Kind == "" {
		deliverTo = DefaultDeliveryTarget()
	}

	now := time.Now().UTC()
	draft := &CronJob{
		Name:        in.Name,
		Message:     in.Message,
		Schedule:    in.Schedule,
		DeliverTo:   deliverTo,
		NoAgent:     in.NoAgent,
		Script:      in.Script,
		Skills:      in.Skills,
		ContextFrom: in.ContextFrom,
	}
	if err := validateJobContent(draft); err != nil {
		return nil, err
	}
	if err := validateScheduleNotPast(in.Schedule, now); err != nil {
		return nil, err
	}

	jobs, err := j.load()
	if err != nil {
		return nil, err
	}

	if in.ContextFrom != "" {
		found := false
		for _, existing := range jobs {
			if existing.ID == in.ContextFrom {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("context_from job not found: %s", in.ContextFrom)
		}
	}

	id := generateJobID()
	for _, existing := range jobs {
		if existing.ID == id {
			id = generateJobID()
			break
		}
	}

	job := &CronJob{
		ID:                 id,
		Name:               in.Name,
		Message:            in.Message,
		Schedule:           in.Schedule,
		Enabled:            true,
		CreatedAt:          now,
		UpdatedAt:          now,
		DeliverTo:          deliverTo,
		CreatedInSessionID: in.CreatedInSessionID,
		Skills:             in.Skills,
		NoAgent:            in.NoAgent,
		Script:             in.Script,
		ContextFrom:        in.ContextFrom,
	}

	switch in.Schedule.Kind {
	case ScheduleOnce:
		job.NextRunAt = in.Schedule.RunAt
	case ScheduleCron:
		if next, err := NextCronRun(in.Schedule.Cron, now); err == nil {
			job.NextRunAt = next
		}
	}

	if err := job.Validate(); err != nil {
		return nil, err
	}

	jobs = append(jobs, job)
	if err := j.save(jobs); err != nil {
		return nil, err
	}

	return job, nil
}

// List returns all jobs, optionally filtering by enabled status.
func (j *Jobs) List(enabledOnly bool) ([]*CronJob, error) {
	jobs, err := j.load()
	if err != nil {
		return nil, err
	}

	if !enabledOnly {
		return jobs, nil
	}

	var enabled []*CronJob
	for _, job := range jobs {
		if job.Enabled {
			enabled = append(enabled, job)
		}
	}
	return enabled, nil
}

// Remove deletes a job permanently.
func (j *Jobs) Remove(id string) (bool, error) {
	jobs, err := j.load()
	if err != nil {
		return false, err
	}

	found := false
	filtered := make([]*CronJob, 0, len(jobs))
	for _, job := range jobs {
		if job.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, job)
	}

	if !found {
		return false, nil
	}

	if err := j.save(filtered); err != nil {
		return false, err
	}
	return true, nil
}

// Edit applies a partial update to an existing job.
func (j *Jobs) Edit(id string, patch JobPatch) (*CronJob, bool, error) {
	return j.update(id, func(job *CronJob) error {
		return patch.Apply(job)
	})
}

// Pause disables a job without removing it.
func (j *Jobs) Pause(id string) (*CronJob, bool, error) {
	return j.update(id, func(job *CronJob) error {
		job.Enabled = false
		job.NextRunAt = time.Time{}
		return nil
	})
}

// Resume re-enables a paused job and recomputes its next run time.
func (j *Jobs) Resume(id string) (*CronJob, bool, error) {
	return j.update(id, func(job *CronJob) error {
		job.Enabled = true
		job.NextRunAt = job.ComputeNextRun(time.Now().UTC())
		return nil
	})
}

// RunNow schedules a job to run on the next scheduler tick.
func (j *Jobs) RunNow(id string) (*CronJob, bool, error) {
	return j.update(id, func(job *CronJob) error {
		if !job.Enabled {
			return fmt.Errorf("job is disabled")
		}
		job.NextRunAt = time.Now().UTC()
		return nil
	})
}

// Due returns all enabled jobs that should run at or before the given time.
func (j *Jobs) Due(at time.Time) ([]*CronJob, error) {
	jobs, err := j.load()
	if err != nil {
		return nil, err
	}

	var due []*CronJob
	for _, job := range jobs {
		if job.ShouldRun(at) {
			due = append(due, job)
		}
	}
	return due, nil
}

// MarkRun updates a job after execution.
func (j *Jobs) MarkRun(id string, at time.Time) (*CronJob, bool, error) {
	return j.update(id, func(job *CronJob) error {
		job.MarkRun(at)
		return nil
	})
}

// RecordRun adds a job run to the history.
func (j *Jobs) RecordRun(run JobRun) error {
	runs, err := j.loadRuns()
	if err != nil {
		return err
	}

	run.TruncateOutput(2000)
	runs = append([]JobRun{run}, runs...)

	if len(runs) > 1000 {
		runs = runs[:1000]
	}

	return j.saveRuns(runs)
}

// ListRuns returns job runs, optionally filtered by job ID.
func (j *Jobs) ListRuns(jobID string, limit int) ([]JobRun, error) {
	runs, err := j.loadRuns()
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 10
	}

	var filtered []JobRun
	for _, run := range runs {
		if jobID == "" || run.JobID == jobID {
			filtered = append(filtered, run)
			if len(filtered) >= limit {
				break
			}
		}
	}

	return filtered, nil
}

// SaveOutput persists the latest output for context_from chaining.
func (j *Jobs) SaveOutput(jobID, content string) error {
	dir := filepath.Join(j.Dir(), "output", jobID)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	outPath := filepath.Join(dir, "latest.txt")
	if err := os.WriteFile(outPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write job output: %w", err)
	}
	return nil
}

// GetOutput reads the latest persisted output for a job.
func (j *Jobs) GetOutput(jobID string) (string, error) {
	outPath := filepath.Join(j.Dir(), "output", jobID, "latest.txt")
	data, err := os.ReadFile(outPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read job output: %w", err)
	}
	return string(data), nil
}

func (j *Jobs) load() ([]*CronJob, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	data, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []*CronJob{}, nil
		}
		return nil, fmt.Errorf("read jobs file: %w", err)
	}

	var file jobsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse jobs file: %w", err)
	}

	valid := make([]*CronJob, 0, len(file.Jobs))
	now := time.Now().UTC()
	for _, job := range file.Jobs {
		if job == nil {
			continue
		}
		if job.DeliverTo.Kind == "" {
			job.DeliverTo = DefaultDeliveryTarget()
		}
		if err := job.Validate(); err != nil {
			logger.Warning("cron: skipping invalid job",
				logger.BuildMetadata("job_id", job.ID, "job_name", job.Name, "error", err))
			continue
		}
		if job.Schedule.Kind == ScheduleOnce && !job.Schedule.RunAt.After(now) && !job.LastRunAt.IsZero() {
			job.Enabled = false
			job.NextRunAt = time.Time{}
		}
		valid = append(valid, job)
	}

	return valid, nil
}

func (j *Jobs) save(jobs []*CronJob) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	file := jobsFile{
		Jobs:      jobs,
		UpdatedAt: time.Now().UTC(),
		Version:   2,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal jobs: %w", err)
	}

	tmpPath := j.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp jobs file: %w", err)
	}

	if err := os.Rename(tmpPath, j.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename jobs file: %w", err)
	}

	return nil
}

func (j *Jobs) update(id string, updater func(*CronJob) error) (*CronJob, bool, error) {
	jobs, err := j.load()
	if err != nil {
		return nil, false, err
	}

	for _, job := range jobs {
		if job.ID == id {
			if err := updater(job); err != nil {
				return nil, false, err
			}
			job.UpdatedAt = time.Now().UTC()
			if err := job.Validate(); err != nil {
				return nil, false, err
			}
			if err := j.save(jobs); err != nil {
				return nil, false, err
			}
			return job, true, nil
		}
	}

	return nil, false, nil
}

func (j *Jobs) loadRuns() ([]JobRun, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	data, err := os.ReadFile(j.runsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []JobRun{}, nil
		}
		return nil, fmt.Errorf("read runs file: %w", err)
	}

	var file runsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse runs file: %w", err)
	}

	return file.Runs, nil
}

func (j *Jobs) saveRuns(runs []JobRun) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	file := runsFile{
		Runs:      runs,
		UpdatedAt: time.Now().UTC(),
		Version:   2,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runs: %w", err)
	}

	tmpPath := j.runsPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp runs file: %w", err)
	}

	if err := os.Rename(tmpPath, j.runsPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runs file: %w", err)
	}

	return nil
}

func generateJobID() string {
	return uuid.New().String()[:12]
}

var (
	globalJobs   *Jobs
	globalJobsMu sync.RWMutex
)

// Init stores the process-global cron jobs store.
func Init(jobs *Jobs) {
	globalJobsMu.Lock()
	defer globalJobsMu.Unlock()
	globalJobs = jobs
}

// Must returns the process-global cron jobs store.
func Must() *Jobs {
	globalJobsMu.RLock()
	defer globalJobsMu.RUnlock()
	if globalJobs == nil {
		panic("cron: Init not called")
	}
	return globalJobs
}