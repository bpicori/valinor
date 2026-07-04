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
		Service:     "not installed",
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
	if state == "not installed" {
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

func launchdBootout(ctx context.Context, plist string) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", domain, plist)
	_ = cmd.Run()
	return nil
}

func launchdAgentState(ctx context.Context, label string) (string, string, error) {
	domain, err := guiDomain()
	if err != nil {
		return "not installed", "", err
	}

	out, err := exec.CommandContext(ctx, "launchctl", "print", filepath.Join(domain, label)).CombinedOutput()
	if err != nil {
		return "inactive", "not loaded", nil
	}

	text := string(out)
	if strings.Contains(text, "state = running") {
		return "active", "running", nil
	}
	if strings.Contains(text, "state = waiting") || strings.Contains(text, "state = sleeping") {
		return "active", strings.TrimSpace(lastLine(text)), nil
	}
	if strings.Contains(text, "state = exited") {
		return "failed", "exited", nil
	}
	return "inactive", strings.TrimSpace(lastLine(text)), nil
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