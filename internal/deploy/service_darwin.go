//go:build darwin

package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	agentLabel  = "com.valinor.agent"
	updateLabel = "com.valinor.update"
	agentsDir   = "Library/LaunchAgents"
)

func platformName() string { return "launchd (darwin)" }

func platformInstall(ctx context.Context, paths Paths) error {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("missing required command: launchctl")
	}
	if err := writeLaunchdPlists(paths); err != nil {
		return err
	}
	if err := launchdBootstrap(ctx, paths, agentLabel); err != nil {
		return err
	}
	return launchdBootstrap(ctx, paths, updateLabel)
}

func platformUninstall(ctx context.Context, paths Paths) error {
	agentDir := filepath.Join(paths.Home, agentsDir)
	for _, label := range []string{updateLabel, agentLabel} {
		plist := filepath.Join(agentDir, label+".plist")
		_ = launchdBootout(ctx, plist)
		_ = os.Remove(plist)
	}
	return nil
}

func platformStatus(ctx context.Context, paths Paths) (Status, error) {
	status := Status{
		Platform:    platformName(),
		Service:     ServiceNotInstalled,
		UpdateTimer: "not installed",
	}

	agentPlist := filepath.Join(paths.Home, agentsDir, agentLabel+".plist")
	if _, err := os.Stat(agentPlist); err != nil {
		return status, nil
	}

	state, detail, err := launchdAgentState(ctx, agentLabel)
	if err != nil {
		return status, err
	}
	status.Service = state
	status.ServiceDetail = detail

	updatePlist := filepath.Join(paths.Home, agentsDir, updateLabel+".plist")
	if _, err := os.Stat(updatePlist); err == nil {
		status.UpdateTimer = "enabled"
		status.UpdateDetail = fmt.Sprintf("runs `valinor update` every %s", UpdateInterval)
	}
	return status, nil
}

func platformRestart(ctx context.Context, paths Paths) error {
	agentPlist := filepath.Join(paths.Home, agentsDir, agentLabel+".plist")
	if _, err := os.Stat(agentPlist); err != nil {
		return fmt.Errorf("service is not installed")
	}
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", filepath.Join(domain, agentLabel))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restart service: %w", err)
	}
	return nil
}

func platformRestartIfInstalled(ctx context.Context, paths Paths) error {
	agentPlist := filepath.Join(paths.Home, agentsDir, agentLabel+".plist")
	if _, err := os.Stat(agentPlist); err != nil {
		fmt.Println("service not installed; binary updated but service not restarted")
		return nil
	}
	state, _, err := launchdAgentState(ctx, agentLabel)
	if err != nil {
		return err
	}
	if state == ServiceNotInstalled {
		fmt.Println("service not loaded; binary updated but service not restarted")
		return nil
	}
	return platformRestart(ctx, paths)
}

func platformLogs(ctx context.Context, paths Paths, opts LogOptions) error {
	path := paths.AppLog
	if opts.Deploy {
		path = paths.DeployLog
	}
	return tailFile(path, opts)
}

func writeLaunchdPlists(paths Paths) error {
	agentDir := filepath.Join(paths.Home, agentsDir)
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}

	files := map[string]string{
		agentLabel + ".plist":  launchdAgentPlist(paths),
		updateLabel + ".plist": launchdUpdatePlist(paths),
	}
	for name, content := range files {
		path := filepath.Join(agentDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func launchdAgentPlist(paths Paths) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.valinor.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
		<string>--mode=%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s/.valinor/logs/valinor.stdout.log</string>
	<key>StandardErrorPath</key>
	<string>%s/.valinor/logs/valinor.stderr.log</string>
</dict>
</plist>
`, paths.Binary, DefaultMode, paths.Home, paths.Home)
}

func launchdUpdatePlist(paths Paths) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.valinor.update</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>update</string>
	</array>
	<key>StartInterval</key>
	<integer>1800</integer>
	<key>RunAtLoad</key>
	<false/>
</dict>
</plist>
`, paths.Binary)
}

func launchdBootstrap(ctx context.Context, paths Paths, label string) error {
	plist := filepath.Join(paths.Home, agentsDir, label+".plist")
	_ = launchdBootout(ctx, plist)

	domain, err := guiDomain()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, plist)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootstrap %s: %w", label, err)
	}
	return nil
}

func launchdBootout(ctx context.Context, plist string) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", domain, plist)
	_ = cmd.Run()
	return nil
}

func launchdAgentState(ctx context.Context, label string) (ServiceState, string, error) {
	domain, err := guiDomain()
	if err != nil {
		return ServiceNotInstalled, "", err
	}

	out, err := exec.CommandContext(ctx, "launchctl", "print", filepath.Join(domain, label)).CombinedOutput()
	if err != nil {
		return ServiceInactive, "not loaded", nil
	}

	text := string(out)
	if strings.Contains(text, "state = running") {
		return ServiceActive, "running", nil
	}
	if strings.Contains(text, "state = waiting") || strings.Contains(text, "state = sleeping") {
		return ServiceActive, strings.TrimSpace(lastLine(text)), nil
	}
	if strings.Contains(text, "state = exited") {
		return ServiceFailed, "exited", nil
	}
	return ServiceInactive, strings.TrimSpace(lastLine(text)), nil
}

func guiDomain() (string, error) {
	uid := os.Getuid()
	if uid < 0 {
		return "", fmt.Errorf("resolve user id")
	}
	return "gui/" + strconv.Itoa(uid), nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}