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

	"github.com/bpicori/red-keep/pkg/redkeep"

	"valinor/internal/logger"
)

// Runtime holds workspace and execution context for tool calls.
type Runtime struct {
	workspaceDir     string
	pythonExecutable string
	sem              chan struct{}
}

const (
	fsToolTimeout           = 30 * time.Second
	defaultMaxConcurrentOps = 10
)

// NewRuntime creates a runtime configured for a workspace root.
func NewRuntime(dir string) (*Runtime, error) {
	resolvedPython, err := resolvePythonExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve python executable: %w", err)
	}

	logger.Info("sandbox runtime initialized",
		logger.BuildMetadata("workspace", dir, "python_resolved", resolvedPython))

	return &Runtime{
		workspaceDir:     dir,
		pythonExecutable: resolvedPython,
		sem:              make(chan struct{}, defaultMaxConcurrentOps),
	}, nil
}

func resolvePythonExecutable() (string, error) {
	candidates := []string{"python3", "python"}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("python executable not found in PATH (tried: python3, python)")
}

// RunCommand executes a command in red-keep with workspace RW access.
func (r *Runtime) RunCommand(ctx context.Context, workDir string, command string, args []string) (Result, error) {
	return r.run(ctx, redkeep.RunRequest{
		RWPaths:  []string{r.workspaceDir},
		WorkDir:  workDir,
		AllowNet: true,
		Command:  append([]string{command}, args...),
	})
}

// ReadFile reads a file relative to workspace.
func (r *Runtime) ReadFile(path string) (string, error) {
	fullPath := r.resolveWorkspacePath(path)
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	result, err := r.run(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: r.workspaceDir,
		Command: []string{"cat", fullPath},
	})
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if result.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(result, "read file failed"), nil
	}
	return result.Stdout, nil
}

// WriteFile writes content to a file relative to workspace.
func (r *Runtime) WriteFile(path string, content string) (string, error) {
	relPath := filepath.FromSlash(path)
	parentDir := filepath.Dir(relPath)

	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	if parentDir != "." {
		mkdirResult, err := r.run(ctx, redkeep.RunRequest{
			RWPaths: []string{r.workspaceDir},
			WorkDir: r.workspaceDir,
			Command: []string{"mkdir", "-p", parentDir},
		})
		if err != nil {
			return "Tool Error: " + err.Error(), nil
		}
		if mkdirResult.ExitCode != 0 {
			return "Tool Error: " + commandFailureMessage(mkdirResult, "create parent directory failed"), nil
		}
	}

	writeResult, err := r.runIO(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: r.workspaceDir,
		Command: []string{"tee", relPath},
	}, strings.NewReader(content), nil)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if writeResult.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(writeResult, "write file failed"), nil
	}
	return "ok", nil
}

// ListDirectory lists entries in a directory relative to workspace.
func (r *Runtime) ListDirectory(path string) (string, error) {
	fullPath := r.resolveWorkspacePath(path)
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	result, err := r.run(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: r.workspaceDir,
		Command: []string{"ls", "-1Ap", fullPath},
	})
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if result.ExitCode != 0 {
		return "Tool Error: " + commandFailureMessage(result, "list directory failed"), nil
	}
	return result.Stdout, nil
}

// Grep searches for a regex pattern under a workspace-relative path.
func (r *Runtime) Grep(pattern string, path string, include string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

	args := []string{"-n", "--no-heading", "--max-count", "100"}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, pattern, path)

	result, err := r.run(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: r.workspaceDir,
		Command: append([]string{"rg"}, args...),
	})
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

// RunPython executes Python code from the host runtime.
// mode can be "inline" (code string) or "script" (path to file).
// For inline mode, code is the Python code to execute.
// For script mode, code is the path to the script file.
// args are additional arguments (only used in script mode).
func (r *Runtime) RunPython(ctx context.Context, workDir string, mode string, code string, args []string, timeout time.Duration) (Result, error) {
	// Resolve working directory
	targetWorkDir := r.workspaceDir
	if workDir != "" && workDir != "." {
		targetWorkDir = filepath.Join(r.workspaceDir, filepath.FromSlash(workDir))
	}

	var command []string
	var stdin io.Reader

	switch mode {
	case "inline":
		// Execute code passed as stdin using -c
		command = []string{r.pythonExecutable, "-c", code}
	case "script":
		// Execute script file
		scriptPath := r.resolveWorkspacePath(code)
		command = append([]string{r.pythonExecutable, scriptPath}, args...)
	default:
		return Result{}, fmt.Errorf("invalid mode: %s (must be 'inline' or 'script')", mode)
	}

	return r.runHostCommandWithTimeout(ctx, command, targetWorkDir, stdin, nil, timeout)
}

// WorkspaceDir returns the runtime workspace root.
func (r *Runtime) WorkspaceDir() string {
	return r.workspaceDir
}

// Close releases runtime resources. Currently no-op.
func (r *Runtime) Close() error {
	return nil
}

func (r *Runtime) run(ctx context.Context, req redkeep.RunRequest) (Result, error) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()
	return runWithRedKeep(ctx, req)
}

func (r *Runtime) runIO(ctx context.Context, req redkeep.RunRequest, stdin io.Reader, env []string) (Result, error) {
	r.sem <- struct{}{}
	defer func() { <-r.sem }()
	return runWithRedKeepIO(ctx, req, stdin, env)
}

func (r *Runtime) runHostCommandWithTimeout(ctx context.Context, command []string, workDir string, stdin io.Reader, env []string, timeout time.Duration) (Result, error) {
	if len(command) == 0 {
		return Result{}, fmt.Errorf("empty command")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workDir
	cmd.Stdin = stdin
	if len(env) > 0 {
		cmd.Env = env
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	if err == nil {
		return result, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	if _, ok := err.(*exec.ExitError); ok {
		// Non-zero exit is represented in ExitCode and stderr/stdout.
		return result, nil
	}
	return result, err
}

func (r *Runtime) resolveWorkspacePath(path string) string {
	return filepath.Join(r.workspaceDir, filepath.FromSlash(path))
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
