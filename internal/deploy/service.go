package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Status summarizes deployment state for operator commands.
type Status struct {
	Platform      string
	Service       string
	ServiceDetail string
	UpdateTimer   string
	UpdateDetail  string
}

// LogOptions configures log streaming.
type LogOptions struct {
	Follow bool
	Lines  int
	Deploy bool
}

// PrintStatus writes deployment status to stdout.
func PrintStatus(ctx context.Context) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}

	status, err := platformStatus(ctx, paths)
	if err != nil {
		return err
	}

	installed, err := binaryVersion(paths.Binary)
	if err != nil {
		return err
	}

	latest := ""
	if tag, _, _, err := latestRelease(ctx, DefaultRepo); err == nil {
		latest = tag
	}

	fmt.Printf("Platform:   %s\n", status.Platform)
	fmt.Printf("Service:    %s\n", status.Service)
	if status.ServiceDetail != "" {
		fmt.Printf("              %s\n", status.ServiceDetail)
	}

	if installed != "" {
		fmt.Printf("Version:    %s (installed)", installed)
	} else {
		fmt.Printf("Version:    <none> (installed)")
	}
	if latest != "" {
		if installed == latest {
			fmt.Printf(", latest available\n")
		} else {
			fmt.Printf(", %s (latest available)\n", latest)
		}
	} else {
		fmt.Println()
	}

	fmt.Printf("Binary:     %s\n", paths.Binary)
	fmt.Printf("Config:     %s\n", paths.ConfigFile)
	fmt.Printf("App log:    %s\n", paths.AppLog)
	fmt.Printf("Deploy log: %s\n", paths.DeployLog)
	fmt.Printf("Update:     %s\n", status.UpdateTimer)
	if status.UpdateDetail != "" {
		fmt.Printf("              %s\n", status.UpdateDetail)
	}
	return nil
}

// RunUpdate performs a release update and restarts the service when installed.
func RunUpdate(ctx context.Context) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}

	return updateRelease(ctx, paths, true)
}

// RunRollback restores the previous binary.
func RunRollback(ctx context.Context) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}

	return rollbackRelease(ctx, paths, true)
}

// RunRestart restarts the installed service.
func RunRestart(ctx context.Context) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	return platformRestart(ctx, paths)
}

// RunUninstall removes the installed service.
func RunUninstall(ctx context.Context) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	if err := platformUninstall(ctx, paths); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Service removed. Configuration and data in ~/.valinor were kept.")
	return nil
}

// RunLogs streams deployment or service logs.
func RunLogs(ctx context.Context, opts LogOptions) error {
	paths, err := defaultPaths()
	if err != nil {
		return err
	}
	return platformLogs(ctx, paths, opts)
}

func tailFile(path string, opts LogOptions) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("log file not found: %s", path)
	}
	args := []string{}
	if opts.Lines > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", opts.Lines))
	}
	if opts.Follow {
		args = append(args, "-f")
	}
	args = append(args, path)
	cmd := exec.Command("tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}