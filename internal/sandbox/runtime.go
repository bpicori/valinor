package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valinor/internal/buildinfo"
	"valinor/internal/logger"
)

const (
	fsToolTimeout           = 30 * time.Second
	defaultMaxConcurrentOps = 10
	podmanInstallURL        = "https://podman.io/docs/installation"
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
func NewRuntime(dir string) (*Runtime, error) {
	image := buildinfo.SandboxImage()
	logger.Debug("sandbox runtime creation starting", logger.BuildMetadata("workspace", dir, "image", image))

	// Check 1: Verify Podman is installed
	if err := checkPodmanInstalled(); err != nil {
		return nil, err
	}

	// Check 2: Build the sandbox image when missing or outdated for this Valinor version.
	if err := ensureSandboxImageReady(image); err != nil {
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

	runArgs := []string{"run", "-d", "--rm",
		"-v", absDir + ":/workspace:Z",
		"-w", "/workspace",
		image,
	}

	cmd := exec.CommandContext(ctx, "podman", runArgs...)

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

	rt := &Runtime{
		workspaceDir: absDir,
		containerID:  containerID,
		image:        image,
		sem:          make(chan struct{}, defaultMaxConcurrentOps),
	}

	logger.Info("sandbox runtime initialized",
		logger.BuildMetadata("workspace", dir, "container_id", containerID[:12], "image", image))

	return rt, nil
}

// checkPodmanInstalled verifies that podman is installed and accessible.
func checkPodmanInstalled() error {
	if _, err := exec.LookPath("podman"); err != nil {
		logger.Error("podman is not installed", logger.BuildMetadata("error", err))
		return fmt.Errorf("podman is not installed or not found in PATH.\n\nInstall Podman first: %s", podmanInstallURL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "podman", "version")
	if err := cmd.Run(); err != nil {
		logger.Error("podman is installed but not accessible", logger.BuildMetadata("error", err))
		return fmt.Errorf("podman is installed but not accessible: %w\n\nIf you're on macOS, make sure the Podman machine is initialized and running:\n  podman machine init\n  podman machine start\n\nInstallation and setup instructions: %s", err, podmanInstallURL)
	}
	logger.Debug("podman is installed and accessible", nil)
	return nil
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

// FileSlice is a ranged read of a workspace file inside the container.
type FileSlice struct {
	Content    string
	StartLine  int
	EndLine    int
	TotalLines int
	Truncated  bool // content was cut by the byte cap
}

// ReadRangeMaxLines and ReadRangeMaxBytes bound one ranged read.
// The byte cap stays below the tool-result cap so the continuation hint remains accurate.
const (
	ReadRangeMaxLines = 2000
	ReadRangeMaxBytes = 20_000
)

// ReadFileRange reads a 1-based line range from a workspace file inside the container.
// It reports the file's total line count so callers can paginate.
func (r *Runtime) ReadFileRange(path string, offset, limit int) (FileSlice, error) {
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = ReadRangeMaxLines
	}
	if limit > ReadRangeMaxLines {
		limit = ReadRangeMaxLines
	}
	end := offset + limit - 1

	fullPath := r.resolveContainerPath(path)
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	const totalMarker = "__VALINOR_TOTAL_LINES__="
	program := fmt.Sprintf(
		`NR>=%d && NR<=%d { print } END { printf "\n%s%%d\n", NR }`,
		offset, end, totalMarker,
	)

	// argv form: podman exec ... awk <program> <path> (no shell quoting needed).
	result, err := r.exec(ctx, []string{"awk", program, fullPath}, "/workspace", nil, nil)
	if err != nil {
		return FileSlice{}, err
	}
	if result.ExitCode != 0 {
		return FileSlice{}, fmt.Errorf("%s", commandFailureMessage(result, "read file failed"))
	}

	content := result.Stdout
	total := 0
	if idx := strings.LastIndex(content, totalMarker); idx >= 0 {
		if n, convErr := strconv.Atoi(strings.TrimSpace(content[idx+len(totalMarker):])); convErr == nil {
			total = n
		}
		content = strings.TrimSuffix(content[:idx], "\n")
	}

	truncated := false
	if len(content) > ReadRangeMaxBytes {
		cut := ReadRangeMaxBytes
		if idx := strings.LastIndex(content[:cut], "\n"); idx > cut/2 {
			cut = idx
		}
		content = content[:cut]
		truncated = true
	}

	endLine := end
	if truncated {
		if lines := strings.Count(content, "\n"); lines > 0 {
			endLine = offset + lines - 1
		} else {
			endLine = offset
		}
	}
	if total > 0 && endLine > total {
		endLine = total
	}

	return FileSlice{
		Content:    content,
		StartLine:  offset,
		EndLine:    endLine,
		TotalLines: total,
		Truncated:  truncated,
	}, nil
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
