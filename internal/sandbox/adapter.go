package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/bpicori/red-keep/pkg/redkeep"
)

// Result contains command execution outputs from red-keep.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func runWithRedKeep(ctx context.Context, policy redKeepPolicy, req redkeep.RunRequest) (Result, error) {
	return runWithRedKeepIO(ctx, policy, req, nil, nil)
}

func runWithRedKeepIO(ctx context.Context, policy redKeepPolicy, req redkeep.RunRequest, stdin io.Reader, env []string) (Result, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	result, err := redkeep.Run(req, redkeep.RunIO{
		Context:          ctx,
		Stdin:            stdin,
		Stdout:           &stdout,
		Stderr:           &stderr,
		Env:              env,
		HelperBinaryPath: policy.BinaryPath,
	})
	if err != nil {
		return Result{}, err
	}

	return Result{
		ExitCode: result.ExitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}

func withPolicyCapabilities(policy redKeepPolicy, req redkeep.RunRequest) redkeep.RunRequest {
	req.AllowNet = policy.AllowNet
	req.AllowDomains = append([]string{}, policy.AllowDomains...)
	req.DenyDomains = append([]string{}, policy.DenyDomains...)
	req.AllowExec = policy.AllowExec
	req.AllowPTY = policy.AllowPTY
	return req
}

// FormatCommandResult returns the textual tool result for command execution.
func FormatCommandResult(result Result) string {
	return fmt.Sprintf("exit_code: %d\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
}
