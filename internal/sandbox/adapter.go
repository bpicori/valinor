package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bpicori/red-keep/pkg/redkeep"

	"valinor/internal/applog"
)

// Result contains command execution outputs from red-keep.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func runWithRedKeep(ctx context.Context, req redkeep.RunRequest) (Result, error) {
	return runWithRedKeepIO(ctx, req, nil, nil)
}

func runWithRedKeepIO(ctx context.Context, req redkeep.RunRequest, stdin io.Reader, env []string) (Result, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	result, err := redkeep.Run(req, redkeep.RunIO{
		Context: ctx,
		Stdin:   stdin,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Env:     env,
	})
	if err != nil {
		cmd := strings.Join(req.Command, " ")
		applog.Error("sandbox redkeep failed", applog.M("command", cmd, "work_dir", req.WorkDir, "error", err))
		return Result{}, err
	}

	return Result{
		ExitCode: result.ExitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

// FormatCommandResult returns the textual tool result for command execution.
func FormatCommandResult(result Result) string {
	return fmt.Sprintf("exit_code: %d\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
}
