//go:build linux

package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const (
	systemdUserDir = ".config/systemd/user"
	templateUnit   = "valinor@.service"
)

func platformName() string { return "systemd (linux)" }

func platformInstall(ctx context.Context, paths Paths) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	unitDir := filepath.Join(paths.Home, systemdUserDir)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	unit := template.Must(template.New("unit").Parse(`[Unit]
Description=Valinor instance %i
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h
ExecStart={{.Executable}} run --instance %i
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=45
Environment=PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin

[Install]
WantedBy=default.target
`))
	unitPath := filepath.Join(unitDir, templateUnit)
	file, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("create service unit: %w", err)
	}
	if err := unit.Execute(file, struct{ Executable string }{executable}); err != nil {
		file.Close()
		return fmt.Errorf("render service unit: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close service unit: %w", err)
	}
	if err := os.Chmod(unitPath, 0o644); err != nil {
		return fmt.Errorf("chmod service unit: %w", err)
	}
	if err := systemdRun(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := systemdRun(ctx, "enable", "--now", paths.ServiceUnit); err != nil {
		return err
	}
	if !lingerEnabled(ctx) {
		fmt.Println("Note: systemd user lingering is disabled; enable it with `sudo loginctl enable-linger $USER` for boot startup.")
	}
	return nil
}

func platformUninstall(ctx context.Context, paths Paths) error {
	_ = systemdRun(ctx, "disable", "--now", paths.ServiceUnit)
	return systemdRun(ctx, "daemon-reload")
}

func platformStatus(ctx context.Context, paths Paths) (Status, error) {
	status := Status{
		Platform: platformName(),
		Service:  "not installed",
	}
	if !systemdUnitLoaded(ctx, paths.ServiceUnit) {
		return status, nil
	}
	active, detail, err := systemdUnitActiveState(ctx, paths.ServiceUnit)
	if err != nil {
		return status, err
	}
	status.Service = active
	status.ServiceDetail = detail
	return status, nil
}

func platformRestart(ctx context.Context, paths Paths) error {
	if !systemdUnitLoaded(ctx, paths.ServiceUnit) {
		return fmt.Errorf("service is not installed")
	}
	return systemdRun(ctx, "restart", paths.ServiceUnit)
}

func platformRestartIfInstalled(ctx context.Context, paths Paths) error {
	if systemdIsActive(ctx, paths.ServiceUnit) {
		return systemdRun(ctx, "restart", paths.ServiceUnit)
	}
	if systemdIsEnabled(ctx, paths.ServiceUnit) {
		return systemdRun(ctx, "start", paths.ServiceUnit)
	}
	fmt.Println("service not enabled; binary updated but service not restarted")
	return nil
}

func platformRestartAll(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "list-units", "--type=service", "--state=active", "--no-legend", "valinor@*.service").Output()
	if err != nil {
		return fmt.Errorf("list active instance services: %w", err)
	}
	units := make([]string, 0)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "valinor@") {
			continue
		}
		units = append(units, fields[0])
	}
	if len(units) == 0 {
		fmt.Println("no active Valinor instance services to restart")
		return nil
	}
	for _, unit := range units {
		if err := systemdRun(ctx, "restart", unit); err != nil {
			return err
		}
		fmt.Printf("restarted %s\n", unit)
	}
	return nil
}

func platformLogs(ctx context.Context, paths Paths, opts LogOptions) error {
	args := []string{"--user", "-u", paths.ServiceUnit}
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

func systemdUnitLoaded(ctx context.Context, unit string) bool {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", unit, "-p", "LoadState", "--value").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "loaded"
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

func lingerEnabled(ctx context.Context) bool {
	user := os.Getenv("USER")
	if user == "" {
		return true
	}
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", user, "-p", "Linger", "--value").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) == "yes"
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
