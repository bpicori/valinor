package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/logger"
)

const (
	fsToolTimeout           = 30 * time.Second
	defaultMaxConcurrentOps = 10
)

// Runtime holds workspace and execution context for tool calls.
// All commands execute inside a single long-running Podman container.
type Runtime struct {
	workspaceDir string
	containerID  string
	image        string
	sem          chan struct{}
}

// NewRuntime creates a runtime configured for a workspace root.
// It starts a long-running Podman container with the workspace bind-mounted.
// The image parameter specifies the container image to use (e.g., "valinor-sandbox:latest").
func NewRuntime(dir, image string) (*Runtime, error) {
	logger.Debug("sandbox runtime creation starting", logger.BuildMetadata("workspace", dir, "image", image))

	// Check 1: Verify Podman is installed
	if err := checkPodmanInstalled(); err != nil {
		return nil, err
	}

	// Check 2: Verify the sandbox image exists
	if err := ensureSandboxImage(image); err != nil {
		return nil, err
	}

	// Resolve absolute path for the workspace
	absDir, err := filepath.Abs(dir)
	if err != nil {
		logger.Error("sandbox runtime failed to resolve workspace path", logger.BuildMetadata("workspace", dir, "error", err))
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	logger.Debug("sandbox runtime workspace resolved", logger.BuildMetadata("workspace", dir, "abs_dir", absDir))

	// Start a container that stays running (for exec operations)
	// Using :Z suffix for SELinux compatibility, --rm for auto-cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger.Debug("sandbox runtime starting container", logger.BuildMetadata("image", image, "abs_dir", absDir))

	cmd := exec.CommandContext(ctx, "podman", "run", "-d", "--rm",
		"-v", absDir+":/workspace:Z",
		"-w", "/workspace",
		image,
		"sleep", "infinity")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if err != nil {
		stderrStr := stderr.String()
		logger.Error("sandbox runtime failed to start container",
			logger.BuildMetadata("image", image, "error", err, "stderr", stderrStr))
		return nil, fmt.Errorf("start sandbox container: %w (stderr: %s)", err, stderrStr)
	}

	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		logger.Error("sandbox runtime failed to get container ID", logger.BuildMetadata("output", string(output)))
		return nil, fmt.Errorf("failed to get container ID from podman run")
	}

	logger.Info("sandbox runtime initialized",
		logger.BuildMetadata("workspace", dir, "container_id", containerID[:12], "image", image))

	return &Runtime{
		workspaceDir: absDir,
		containerID:  containerID,
		image:        image,
		sem:          make(chan struct{}, defaultMaxConcurrentOps),
	}, nil
}

// checkPodmanInstalled verifies that podman is installed and accessible.
func checkPodmanInstalled() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "podman", "version")
	if err := cmd.Run(); err != nil {
		logger.Error("podman is not installed or not accessible", logger.BuildMetadata("error", err))
		return fmt.Errorf("podman is not installed or not accessible in PATH: %w\n\nTo install Podman:\n  - macOS: brew install podman && podman machine init && podman machine start\n  - Linux: sudo apt install podman  (or your distro's package manager)\n  - See: https://podman.io/docs/installation", err)
	}
	logger.Debug("podman is installed and accessible", nil)
	return nil
}

// ensureSandboxImage checks if the sandbox image exists locally.
// For now, we require the image to be built locally. In the future, this could pull from a registry.
func ensureSandboxImage(image string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check if image exists locally
	cmd := exec.CommandContext(ctx, "podman", "image", "exists", image)
	if err := cmd.Run(); err == nil {
		logger.Debug("sandbox image exists locally", logger.BuildMetadata("image", image))
		return nil
	}

	// Image doesn't exist - provide clear instructions
	logger.Error("sandbox image not found", logger.BuildMetadata("image", image))
	return fmt.Errorf("sandbox image '%s' not found locally\n\nTo build the image, run:\n  podman build -t %s -f internal/sandbox/Containerfile .\n\nOr if using make:\n  make build-sandbox", image, image)
}

// RunCommand executes a shell command inside the container.
func (r *Runtime) RunCommand(ctx context.Context, workDir string, shellCommand string, timeout time.Duration) (Result, error) {
	command := []string{"bash", "-c", shellCommand}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	targetWorkDir := "/workspace"
	if workDir != "" && workDir != "." {
		targetWorkDir = filepath.Join("/workspace", filepath.FromSlash(workDir))
	}

	return r.exec(ctx, command, targetWorkDir, nil, nil)
}

// RunCommandOnHost is an alias for RunCommand in the container sandbox.
// All commands run inside the container - there is no host execution.
func (r *Runtime) RunCommandOnHost(ctx context.Context, workDir string, shellCommand string, timeout time.Duration) (Result, error) {
	return r.RunCommand(ctx, workDir, shellCommand, timeout)
}

// ReadFile reads a file relative to workspace inside the container.
func (r *Runtime) ReadFile(path string) (string, error) {
	fullPath := r.resolveContainerPath(path)
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	result, err := r.exec(ctx, []string{"cat", fullPath}, "/workspace", nil, nil)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if result.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(result, "read file failed"), nil
	}
	return result.Stdout, nil
}

// WriteFile writes content to a file relative to workspace inside the container.
func (r *Runtime) WriteFile(path string, content string) (string, error) {
	relPath := filepath.FromSlash(path)
	parentDir := filepath.Dir(relPath)

	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	// Create parent directory if needed
	if parentDir != "." {
		mkdirResult, err := r.exec(ctx, []string{"mkdir", "-p", parentDir}, "/workspace", nil, nil)
		if err != nil {
			return "Tool Error: " + err.Error(), nil
		}
		if mkdirResult.ExitCode != 0 {
			return "Tool Error: " + commandFailureMessage(mkdirResult, "create parent directory failed"), nil
		}
	}

	// Write file using tee
	writeResult, err := r.execWithIO(ctx, []string{"tee", relPath}, "/workspace", strings.NewReader(content), nil)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if writeResult.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(writeResult, "write file failed"), nil
	}
	return "ok", nil
}

// ListDirectory lists entries in a directory relative to workspace inside the container.
func (r *Runtime) ListDirectory(path string) (string, error) {
	fullPath := r.resolveContainerPath(path)
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	result, err := r.exec(ctx, []string{"ls", "-1Ap", fullPath}, "/workspace", nil, nil)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if result.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(result, "list directory failed"), nil
	}
	return result.Stdout, nil
}

// Grep searches for a regex pattern under a workspace-relative path inside the container.
func (r *Runtime) Grep(pattern string, path string, include string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	args := []string{"-n", "--no-heading", "--max-count", "100"}
	if include != "" {
		args = append(args, "--glob", include)
	}

	fullPath := r.resolveContainerPath(path)
	args = append(args, pattern, fullPath)

	result, err := r.exec(ctx, append([]string{"rg"}, args...), "/workspace", nil, nil)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if result.ExitCode == 0 {
		return result.Stdout, nil
	}
	if result.ExitCode == 1 && strings.TrimSpace(result.Stderr) == "" {
		return "No matches found.", nil
	}
	return "Tool Error: " + commandFailureMessage(result, "grep failed"), nil
}

// RunPython executes Python code inside the container.
// mode can be "inline" (code string) or "script" (path to file).
// For inline mode, code is the Python code to execute.
// For script mode, code is the path to the script file.
// args are additional arguments (only used in script mode).
func (r *Runtime) RunPython(ctx context.Context, workDir string, mode string, code string, args []string, timeout time.Duration) (Result, error) {
	targetWorkDir := "/workspace"
	if workDir != "" && workDir != "." {
		targetWorkDir = filepath.Join("/workspace", filepath.FromSlash(workDir))
	}

	var command []string
	var stdin io.Reader

	switch mode {
	case "inline":
		// Execute code passed as stdin using -c
		command = []string{"python3", "-c", code}
	case "script":
		// Execute script file (path is already relative to workspace)
		containerPath := filepath.Join("/workspace", filepath.FromSlash(code))
		command = append([]string{"python3", containerPath}, args...)
	default:
		return Result{}, fmt.Errorf("invalid mode: %s (must be 'inline' or 'script')", mode)
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	return r.execWithIO(ctx, command, targetWorkDir, stdin, nil)
}

// WorkspaceDir returns the runtime workspace root.
func (r *Runtime) WorkspaceDir() string {
	return r.workspaceDir
}

// Close releases runtime resources by removing the container.
func (r *Runtime) Close() error {
	if r.containerID == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "podman", "rm", "-f", r.containerID)
	if err := cmd.Run(); err != nil {
		logger.Error("failed to remove sandbox container", logger.BuildMetadata("container_id", r.containerID, "error", err))
		return fmt.Errorf("remove sandbox container: %w", err)
	}

	logger.Info("sandbox runtime closed", logger.BuildMetadata("container_id", r.containerID[:12]))
	return nil
}

// exec runs a command in the container with concurrency limiting.
func (r *Runtime) exec(ctx context.Context, command []string, workDir string, stdin io.Reader, env []string) (Result, error) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()
	return execInContainer(ctx, r.containerID, command, workDir, stdin, env)
}

// execWithIO runs a command with stdin/env in the container with concurrency limiting.
func (r *Runtime) execWithIO(ctx context.Context, command []string, workDir string, stdin io.Reader, env []string) (Result, error) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()
	return execInContainer(ctx, r.containerID, command, workDir, stdin, env)
}

// resolveContainerPath converts a workspace-relative path to a container path.
func (r *Runtime) resolveContainerPath(path string) string {
	return filepath.Join("/workspace", filepath.FromSlash(path))
}

func commandFailureMessage(result Result, fallback string) string {
	msg := strings.TrimSpace(result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(result.Stdout)
	}
	if msg == "" {
		msg = fallback
	}
	return msg
}
