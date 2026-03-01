package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"valinor/internal/cron"
	"valinor/internal/logger"
)

// cronServiceGetter allows the tool to access the cron service.
// This is set by the tool initializer during startup.
var cronService cronServiceInterface

// cronServiceInterface abstracts the cron service for tool operations.
type cronServiceInterface interface {
	AddJob(name, message string, schedule cron.Schedule) (*cron.CronJob, error)
	ListJobs(enabledOnly bool) ([]*cron.CronJob, error)
	RemoveJob(id string) (bool, error)
	GetJob(id string) (*cron.CronJob, bool, error)
}

// SetCronService sets the cron service for tool operations.
// Called during system initialization.
func SetCronService(svc cronServiceInterface) {
	cronService = svc
}

const cronAddName = "cron_add"
const cronListName = "cron_list"
const cronRemoveName = "cron_remove"

// cronAddParams returns the parameters schema for cron_add.
func cronAddParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Short descriptive name for the job (e.g., 'Daily Weather', 'Hourly Check')",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The message/prompt to send to the AI when the job runs. Be specific about what to do.",
			},
			"schedule_kind": map[string]any{
				"type":        "string",
				"description": "Type of schedule: 'every' for recurring intervals, 'at' for one-time specific time",
				"enum":        []string{"every", "at"},
			},
			"every": map[string]any{
				"type":        "string",
				"description": "For 'every' schedule: interval duration like '5m', '30m', '1h', '1h30m'. Minimum 1 minute.",
			},
			"at": map[string]any{
				"type":        "string",
				"description": "For 'at' schedule: ISO8601/RFC3339 timestamp when to run once, e.g., '2026-03-01T09:00:00Z'",
			},
		},
		"required":             []string{"name", "message", "schedule_kind"},
		"additionalProperties": false,
	}
}

// cronAddExec executes the cron_add tool.
func cronAddExec(args json.RawMessage) (string, error) {
	if cronService == nil {
		return "Error: cron service not initialized", nil
	}

	var p struct {
		Name         string `json:"name"`
		Message      string `json:"message"`
		ScheduleKind string `json:"schedule_kind"`
		Every        string `json:"every,omitempty"`
		At           string `json:"at,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Validate inputs
	if p.Name == "" {
		return "Error: name is required", nil
	}
	if p.Message == "" {
		return "Error: message is required", nil
	}

	schedule := cron.Schedule{}

	switch p.ScheduleKind {
	case "every":
		if p.Every == "" {
			return "Error: 'every' duration is required when schedule_kind is 'every'", nil
		}
		// Validate duration
		dur, err := time.ParseDuration(p.Every)
		if err != nil {
			return fmt.Sprintf("Error: invalid duration '%s': %v", p.Every, err), nil
		}
		if dur < time.Minute {
			return "Error: minimum interval is 1 minute", nil
		}
		schedule.Kind = cron.ScheduleEvery
		schedule.Every = p.Every
	case "at":
		if p.At == "" {
			return "Error: 'at' timestamp is required when schedule_kind is 'at'", nil
		}
		t, err := time.Parse(time.RFC3339, p.At)
		if err != nil {
			// Try parsing without timezone suffix
			t, err = time.Parse("2006-01-02T15:04:05", p.At)
			if err != nil {
				return fmt.Sprintf("Error: invalid timestamp '%s'. Use RFC3339 format like '2026-03-01T09:00:00Z'", p.At), nil
			}
			t = t.UTC()
		}
		if t.Before(time.Now().UTC()) {
			return "Error: scheduled time is in the past", nil
		}
		schedule.Kind = cron.ScheduleAt
		schedule.At = t
	default:
		return fmt.Sprintf("Error: invalid schedule_kind '%s'. Must be 'every' or 'at'", p.ScheduleKind), nil
	}

	job, err := cronService.AddJob(p.Name, p.Message, schedule)
	if err != nil {
		logger.Error("cron_add failed", logger.BuildMetadata("name", p.Name, "error", err))
		return fmt.Sprintf("Error creating cron job: %v", err), nil
	}

	var scheduleDesc string
	if job.Schedule.Kind == cron.ScheduleEvery {
		scheduleDesc = fmt.Sprintf("every %s", job.Schedule.Every)
	} else {
		scheduleDesc = fmt.Sprintf("at %s", job.Schedule.At.Format(time.RFC3339))
	}

	return fmt.Sprintf("Created cron job '%s' (ID: %s) with schedule: %s, next run: %s",
		job.Name, job.ID, scheduleDesc, job.NextRunAt.Format("2006-01-02 15:04 MST")), nil
}

// cronListParams returns the parameters schema for cron_list.
func cronListParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"enabled_only": map[string]any{
				"type":        "boolean",
				"description": "If true, only show enabled jobs (default: true)",
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	}
}

// cronListExec executes the cron_list tool.
func cronListExec(args json.RawMessage) (string, error) {
	if cronService == nil {
		return "Error: cron service not initialized", nil
	}

	var p struct {
		EnabledOnly bool `json:"enabled_only"`
	}
	// Default to showing only enabled jobs
	p.EnabledOnly = true
	_ = json.Unmarshal(args, &p) // Unmarshal errors are fine, use defaults

	jobs, err := cronService.ListJobs(p.EnabledOnly)
	if err != nil {
		logger.Error("cron_list failed", logger.BuildMetadata("error", err))
		return fmt.Sprintf("Error listing cron jobs: %v", err), nil
	}

	if len(jobs) == 0 {
		if p.EnabledOnly {
			return "No active scheduled jobs. Use cron_add to create one.", nil
		}
		return "No scheduled jobs found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d job(s):\n\n", len(jobs)))

	for _, j := range jobs {
		status := "enabled"
		if !j.Enabled {
			status = "disabled"
		}

		var scheduleStr string
		if j.Schedule.Kind == cron.ScheduleEvery {
			scheduleStr = fmt.Sprintf("every %s", j.Schedule.Every)
		} else {
			scheduleStr = fmt.Sprintf("at %s", j.Schedule.At.Format(time.RFC3339))
		}

		nextRun := "not scheduled"
		if !j.NextRunAt.IsZero() {
			nextRun = j.NextRunAt.Format("2006-01-02 15:04 MST")
		}

		sb.WriteString(fmt.Sprintf("• %s (ID: %s)\n", j.Name, j.ID))
		sb.WriteString(fmt.Sprintf("  Status: %s | Schedule: %s\n", status, scheduleStr))
		sb.WriteString(fmt.Sprintf("  Next run: %s | Message: %.60s...\n\n",
			nextRun, j.Message))
	}

	return sb.String(), nil
}

// cronRemoveParams returns the parameters schema for cron_remove.
func cronRemoveParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]string{
				"type":        "string",
				"description": "The job ID to remove (get from cron_list)",
			},
		},
		"required":             []string{"job_id"},
		"additionalProperties": false,
	}
}

// cronRemoveExec executes the cron_remove tool.
func cronRemoveExec(args json.RawMessage) (string, error) {
	if cronService == nil {
		return "Error: cron service not initialized", nil
	}

	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if p.JobID == "" {
		return "Error: job_id is required", nil
	}

	removed, err := cronService.RemoveJob(p.JobID)
	if err != nil {
		logger.Error("cron_remove failed", logger.BuildMetadata("job_id", p.JobID, "error", err))
		return fmt.Sprintf("Error removing job: %v", err), nil
	}

	if !removed {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}

	return fmt.Sprintf("Removed cron job (ID: %s)", p.JobID), nil
}
