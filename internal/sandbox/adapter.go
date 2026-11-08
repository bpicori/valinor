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

const (
	// maxExecCaptureBytes bounds one captured stream. Tool results are capped
	// again at 24k characters; this limit exists to keep a runaway command from
	// exhausting host memory before that cap is applied.
	maxExecCaptureBytes = 1 << 20
)

// captureBuffer is satisfied by bytes.Buffer and boundedBuffer.
type captureBuffer interface {
	io.Writer
	String() string
}

// boundedBuffer keeps the head and tail of a stream and drops the middle.
// Write always reports the full length so the child process never blocks on a
// full pipe, and memory use stays within the configured limit.
type boundedBuffer struct {
	head, tail           []byte
	headLimit, tailLimit int
	total                int
	truncated            bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	if limit < 2 {
		limit = 2
	}
	headLimit := limit / 2
	return &boundedBuffer{
		head:      make([]byte, 0, headLimit),
		tail:      make([]byte, 0, limit-headLimit),
		headLimit: headLimit,
		tailLimit: limit - headLimit,
	}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	b.total += written

	if len(b.head) < b.headLimit {
		n := b.headLimit - len(b.head)
		if n > len(p) {
			n = len(p)
		}
		b.head = append(b.head, p[:n]...)
		p = p[n:]
	}

	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.tailLimit {
			b.truncated = true
			b.tail = b.tail[len(b.tail)-b.tailLimit:]
		}
	}

	return written, nil
}

func (b *boundedBuffer) String() string {
	if !b.truncated {
		return string(b.head) + string(b.tail)
	}

	dropped := b.total - len(b.head) - len(b.tail)
	return string(b.head) + fmt.Sprintf(
		"\n\n[... output truncated after %d MiB; %d bytes dropped. Redirect output to a workspace file and read it with offset/limit.]\n\n",
		(b.headLimit+b.tailLimit)/(1<<20), dropped,
	) + string(b.tail)
}

// execInContainer executes a command inside a running Podman container.
// It runs 'podman exec' with appropriate flags for working directory, environment,
// and stdin handling. maxCapture=0 captures output without a limit.
func execInContainer(ctx context.Context, containerID string, command []string, workDir string, stdin io.Reader, env []string, maxCapture int) (Result, error) {
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

	var stdout, stderr captureBuffer
	if maxCapture > 0 {
		stdout = newBoundedBuffer(maxCapture)
		stderr = newBoundedBuffer(maxCapture)
	} else {
		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

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
