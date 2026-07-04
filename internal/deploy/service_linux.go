//go:build linux

package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	serviceUnit    = "valinor.service"
	updateService  = "valinor-update.service"
	updateTimer    = "valinor-update.timer"
	systemdUserDir = ".config/systemd/user"
)

func platformName() string { return "systemd (linux)" }

func platformInstall(ctx context.Context, paths Paths) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("missing required command: systemctl")
	}
	if err := writeSystemdUnits(paths); err != nil {
		return err
	}
	if err := systemdRun(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := systemdRun(ctx, "enable", "--now", serviceUnit); err != nil {
		return err
	}
	if err := systemdRun(ctx, "enable", "--now", updateTimer); err != nil {
		return err
	}
	return enableLinger(ctx)
}

func platformUninstall(ctx context.Context, paths Paths) error {
	_ = systemdRun(ctx, "disable", "--now", updateTimer)
	_ = systemdRun(ctx, "disable", "--now", serviceUnit)

	unitDir := filepath.Join(paths.Home, systemdUserDir)
	for _, name := range []string{serviceUnit, updateService, updateTimer} {
		_ = os.Remove(filepath.Join(unitDir, name))
	}
	return systemdRun(ctx, "daemon-reload")
}

func platformStatus(ctx context.Context, paths Paths) (Status, error) {
	status := Status{
		Platform:    platformName(),
		Service:     "not installed",
		UpdateTimer: "not installed",
	}

	if !systemdUnitInstalled(paths, serviceUnit) {
		return status, nil
	}

	active, detail, err := systemdUnitActiveState(ctx, serviceUnit)
	if err != nil {
		return status, err
	}
	status.Service = active
	status.ServiceDetail = detail

	if systemdUnitInstalled(paths, updateTimer) {
		timerActive, timerDetail, err := systemdTimerState(ctx)
		if err != nil {
			return status, err
		}
		status.UpdateTimer = timerActive
		status.UpdateDetail = timerDetail
	}
	return status, nil
}

func platformRestart(ctx context.Context, paths Paths) error {
	if !systemdUnitInstalled(paths, serviceUnit) {
		return fmt.Errorf("service is not installed")
	}
	return systemdRun(ctx, "restart", serviceUnit)
}

func platformRestartIfInstalled(ctx context.Context, paths Paths) error {
	if systemdIsActive(ctx, serviceUnit) {
		return systemdRun(ctx, "restart", serviceUnit)
	}
	if systemdIsEnabled(ctx, serviceUnit) {
		return systemdRun(ctx, "start", serviceUnit)
	}
	fmt.Println("service not enabled; binary updated but service not restarted")
	return nil
}

func platformLogs(ctx context.Context, paths Paths, opts LogOptions) error {
	if opts.Deploy {
		return tailFile(paths.DeployLog, opts)
	}

	args := []string{"--user", "-u", ServiceName}
	if opts.Lines > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", opts.Lines))
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func writeSystemdUnits(paths Paths) error {
	unitDir := filepath.Join(paths.Home, systemdUserDir)
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}

	return writeFiles(unitDir, map[string]string{
		serviceUnit:   systemdMainUnit(paths),
		updateService: systemdUpdateUnit(paths),
		updateTimer:   systemdUpdateTimer(),
	})
}

func systemdMainUnit(paths Paths) string {
	return fmt.Sprintf(`[Unit]
Description=Valinor gateway and Telegram bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run --mode=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, paths.Binary, DefaultMode)
}

func systemdUpdateUnit(paths Paths) string {
	return fmt.Sprintf(`[Unit]
Description=Check for Valinor release updates
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s update
`, paths.Binary)
}

func systemdUpdateTimer() string {
	return `[Unit]
Description=Check for Valinor releases every 30 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=30min
Persistent=true

[Install]
WantedBy=timers.target
`
}

func enableLinger(ctx context.Context) error {
	if _, err := exec.LookPath("loginctl"); err != nil {
		return fmt.Errorf("missing required command: loginctl")
	}
	user := os.Getenv("USER")
	if user == "" {
		return fmt.Errorf("USER environment variable is empty")
	}

	out, err := exec.CommandContext(ctx, "loginctl", "show-user", user, "-p", "Linger", "--value").Output()
	if err != nil {
		return fmt.Errorf("check systemd linger: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(string(out)), "yes") {
		return nil
	}
	if err := exec.CommandContext(ctx, "loginctl", "enable-linger", user).Run(); err != nil {
		return fmt.Errorf("enable systemd linger: %w", err)
	}
	fmt.Printf("Enabled systemd linger for %s (service runs when logged out)\n", user)
	return nil
}

func systemdUnitInstalled(paths Paths, name string) bool {
	_, err := os.Stat(filepath.Join(paths.Home, systemdUserDir, name))
	return err == nil
}

func systemdUnitActiveState(ctx context.Context, unit string) (string, string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", unit, "-p", "ActiveState", "-p", "SubState", "--value").Output()
	if err != nil {
		return "not installed", "", fmt.Errorf("read service state: %w", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) == 0 {
		return "inactive", "", nil
	}
	return mapSystemdState(lines[0]), strings.Join(lines, " "), nil
}

func systemdTimerState(ctx context.Context) (string, string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "list-timers", updateTimer, "--no-pager").Output()
	if err != nil {
		return "unknown", "", fmt.Errorf("read update timer state: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if !strings.Contains(text, updateTimer) {
		return "not enabled", "", nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return "enabled", text, nil
	}
	return "enabled", strings.TrimSpace(lines[len(lines)-1]), nil
}

func systemdIsActive(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", unit).Run() == nil
}

func systemdIsEnabled(ctx context.Context, unit string) bool {
	return exec.CommandContext(ctx, "systemctl", "--user", "is-enabled", "--quiet", unit).Run() == nil
}

func systemdRun(ctx context.Context, args ...string) error {
	cmdArgs := append([]string{"--user"}, args...)
	cmd := exec.CommandContext(ctx, "systemctl", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func mapSystemdState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "active":
		return "active"
	case "failed":
		return "failed"
	default:
		return "inactive"
	}
}