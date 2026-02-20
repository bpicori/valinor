package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"valinor/internal/sandbox"
)

const (
	runCommandName      = "run_command"
	defaultTimeoutMs    = 30000
	maxCommandTimeoutMs = 300000
)

func runCommandDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name: runCommandName,
		Description: openai.String(
			"Run a shell command in a sandbox limited to the workspace.",
		),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]string{
					"type":        "string",
					"description": "Command binary to execute (e.g. ls, go, python)",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command arguments",
					"items": map[string]string{
						"type": "string",
					},
				},
				"cwd": map[string]string{
					"type":        "string",
					"description": "Relative working directory within workspace (default \".\")",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"description": "Execution timeout in milliseconds (1-300000, default 30000)",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	})
}

func runCommandExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Cwd       string   `json:"cwd"`
		TimeoutMs int      `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	command := strings.TrimSpace(p.Command)
	if command == "" {
		return "Tool Error: command is required.", nil
	}
	if strings.Contains(command, "/") {
		return "Tool Error: command must be a binary name, not a path.", nil
	}

	cwd := normalizePath(p.Cwd)
	if cwd == "" {
		cwd = "."
	}
	if cwd != "." && (strings.HasPrefix(cwd, "..") || strings.Contains(cwd, "/..")) {
		return "Tool Error: cwd must not escape workspace (\"..\" not allowed).", nil
	}
	if filepath.IsAbs(cwd) {
		return "Tool Error: cwd must be relative to workspace (absolute paths not allowed).", nil
	}

	timeoutMs := p.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = defaultTimeoutMs
	}
	if timeoutMs < 1 || timeoutMs > maxCommandTimeoutMs {
		return "Tool Error: timeout_ms must be between 1 and 300000.", nil
	}

	workDir := rt.WorkspaceDir()
	if cwd != "." {
		workDir = filepath.Join(rt.WorkspaceDir(), cwd)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	result, err := rt.RunCommand(ctx, workDir, command, p.Args)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	return sandbox.FormatCommandResult(result), nil
}
