package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/sandbox"
)

// runScript executes a workspace script inside the sandbox container.
// Scripts must live under the workspace so the container can see them;
// there is no host execution path.
func runScript(ctx context.Context, workspaceDir, scriptsDir, scriptName string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(scriptName) == "" {
		return "", fmt.Errorf("script name is required")
	}
	if !strings.HasSuffix(strings.ToLower(scriptName), ".sh") {
		return "", fmt.Errorf("scripts must end with .sh")
	}

	rel, err := filepath.Rel(workspaceDir, filepath.Join(scriptsDir, scriptName))
	if err != nil || strings.HasPrefix(filepath.ToSlash(rel), "..") {
		return "", fmt.Errorf("scripts_dir must be inside the workspace to run in the sandbox")
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := sandbox.MustRuntime().RunCommand(runCtx, ".", fmt.Sprintf("bash %q", filepath.ToSlash(rel)), timeout)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		errMsg := strings.TrimSpace(result.Stderr)
		if errMsg == "" {
			errMsg = strings.TrimSpace(result.Stdout)
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return "", fmt.Errorf("script exited with error: %s", errMsg)
	}
	return strings.TrimSpace(result.Stdout), nil
}
