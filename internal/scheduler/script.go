package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// runScript executes a script from scriptsDir with the given timeout.
func runScript(ctx context.Context, scriptsDir, scriptName string, timeout time.Duration) (string, error) {
	if scriptName == "" {
		return "", fmt.Errorf("script name is required")
	}
	if strings.Contains(scriptName, "..") || filepath.IsAbs(scriptName) {
		return "", fmt.Errorf("invalid script name")
	}

	scriptPath := filepath.Join(scriptsDir, scriptName)
	if _, err := os.Stat(scriptPath); err != nil {
		return "", fmt.Errorf("script not found: %s", scriptName)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if strings.HasSuffix(strings.ToLower(scriptName), ".sh") {
		cmd = exec.CommandContext(runCtx, "bash", scriptPath)
	} else {
		cmd = exec.CommandContext(runCtx, scriptPath)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("script exited with error: %s", errMsg)
	}

	return strings.TrimSpace(stdout.String()), nil
}