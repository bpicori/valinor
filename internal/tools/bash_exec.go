package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/sandbox"
)

const (
	bashExecName       = "bash_exec"
	bashDefaultTimeout = 30000
	bashMaxTimeout     = 300000
)

func bashExecParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]string{
				"type":        "string",
				"description": "The bash command to execute (e.g., 'ls', 'git status', 'npm install')",
			},
			"args": map[string]any{
				"type":        "array",
				"description": "Command-line arguments for the command",
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
	}
}

func bashExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Cwd       string   `json:"cwd"`
		TimeoutMs int      `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	// Validate command is not empty
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return "Tool Error: 'command' is required and cannot be empty.", nil
	}

	// Validate cwd
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

	// Validate timeout
	timeoutMs := p.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = bashDefaultTimeout
	}
	if timeoutMs < 1 || timeoutMs > bashMaxTimeout {
		return "Tool Error: timeout_ms must be between 1 and 300000.", nil
	}

	// Build the full shell command
	shellCommand := command
	if len(p.Args) > 0 {
		shellCommand += " " + shellEscapeArgs(p.Args)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	timeout := time.Duration(timeoutMs) * time.Millisecond

	result, err := rt.RunCommandOnHost(ctx, cwd, shellCommand, timeout)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	return sandbox.FormatCommandResult(result), nil
}

// shellEscapeArgs escapes arguments for shell execution.
func shellEscapeArgs(args []string) string {
	var escaped []string
	for _, arg := range args {
		// Simple escaping: wrap in quotes if contains spaces or special chars
		if strings.ContainsAny(arg, " \"'\t\n\r") {
			escaped = append(escaped, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
		} else {
			escaped = append(escaped, arg)
		}
	}
	return strings.Join(escaped, " ")
}
