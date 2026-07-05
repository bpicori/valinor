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
		Platform: platformName(),
		Service:  "not installed",
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