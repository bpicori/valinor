package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bpicori/red-keep/pkg/redkeep"
)

// Runtime holds workspace and execution context for tool calls.
type Runtime struct {
	workspaceDir   string
	executablePath string
	mu             sync.Mutex
}

const fsToolTimeout = 30 * time.Second

// NewRuntime creates a runtime configured for a workspace root.
func NewRuntime(dir string) (*Runtime, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	return &Runtime{
		workspaceDir:   dir,
		executablePath: execPath,
	}, nil
}

// RunCommand executes a command in red-keep with workspace RW access.
func (r *Runtime) RunCommand(ctx context.Context, workDir string, command string, args []string) (Result, error) {
	return r.run(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: workDir,
		Command: append([]string{command}, args...),
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
	fullPath := r.resolveWorkspacePath(path)
	parentDir := filepath.Dir(fullPath)

	ctx, cancel := context.WithTimeout(context.Background(), fsToolTimeout)
	defer cancel()

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

	writeResult, err := r.runIO(ctx, redkeep.RunRequest{
		RWPaths: []string{r.workspaceDir},
		WorkDir: r.workspaceDir,
		Command: []string{"tee", fullPath},
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

// WorkspaceDir returns the runtime workspace root.
func (r *Runtime) WorkspaceDir() string {
	return r.workspaceDir
}

// Close releases runtime resources. Currently no-op.
func (r *Runtime) Close() error {
	return nil
}

func (r *Runtime) run(ctx context.Context, req redkeep.RunRequest) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runWithRedKeep(ctx, req)
}

func (r *Runtime) runIO(ctx context.Context, req redkeep.RunRequest, stdin io.Reader, env []string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runWithRedKeepIO(ctx, req, stdin, env)
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
