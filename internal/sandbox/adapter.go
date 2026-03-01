package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"valinor/internal/logger"
)

// Result contains command execution outputs.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// execInContainer executes a command inside a running Podman container.
// It runs 'podman exec' with appropriate flags for working directory, environment,
// and stdin handling.
func execInContainer(ctx context.Context, containerID string, command []string, workDir string, stdin io.Reader, env []string) (Result, error) {
	args := []string{"exec"}

	// Set working directory inside the container
	if workDir != "" {
		args = append(args, "-w", workDir)
	}

	// Add environment variables
	for _, e := range env {
		args = append(args, "-e", e)
	}

	// Enable interactive mode if we have stdin
	if stdin != nil {
		args = append(args, "-i")
	}

	// Specify the container and command
	args = append(args, containerID)
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdin = stdin

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err == nil {
		return result, nil
	}

	// Non-zero exit codes are not errors - they're represented in ExitCode
	if _, ok := err.(*exec.ExitError); ok {
		return result, nil
	}

	// Log actual execution errors
	cmdStr := strings.Join(command, " ")
	logger.Error("podman exec failed", logger.BuildMetadata("command", cmdStr, "container", containerID, "error", err))
	return result, fmt.Errorf("podman exec failed: %w", err)
}

// FormatCommandResult returns the textual tool result for command execution.
func FormatCommandResult(result Result) string {
	return fmt.Sprintf("exit_code: %d\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
}
