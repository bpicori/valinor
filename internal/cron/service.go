package cron

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Service provides high-level cron operations and job scheduling.
type Service struct {
	store  *Store
	ticker *time.Ticker
	mu     sync.RWMutex
	// Optional callback for when jobs are due - set by scheduler
	onDueJobs func([]*CronJob)
}

// NewService creates a new cron service with the given storage directory.
func NewService(storageDir string) (*Service, error) {
	store, err := NewStore(storageDir)
	if err != nil {
		return nil, fmt.Errorf("create cron store: %w", err)
	}
	return &Service{store: store}, nil
}

// SetDueCallback registers a function to be called when jobs become due.
// This is used by the scheduler to receive job notifications.
func (s *Service) SetDueCallback(fn func([]*CronJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDueJobs = fn
}

// AddJob creates a new recurring or one-time job.
// For recurring: schedule.Kind = "every", schedule.Every = "5m" or "1h30m"
// For one-time: schedule.Kind = "at", schedule.At = time.Time
func (s *Service) AddJob(name, message string, schedule Schedule) (*CronJob, error) {
	if name == "" {
		return nil, fmt.Errorf("job name is required")
	}
	if message == "" {
		return nil, fmt.Errorf("job message is required")
	}
	if err := schedule.Validate(); err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}

	return s.store.AddJob(name, message, schedule)
}

// ListJobs returns all jobs, optionally filtering by enabled status.
func (s *Service) ListJobs(enabledOnly bool) ([]*CronJob, error) {
	jobs, err := s.store.Load()
	if err != nil {
		return nil, err
	}

	if !enabledOnly {
		return jobs, nil
	}

	var enabled []*CronJob
	for _, j := range jobs {
		if j.Enabled {
			enabled = append(enabled, j)
		}
	}
	return enabled, nil
}

// RemoveJob deletes a job permanently.
func (s *Service) RemoveJob(id string) (bool, error) {
	return s.store.RemoveJob(id)
}

// GetJob retrieves a job by ID.
func (s *Service) GetJob(id string) (*CronJob, bool, error) {
	return s.store.GetJob(id)
}

// EnableJob enables a disabled job.
func (s *Service) EnableJob(id string) (*CronJob, bool, error) {
	return s.store.UpdateJob(id, func(j *CronJob) error {
		j.Enabled = true
		// Recompute NextRunAt if it was cleared
		if j.NextRunAt.IsZero() {
			switch j.Schedule.Kind {
			case ScheduleEvery:
				j.NextRunAt = j.ComputeNextRun(time.Now().UTC())
			case ScheduleAt:
				j.NextRunAt = j.Schedule.At
			}
		}
		return nil
	})
}

// DisableJob disables a job without removing it.
func (s *Service) DisableJob(id string) (*CronJob, bool, error) {
	return s.store.UpdateJob(id, func(j *CronJob) error {
		j.Enabled = false
		j.NextRunAt = time.Time{}
		return nil
	})
}

// Tick checks for due jobs and returns them.
// Callers should execute the jobs and then call MarkJobExecuted.
func (s *Service) Tick(at time.Time) ([]*CronJob, error) {
	return s.store.ListDueJobs(at)
}

// MarkJobExecuted updates a job after successful execution.
func (s *Service) MarkJobExecuted(id string, at time.Time) (*CronJob, bool, error) {
	return s.store.MarkJobRun(id, at)
}

// StartPoller begins a background poller that checks for due jobs.
// The interval determines how frequently to check. Use StopPoller to stop.
func (s *Service) StartPoller(ctx context.Context, interval time.Duration) {
	s.mu.Lock()
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.ticker = time.NewTicker(interval)
	s.mu.Unlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				if s.ticker != nil {
					s.ticker.Stop()
					s.ticker = nil
				}
				s.mu.Unlock()
				return
			case t := <-s.ticker.C:
				jobs, err := s.Tick(t)
				if err != nil {
					// Log but continue - next tick may succeed
					continue
				}
				if len(jobs) > 0 {
					s.mu.RLock()
					cb := s.onDueJobs
					s.mu.RUnlock()
					if cb != nil {
						cb(jobs)
					}
				}
			}
		}
	}()
}

// StopPoller stops the background poller.
func (s *Service) StopPoller() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}
}

// Validate validates a schedule standalone (useful for tool input validation).
func (s *Service) Validate(schedule Schedule) error {
	return schedule.Validate()
}

// DeleteJob permanently removes a job by ID.
func (s *Service) DeleteJob(id string) error {
	return s.store.DeleteJob(id)
}

// RecordRun records a job execution in the run history.
func (s *Service) RecordRun(run JobRun) error {
	return s.store.RecordRun(run)
}

// ListRuns returns job runs from the history, optionally filtered by job ID.
func (s *Service) ListRuns(jobID string, limit int) ([]JobRun, error) {
	return s.store.ListRuns(jobID, limit)
}
