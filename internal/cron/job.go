// Package cron provides structured cron job scheduling with persistence.
// Jobs are stored in JSON format and support "every" (interval) and "at" (one-time) schedules.
package cron

import (
	"encoding/json"
	"fmt"
	"time"
)

// ScheduleKind identifies the type of schedule.
type ScheduleKind string

const (
	// ScheduleEvery runs the job at fixed intervals (e.g., "5m", "1h").
	ScheduleEvery ScheduleKind = "every"
	// ScheduleAt runs the job once at a specific time.
	ScheduleAt ScheduleKind = "at"
)

// Schedule defines when a job should run.
type Schedule struct {
	Kind   ScheduleKind `json:"kind"`
	Every  string       `json:"every,omitempty"`   // Duration string like "5m", "1h", "30m" (for ScheduleEvery)
	At     time.Time    `json:"at,omitempty"`      // Specific time to run (for ScheduleAt)
	AtZone string       `json:"at_zone,omitempty"` // IANA timezone for At interpretation
}

// Validate checks the schedule configuration and returns an error if invalid.
func (s Schedule) Validate() error {
	switch s.Kind {
	case ScheduleEvery:
		if s.Every == "" {
			return fmt.Errorf("schedule.every is required for 'every' kind")
		}
		if _, err := time.ParseDuration(s.Every); err != nil {
			return fmt.Errorf("invalid schedule.every duration: %w", err)
		}
	case ScheduleAt:
		if s.At.IsZero() {
			return fmt.Errorf("schedule.at is required for 'at' kind")
		}
	default:
		return fmt.Errorf("invalid schedule.kind: %s (must be 'every' or 'at')", s.Kind)
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
	Kind           string `json:"kind"`                       // origin | client | broadcast | silent
	ClientType     string `json:"client_type,omitempty"`      // for kind=client
	ClientIdentity string `json:"client_identity,omitempty"`  // for kind=client
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
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Message   string         `json:"message"` // The prompt/message to send to the agent
	Schedule  Schedule       `json:"schedule"`
	Enabled   bool           `json:"enabled"`
	NextRunAt time.Time      `json:"next_run_at"` // Computed field: when to run next
	LastRunAt time.Time      `json:"last_run_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeliverTo          DeliveryTarget `json:"deliver_to"`
	CreatedInSessionID string         `json:"created_in_session_id,omitempty"`
}

// Validate checks the job configuration and returns an error if invalid.
func (j *CronJob) Validate() error {
	if j.ID == "" {
		return fmt.Errorf("cron job id is required")
	}
	if j.Name == "" {
		return fmt.Errorf("cron job name is required")
	}
	if j.Message == "" {
		return fmt.Errorf("cron job message is required")
	}

	switch j.Schedule.Kind {
	case ScheduleEvery:
		if j.Schedule.Every == "" {
			return fmt.Errorf("schedule.every is required for 'every' kind")
		}
		if _, err := time.ParseDuration(j.Schedule.Every); err != nil {
			return fmt.Errorf("invalid schedule.every duration: %w", err)
		}
	case ScheduleAt:
		if j.Schedule.At.IsZero() {
			return fmt.Errorf("schedule.at is required for 'at' kind")
		}
	default:
		return fmt.Errorf("invalid schedule.kind: %s (must be 'every' or 'at')", j.Schedule.Kind)
	}

	if err := j.DeliverTo.Validate(); err != nil {
		return err
	}

	return nil
}

// ComputeNextRun calculates when the job should next run based on its schedule.
// For ScheduleEvery, it adds the interval to the reference time.
// For ScheduleAt, it returns the at time if in the future, otherwise zero.
func (j *CronJob) ComputeNextRun(from time.Time) time.Time {
	if !j.Enabled {
		return time.Time{}
	}

	switch j.Schedule.Kind {
	case ScheduleEvery:
		dur, err := time.ParseDuration(j.Schedule.Every)
		if err != nil {
			return time.Time{}
		}
		return from.Add(dur)
	case ScheduleAt:
		if j.Schedule.At.After(from) {
			return j.Schedule.At
		}
		return time.Time{} // One-time job in the past
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
	case ScheduleEvery:
		j.NextRunAt = j.ComputeNextRun(at)
	case ScheduleAt:
		// One-time job: disable after run
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

// CronJobJSON is the serializable representation of a CronJob.
// We use custom marshaling to handle time fields consistently.
type cronJobJSON struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Message   string         `json:"message"`
	Schedule  Schedule       `json:"schedule"`
	Enabled   bool           `json:"enabled"`
	NextRunAt time.Time      `json:"next_run_at"`
	LastRunAt time.Time      `json:"last_run_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeliverTo          DeliveryTarget `json:"deliver_to,omitempty"`
	CreatedInSessionID string         `json:"created_in_session_id,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for CronJob.
func (j CronJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(cronJobJSON{
		ID:        j.ID,
		Name:      j.Name,
		Message:   j.Message,
		Schedule:  j.Schedule,
		Enabled:   j.Enabled,
		NextRunAt: j.NextRunAt,
		LastRunAt: j.LastRunAt,
		CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt,
		DeliverTo:          j.DeliverTo,
		CreatedInSessionID: j.CreatedInSessionID,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for CronJob.
func (j *CronJob) UnmarshalJSON(data []byte) error {
	var tmp cronJobJSON
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*j = CronJob{
		ID:        tmp.ID,
		Name:      tmp.Name,
		Message:   tmp.Message,
		Schedule:  tmp.Schedule,
		Enabled:   tmp.Enabled,
		NextRunAt: tmp.NextRunAt,
		LastRunAt: tmp.LastRunAt,
		CreatedAt: tmp.CreatedAt,
		UpdatedAt: tmp.UpdatedAt,
		DeliverTo:          tmp.DeliverTo,
		CreatedInSessionID: tmp.CreatedInSessionID,
	}
	return nil
}

// NewEveryJob creates a new recurring job that runs at fixed intervals.
func NewEveryJob(id, name, message, every string) (*CronJob, error) {
	if every == "" {
		return nil, fmt.Errorf("interval duration is required")
	}
	if _, err := time.ParseDuration(every); err != nil {
		return nil, fmt.Errorf("invalid interval duration: %w", err)
	}

	now := time.Now().UTC()
	dur, _ := time.ParseDuration(every)

	job := &CronJob{
		ID:        id,
		Name:      name,
		Message:   message,
		Schedule:  Schedule{Kind: ScheduleEvery, Every: every},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
		DeliverTo: DefaultDeliveryTarget(),
	}
	job.NextRunAt = now.Add(dur)

	if err := job.Validate(); err != nil {
		return nil, err
	}
	return job, nil
}

// NewAtJob creates a new one-time job scheduled for a specific time.
func NewAtJob(id, name, message string, at time.Time) (*CronJob, error) {
	if at.IsZero() {
		return nil, fmt.Errorf("schedule time is required")
	}

	now := time.Now().UTC()

	job := &CronJob{
		ID:        id,
		Name:      name,
		Message:   message,
		Schedule:  Schedule{Kind: ScheduleAt, At: at, AtZone: at.Location().String()},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
		DeliverTo: DefaultDeliveryTarget(),
	}
	job.NextRunAt = at

	if err := job.Validate(); err != nil {
		return nil, err
	}
	return job, nil
}
