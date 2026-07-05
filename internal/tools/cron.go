package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/logger"
)



// CronjobName is the tool name for the cronjob tool.
const CronjobName = "cronjob"

func cronjobParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Operation: add, list, remove, history, pause, resume, run, edit",
				"enum":        []string{"add", "list", "remove", "history", "pause", "resume", "run", "edit"},
			},
			"name": map[string]any{
				"type":        "string",
				"description": "For add/edit: short descriptive job name",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "For add/edit (agent jobs only): instruction for the LLM on each run. Omit when using no_agent.",
			},
			"schedule_kind": map[string]any{
				"type":        "string",
				"description": "For add/edit: cron (recurring) or once (one-time)",
				"enum":        []string{"cron", "once"},
			},
			"cron": map[string]any{
				"type":        "string",
				"description": "For schedule_kind=cron: expression like @every 5m, @hourly, or 0 9 * * 1-5",
			},
			"run_at": map[string]any{
				"type":        "string",
				"description": "For schedule_kind=once: RFC3339 timestamp for the one-time run",
			},
			"deliver_to": map[string]any{
				"type":        "object",
				"description": "Delivery target: origin (default), client, broadcast, or silent",
				"properties": map[string]any{
					"kind":            map[string]any{"type": "string", "enum": []string{"origin", "client", "broadcast", "silent"}},
					"client_type":     map[string]any{"type": "string"},
					"client_identity": map[string]any{"type": "string"},
				},
				"required": []string{"kind"},
			},
			"skills": map[string]any{
				"type":        "array",
				"description": "For add/edit: skill names to inject when the job runs",
				"items":       map[string]any{"type": "string"},
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "For add/edit: absolute workspace override for job execution",
			},
			"no_agent": map[string]any{
				"type":        "boolean",
				"description": "For add/edit: run a host script instead of the LLM. Use for monitoring, price alerts, health checks.",
			},
			"script": map[string]any{
				"type":        "string",
				"description": "For add/edit: script filename only (e.g. btc-alert.sh), not scripts/btc-alert.sh. File must already exist in workspace scripts/. Required when no_agent=true.",
			},
			"context_from": map[string]any{
				"type":        "string",
				"description": "For add/edit: job ID whose last output is prepended to this job's prompt",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "For remove/history/pause/resume/run/edit: target job ID",
			},
			"enabled_only": map[string]any{
				"type":        "boolean",
				"description": "For list: show only enabled jobs (default true)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "For history: max entries (default 10, max 100)",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

type cronjobArgs struct {
	Action       string   `json:"action"`
	Name         string   `json:"name,omitempty"`
	Message      string   `json:"message,omitempty"`
	ScheduleKind string   `json:"schedule_kind,omitempty"`
	Cron         string   `json:"cron,omitempty"`
	RunAt        string   `json:"run_at,omitempty"`
	DeliverTo    *struct {
		Kind           string `json:"kind"`
		ClientType     string `json:"client_type,omitempty"`
		ClientIdentity string `json:"client_identity,omitempty"`
	} `json:"deliver_to,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	Workdir      string   `json:"workdir,omitempty"`
	NoAgent      *bool    `json:"no_agent,omitempty"`
	Script       string   `json:"script,omitempty"`
	ContextFrom string `json:"context_from,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	EnabledOnly  *bool    `json:"enabled_only,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

func cronjobExec(runCtx *RunContext, args json.RawMessage) (string, error) {
	var p cronjobArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	switch p.Action {
	case "add":
		return cronjobAdd(runCtx, p)
	case "list":
		return cronjobList(p)
	case "remove":
		return cronjobRemove(p)
	case "history":
		return cronjobHistory(p)
	case "pause":
		return cronjobPause(p)
	case "resume":
		return cronjobResume(p)
	case "run":
		return cronjobRun(p)
	case "edit":
		return cronjobEdit(p)
	default:
		return fmt.Sprintf("Error: unknown action %q", p.Action), nil
	}
}

func cronjobAdd(runCtx *RunContext, p cronjobArgs) (string, error) {
	if p.Name == "" {
		return "Error: name is required for add", nil
	}

	schedule, err := parseSchedule(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}

	deliverTo := cron.DefaultDeliveryTarget()
	if p.DeliverTo != nil {
		deliverTo = cron.DeliveryTarget{
			Kind:           p.DeliverTo.Kind,
			ClientType:     p.DeliverTo.ClientType,
			ClientIdentity: p.DeliverTo.ClientIdentity,
		}
	}

	var createdInSessionID string
	if snap, ok := runCtx.Snapshot(); ok {
		createdInSessionID = snap.SessionID
	}

	noAgent := p.NoAgent != nil && *p.NoAgent
	script := p.Script
	if noAgent {
		if err := validateCronScript(script); err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
	}

	job, err := cron.Must().Create(cron.CreateJobInput{
		Name:               p.Name,
		Message:            p.Message,
		Schedule:           schedule,
		CreatedInSessionID: createdInSessionID,
		DeliverTo:          deliverTo,
		Skills:             p.Skills,
		Workdir:            p.Workdir,
		NoAgent:            noAgent,
		Script:             script,
		ContextFrom:        p.ContextFrom,
	})
	if err != nil {
		logger.Error("cronjob add failed", logger.BuildMetadata("name", p.Name, "error", err))
		return fmt.Sprintf("Error creating cron job: %v", err), nil
	}

	return fmt.Sprintf("Created cron job '%s' (ID: %s), schedule: %s, next run: %s",
		job.Name, job.ID, formatSchedule(job.Schedule), formatNextRun(job.NextRunAt)), nil
}

func cronjobEdit(p cronjobArgs) (string, error) {
	if p.JobID == "" {
		return "Error: job_id is required for edit", nil
	}

	patch := cron.JobPatch{}
	hasPatch := false

	if p.Name != "" {
		patch.Name = &p.Name
		hasPatch = true
	}
	if p.Message != "" {
		patch.Message = &p.Message
		hasPatch = true
	}
	if p.ScheduleKind != "" {
		schedule, err := parseSchedule(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		patch.Schedule = &schedule
		hasPatch = true
	}
	if p.DeliverTo != nil {
		dt := cron.DeliveryTarget{
			Kind:           p.DeliverTo.Kind,
			ClientType:     p.DeliverTo.ClientType,
			ClientIdentity: p.DeliverTo.ClientIdentity,
		}
		patch.DeliverTo = &dt
		hasPatch = true
	}
	if p.Skills != nil {
		patch.Skills = &p.Skills
		hasPatch = true
	}
	if p.Workdir != "" {
		patch.Workdir = &p.Workdir
		hasPatch = true
	}
	if p.NoAgent != nil {
		patch.NoAgent = p.NoAgent
		hasPatch = true
	}
	if p.Script != "" {
		if err := validateCronScript(p.Script); err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		patch.Script = &p.Script
		hasPatch = true
	}
	if p.ContextFrom != "" {
		patch.ContextFrom = &p.ContextFrom
		hasPatch = true
	}

	if !hasPatch {
		return "Error: no fields to update", nil
	}

	job, ok, err := cron.Must().Edit(p.JobID, patch)
	if err != nil {
		return fmt.Sprintf("Error editing job: %v", err), nil
	}
	if !ok {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}

	return fmt.Sprintf("Updated cron job '%s' (ID: %s), schedule: %s, next run: %s",
		job.Name, job.ID, formatSchedule(job.Schedule), formatNextRun(job.NextRunAt)), nil
}

func cronjobList(p cronjobArgs) (string, error) {
	enabledOnly := true
	if p.EnabledOnly != nil {
		enabledOnly = *p.EnabledOnly
	}

	jobList, err := cron.Must().List(enabledOnly)
	if err != nil {
		return fmt.Sprintf("Error listing cron jobs: %v", err), nil
	}
	if len(jobList) == 0 {
		if enabledOnly {
			return "No active scheduled jobs.", nil
		}
		return "No scheduled jobs found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d job(s):\n\n", len(jobList)))
	for _, j := range jobList {
		status := "enabled"
		if !j.Enabled {
			status = "paused"
		}
		sb.WriteString(fmt.Sprintf("• %s (ID: %s)\n", j.Name, j.ID))
		sb.WriteString(fmt.Sprintf("  Status: %s | Schedule: %s\n", status, formatSchedule(j.Schedule)))
		sb.WriteString(fmt.Sprintf("  Next run: %s | Message: %.60s...\n\n", formatNextRun(j.NextRunAt), j.Message))
	}
	return sb.String(), nil
}

func cronjobRemove(p cronjobArgs) (string, error) {
	if p.JobID == "" {
		return "Error: job_id is required for remove", nil
	}
	removed, err := cron.Must().Remove(p.JobID)
	if err != nil {
		return fmt.Sprintf("Error removing job: %v", err), nil
	}
	if !removed {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}
	return fmt.Sprintf("Removed cron job (ID: %s)", p.JobID), nil
}

func cronjobHistory(p cronjobArgs) (string, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	runs, err := cron.Must().ListRuns(p.JobID, limit)
	if err != nil {
		return fmt.Sprintf("Error listing job history: %v", err), nil
	}
	if len(runs) == 0 {
		if p.JobID != "" {
			return fmt.Sprintf("No run history found for job ID: %s", p.JobID), nil
		}
		return "No job run history found.", nil
	}

	var sb strings.Builder
	if p.JobID != "" {
		sb.WriteString(fmt.Sprintf("Run history for job %s (%d entries):\n\n", p.JobID, len(runs)))
	} else {
		sb.WriteString(fmt.Sprintf("Job run history (%d entries):\n\n", len(runs)))
	}
	for i, run := range runs {
		statusEmoji := "✓"
		if run.Status == "error" {
			statusEmoji = "✗"
		}
		sb.WriteString(fmt.Sprintf("%d. %s %s (Job: %s)\n", i+1, statusEmoji, run.FiredAt.Format("2006-01-02 15:04 MST"), run.JobID))
		sb.WriteString(fmt.Sprintf("   Status: %s\n", run.Status))
		if run.Output != "" {
			sb.WriteString(fmt.Sprintf("   Output: %.100s", run.Output))
			if len(run.Output) > 100 {
				sb.WriteString("...")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func cronjobPause(p cronjobArgs) (string, error) {
	if p.JobID == "" {
		return "Error: job_id is required for pause", nil
	}
	job, ok, err := cron.Must().Pause(p.JobID)
	if err != nil {
		return fmt.Sprintf("Error pausing job: %v", err), nil
	}
	if !ok {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}
	return fmt.Sprintf("Paused cron job '%s' (ID: %s)", job.Name, job.ID), nil
}

func cronjobResume(p cronjobArgs) (string, error) {
	if p.JobID == "" {
		return "Error: job_id is required for resume", nil
	}
	job, ok, err := cron.Must().Resume(p.JobID)
	if err != nil {
		return fmt.Sprintf("Error resuming job: %v", err), nil
	}
	if !ok {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}
	return fmt.Sprintf("Resumed cron job '%s' (ID: %s), next run: %s", job.Name, job.ID, formatNextRun(job.NextRunAt)), nil
}

func cronjobRun(p cronjobArgs) (string, error) {
	if p.JobID == "" {
		return "Error: job_id is required for run", nil
	}
	job, ok, err := cron.Must().RunNow(p.JobID)
	if err != nil {
		return fmt.Sprintf("Error scheduling job run: %v", err), nil
	}
	if !ok {
		return fmt.Sprintf("No job found with ID: %s", p.JobID), nil
	}
	return fmt.Sprintf("Scheduled immediate run for job '%s' (ID: %s)", job.Name, job.ID), nil
}

func validateCronScript(filename string) error {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return fmt.Errorf("script is required when no_agent is true")
	}
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") || filepath.IsAbs(filename) {
		return fmt.Errorf("script must be a filename only (e.g. btc-alert.sh), not a path")
	}

	paths := config.MustPaths()
	scriptPath := filepath.Join(paths.ScriptsDir, filename)
	if _, err := os.Stat(scriptPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("script not found in workspace scripts/: %s", filename)
		}
		return fmt.Errorf("check script %s: %w", filename, err)
	}
	return nil
}

func parseSchedule(p cronjobArgs) (cron.Schedule, error) {
	schedule := cron.Schedule{}
	switch p.ScheduleKind {
	case "cron":
		if p.Cron == "" {
			return schedule, fmt.Errorf("'cron' expression is required when schedule_kind is 'cron'")
		}
		schedule.Kind = cron.ScheduleCron
		schedule.Cron = p.Cron
	case "once":
		if p.RunAt == "" {
			return schedule, fmt.Errorf("'run_at' timestamp is required when schedule_kind is 'once'")
		}
		t, err := time.Parse(time.RFC3339, p.RunAt)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05", p.RunAt)
			if err != nil {
				return schedule, fmt.Errorf("invalid timestamp %q, use RFC3339", p.RunAt)
			}
			t = t.UTC()
		}
		schedule.Kind = cron.ScheduleOnce
		schedule.RunAt = t
	case "":
		return schedule, fmt.Errorf("schedule_kind is required")
	default:
		return schedule, fmt.Errorf("invalid schedule_kind %q (must be 'cron' or 'once')", p.ScheduleKind)
	}
	return schedule, nil
}

func formatSchedule(s cron.Schedule) string {
	switch s.Kind {
	case cron.ScheduleCron:
		return fmt.Sprintf("cron %s", s.Cron)
	case cron.ScheduleOnce:
		return fmt.Sprintf("once at %s", s.RunAt.Format(time.RFC3339))
	default:
		return "unknown"
	}
}

func formatNextRun(t time.Time) string {
	if t.IsZero() {
		return "not scheduled"
	}
	return t.Format("2006-01-02 15:04 MST")
}