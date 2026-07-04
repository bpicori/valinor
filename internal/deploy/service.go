package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ServiceState describes the installed service state.
type ServiceState string

const (
	ServiceNotInstalled ServiceState = "not installed"
	ServiceActive       ServiceState = "active"
	ServiceInactive     ServiceState = "inactive"
	ServiceFailed       ServiceState = "failed"
)

// Status summarizes deployment state for operator commands.
type Status struct {
	Platform      string
	Service       ServiceState
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

// Install performs a full deployment install as gateway + Telegram (run --mode=all).
func Install(ctx context.Context) error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}

	if err := preflightInstall(paths); err != nil {
		return err
	}
	if err := ensureDeployDirs(paths); err != nil {
		return err
	}

	_, _, err = updateRelease(ctx, updateOptions{
		paths:       paths,
		repo:        DefaultRepo,
		skipRestart: true,
	})
	if err != nil {
		return err
	}

	if err := platformInstall(ctx, paths); err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("Install complete.")
	fmt.Println("")
	fmt.Println("Useful commands:")
	fmt.Println("  valinor status")
	fmt.Println("  valinor logs -f")
	fmt.Println("  valinor update")
	fmt.Println("  valinor version")
	return nil
}

// PrintStatus writes deployment status to stdout.
func PrintStatus(ctx context.Context) error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}

	status, err := platformStatus(ctx, paths)
	if err != nil {
		return err
	}

	installed, err := BinaryVersion(paths.Binary)
	if err != nil {
		return err
	}

	latest := ""
	if tag, _, _, err := LatestRelease(ctx, DefaultRepo); err == nil {
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
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}

	_, _, err = updateRelease(ctx, updateOptions{
		paths: paths,
		repo:  DefaultRepo,
		restart: func(ctx context.Context) error {
			return platformRestartIfInstalled(ctx, paths)
		},
	})
	return err
}

// RunRollback restores the previous binary.
func RunRollback(ctx context.Context) error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}

	return rollbackRelease(ctx, paths, func(ctx context.Context) error {
		return platformRestartIfInstalled(ctx, paths)
	})
}

// RunRestart restarts the installed service.
func RunRestart(ctx context.Context) error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	return platformRestart(ctx, paths)
}

// RunUninstall removes the installed service.
func RunUninstall(ctx context.Context) error {
	paths, err := DefaultPaths()
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
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	return platformLogs(ctx, paths, opts)
}

func preflightInstall(paths Paths) error {
	if _, err := os.Stat(paths.ConfigFile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config not found at %s; run `valinor init` first", paths.ConfigFile)
		}
		return fmt.Errorf("stat config file: %w", err)
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("missing required command: podman")
	}
	return nil
}

func ensureDeployDirs(paths Paths) error {
	dirs := []string{
		filepath.Dir(paths.Binary),
		filepath.Dir(paths.AppLog),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
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