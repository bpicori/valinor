package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store provides persistent storage for cron jobs.
type Store struct {
	path     string
	runsPath string
	mu       sync.RWMutex
}

// NewStore creates a new job store at the given directory.
// Jobs are stored in a single JSON file at <dir>/jobs.json.
// Job run history is stored at <dir>/runs.json.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create cron directory: %w", err)
	}
	return &Store{
		path:     filepath.Join(dir, "jobs.json"),
		runsPath: filepath.Join(dir, "runs.json"),
	}, nil
}

// jobsFile is the root structure of the persisted jobs file.
type jobsFile struct {
	Jobs      []*CronJob `json:"jobs"`
	UpdatedAt time.Time  `json:"updated_at"`
	Version   int        `json:"version"` // For future migrations
}

// Load reads all jobs from storage.
func (s *Store) Load() ([]*CronJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.path)
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

	// Validate and clean up jobs (remove invalid entries, disable expired one-time jobs)
	valid := make([]*CronJob, 0, len(file.Jobs))
	now := time.Now().UTC()
	for _, j := range file.Jobs {
		if j == nil {
			continue
		}
		if j.DeliverTo.Kind == "" {
			j.DeliverTo = DefaultDeliveryTarget()
		}
		if err := j.Validate(); err != nil {
			continue
		}
		// Disable expired one-time jobs
		if j.Schedule.Kind == ScheduleAt && !j.Schedule.At.After(now) && !j.LastRunAt.IsZero() {
			j.Enabled = false
			j.NextRunAt = time.Time{}
		}
		valid = append(valid, j)
	}

	return valid, nil
}

// Save persists the given jobs atomically.
func (s *Store) Save(jobs []*CronJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file := jobsFile{
		Jobs:      jobs,
		UpdatedAt: time.Now().UTC(),
		Version:   1,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal jobs: %w", err)
	}

	// Write to temp file then rename for atomicity
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp jobs file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		// Attempt cleanup
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename jobs file: %w", err)
	}

	return nil
}

// AddJob creates and stores a new job with a generated ID.
func (s *Store) AddJob(name, message string, schedule Schedule, createdInSessionID string, deliverTo DeliveryTarget) (*CronJob, error) {
	jobs, err := s.Load()
	if err != nil {
		return nil, err
	}

	// Generate ID and check uniqueness
	id := generateJobID()
	for _, existing := range jobs {
		if existing.ID == id {
			id = generateJobID() // retry once
			break
		}
	}

	if deliverTo.Kind == "" {
		deliverTo = DefaultDeliveryTarget()
	}

	now := time.Now().UTC()
	job := &CronJob{
		ID:                 id,
		Name:               name,
		Message:            message,
		Schedule:           schedule,
		Enabled:            true,
		CreatedAt:          now,
		UpdatedAt:          now,
		DeliverTo:          deliverTo,
		CreatedInSessionID: createdInSessionID,
	}

	// Compute initial NextRunAt
	switch schedule.Kind {
	case ScheduleEvery:
		if schedule.Every != "" {
			if dur, err := time.ParseDuration(schedule.Every); err == nil {
				job.NextRunAt = now.Add(dur)
			}
		}
	case ScheduleAt:
		job.NextRunAt = schedule.At
	}

	if err := job.Validate(); err != nil {
		return nil, err
	}

	jobs = append(jobs, job)
	if err := s.Save(jobs); err != nil {
		return nil, err
	}

	return job, nil
}

// GetJob retrieves a job by ID.
func (s *Store) GetJob(id string) (*CronJob, bool, error) {
	jobs, err := s.Load()
	if err != nil {
		return nil, false, err
	}

	for _, j := range jobs {
		if j.ID == id {
			return j, true, nil
		}
	}
	return nil, false, nil
}

// RemoveJob deletes a job by ID.
func (s *Store) RemoveJob(id string) (bool, error) {
	jobs, err := s.Load()
	if err != nil {
		return false, err
	}

	found := false
	filtered := make([]*CronJob, 0, len(jobs))
	for _, j := range jobs {
		if j.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, j)
	}

	if !found {
		return false, nil
	}

	if err := s.Save(filtered); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateJob modifies an existing job.
func (s *Store) UpdateJob(id string, updater func(*CronJob) error) (*CronJob, bool, error) {
	jobs, err := s.Load()
	if err != nil {
		return nil, false, err
	}

	for _, j := range jobs {
		if j.ID == id {
			if err := updater(j); err != nil {
				return nil, false, err
			}
			j.UpdatedAt = time.Now().UTC()
			if err := j.Validate(); err != nil {
				return nil, false, err
			}
			if err := s.Save(jobs); err != nil {
				return nil, false, err
			}
			return j, true, nil
		}
	}

	return nil, false, nil
}

// ListDueJobs returns all enabled jobs that should run at or before the given time.
func (s *Store) ListDueJobs(at time.Time) ([]*CronJob, error) {
	jobs, err := s.Load()
	if err != nil {
		return nil, err
	}

	var due []*CronJob
	for _, j := range jobs {
		if j.ShouldRun(at) {
			due = append(due, j)
		}
	}
	return due, nil
}

// MarkJobRun updates a job after execution, saving the updated state.
func (s *Store) MarkJobRun(id string, at time.Time) (*CronJob, bool, error) {
	return s.UpdateJob(id, func(j *CronJob) error {
		j.MarkRun(at)
		return nil
	})
}

func generateJobID() string {
	// Short 12-char prefix of UUID is enough for internal IDs
	return uuid.New().String()[:12]
}

// runsFile is the root structure of the persisted runs file.
type runsFile struct {
	Runs      []JobRun  `json:"runs"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

// LoadRuns reads all job runs from storage.
func (s *Store) LoadRuns() ([]JobRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.runsPath)
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

// SaveRuns persists the given job runs atomically.
func (s *Store) SaveRuns(runs []JobRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file := runsFile{
		Runs:      runs,
		UpdatedAt: time.Now().UTC(),
		Version:   1,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runs: %w", err)
	}

	// Write to temp file then rename for atomicity
	tmpPath := s.runsPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp runs file: %w", err)
	}

	if err := os.Rename(tmpPath, s.runsPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runs file: %w", err)
	}

	return nil
}

// RecordRun adds a new job run to the history.
func (s *Store) RecordRun(run JobRun) error {
	runs, err := s.LoadRuns()
	if err != nil {
		return err
	}

	// Truncate output before storing
	run.TruncateOutput(2000)

	// Add new run to the beginning
	runs = append([]JobRun{run}, runs...)

	// Keep only the last 1000 runs to prevent unbounded growth
	if len(runs) > 1000 {
		runs = runs[:1000]
	}

	return s.SaveRuns(runs)
}

// ListRuns returns job runs, optionally filtered by job ID, limited to the specified count.
func (s *Store) ListRuns(jobID string, limit int) ([]JobRun, error) {
	runs, err := s.LoadRuns()
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

// DeleteJob permanently removes a job by ID. Alias for RemoveJob.
func (s *Store) DeleteJob(id string) error {
	_, err := s.RemoveJob(id)
	return err
}
