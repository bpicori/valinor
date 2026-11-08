package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"valinor/internal/config"
)

// Status summarizes deployment state for operator commands.
type Status struct {
	Platform      string
	Service       string
	ServiceDetail string
}

// LogOptions configures log streaming.
type LogOptions struct {
	Follow bool
	Lines  int
}

// PrintStatus writes deployment status to stdout. An empty instance prints all.
func PrintStatus(ctx context.Context, instance string) error {
	if instance == "" {
		instances, err := config.ListInstances()
		if err != nil {
			return err
		}
		if len(instances) == 0 {
			fmt.Println("No instances configured.")
			return nil
		}
		latest := latestVersion(ctx)
		for idx, item := range instances {
			paths, err := pathsForInstance(item.Name)
			if err != nil {
				return err
			}
			if idx > 0 {
				fmt.Println()
			}
			if err := printStatus(ctx, paths, latest); err != nil {
				return err
			}
		}
		return nil
	}

	paths, err := pathsForInstance(instance)
	if err != nil {
		return err
	}
	return printStatus(ctx, paths, latestVersion(ctx))
}

func printStatus(ctx context.Context, paths Paths, latest string) error {
	status, err := platformStatus(ctx, paths)
	if err != nil {
		return err
	}
	installed, err := binaryVersion(paths.Binary)
	if err != nil {
		return err
	}

	fmt.Printf("Instance:   %s\n", paths.Instance)
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
	fmt.Printf("Data:       %s\n", paths.DataDir)
	fmt.Printf("App log:    %s\n", paths.AppLog)
	return nil
}

func latestVersion(ctx context.Context) string {
	tag, _, _, err := latestRelease(ctx, DefaultRepo)
	if err != nil {
		return ""
	}
	return tag
}

// ServiceState returns the systemd/launchd state for one instance.
func ServiceState(ctx context.Context, instance string) (string, error) {
	paths, err := pathsForInstance(instance)
	if err != nil {
		return "unknown", err
	}
	status, err := platformStatus(ctx, paths)
	if err != nil {
		return "unknown", err
	}
	return status.Service, nil
}

// RunInstall installs the template service and enables one instance.
func RunInstall(ctx context.Context, instance string) error {
	paths, err := pathsForInstance(instance)
	if err != nil {
		return err
	}
	if err := platformInstall(ctx, paths); err != nil {
		return err
	}
	fmt.Printf("Service %s installed and enabled.\n", paths.ServiceUnit)
	return nil
}

// RunUpdate performs a release update and restarts the service(s).
func RunUpdate(ctx context.Context, instance string) error {
	if instance != "" {
		paths, err := pathsForInstance(instance)
		if err != nil {
			return err
		}
		return updateRelease(ctx, paths, true)
	}

	paths, err := pathsForInstance(config.DefaultInstanceName)
	if err != nil {
		return err
	}
	if err := updateRelease(ctx, paths, false); err != nil {
		return err
	}
	return platformRestartAll(ctx)
}

// RunRollback restores the previous binary and restarts the service(s).
func RunRollback(ctx context.Context, instance string) error {
	if instance != "" {
		paths, err := pathsForInstance(instance)
		if err != nil {
			return err
		}
		return rollbackRelease(ctx, paths, true)
	}

	paths, err := pathsForInstance(config.DefaultInstanceName)
	if err != nil {
		return err
	}
	if err := rollbackRelease(ctx, paths, false); err != nil {
		return err
	}
	return platformRestartAll(ctx)
}

// RunRestart restarts the installed service.
func RunRestart(ctx context.Context, instance string) error {
	paths, err := pathsForInstance(instance)
	if err != nil {
		return err
	}
	return platformRestart(ctx, paths)
}

// RunUninstall removes the installed service.
func RunUninstall(ctx context.Context, instance string) error {
	paths, err := pathsForInstance(instance)
	if err != nil {
		return err
	}
	if err := platformUninstall(ctx, paths); err != nil {
		return err
	}
	fmt.Printf("Service %s removed. Configuration and data in %s were kept.\n", paths.ServiceUnit, paths.DataDir)
	return nil
}

// RunLogs streams service logs.
func RunLogs(ctx context.Context, opts LogOptions, instance string) error {
	paths, err := pathsForInstance(instance)
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
