package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/llm"
	"valinor/internal/sandbox"
)

const (
	pythonExecName   = "python_exec"
	defaultTimeoutMs = 30000
	maxTimeoutMs     = 300000
)

func pythonExecDef() llm.ToolDefinition {
	return buildToolDef(
		pythonExecName,
		"Execute Python code in the workspace using the host Python runtime. Use for running Python scripts, data analysis, file processing, or Python automation.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code": map[string]string{
					"type":        "string",
					"description": "Python code to execute inline. Either 'code' or 'script_path' must be provided (not both).",
				},
				"script_path": map[string]string{
					"type":        "string",
					"description": "Relative path to a Python script file in the workspace to execute. Either 'code' or 'script_path' must be provided (not both).",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command-line arguments for script mode (ignored in inline mode)",
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
			"required":             []string{},
			"additionalProperties": false,
		},
	)
}

func pythonExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Code       string   `json:"code"`
		ScriptPath string   `json:"script_path"`
		Args       []string `json:"args"`
		Cwd        string   `json:"cwd"`
		TimeoutMs  int      `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	// Validate mode: exactly one of code or script_path must be provided
	codeProvided := strings.TrimSpace(p.Code) != ""
	scriptProvided := strings.TrimSpace(p.ScriptPath) != ""

	if codeProvided && scriptProvided {
		return "Tool Error: provide either 'code' or 'script_path', not both.", nil
	}
	if !codeProvided && !scriptProvided {
		return "Tool Error: either 'code' or 'script_path' is required.", nil
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

	// Validate script_path if provided
	if scriptProvided {
		scriptPath := normalizePath(p.ScriptPath)
		if scriptPath == "" {
			return "Tool Error: script_path is empty.", nil
		}
		if strings.HasPrefix(scriptPath, "..") || strings.Contains(scriptPath, "/..") {
			return "Tool Error: script_path must not escape workspace (\"..\" not allowed).", nil
		}
		if filepath.IsAbs(scriptPath) {
			return "Tool Error: script_path must be relative to workspace (absolute paths not allowed).", nil
		}
	}

	// Validate timeout
	timeoutMs := p.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = defaultTimeoutMs
	}
	if timeoutMs < 1 || timeoutMs > maxTimeoutMs {
		return "Tool Error: timeout_ms must be between 1 and 300000.", nil
	}

	// Determine mode and prepare execution
	var mode, code string
	if codeProvided {
		mode = "inline"
		code = p.Code
	} else {
		mode = "script"
		code = normalizePath(p.ScriptPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	timeout := time.Duration(timeoutMs) * time.Millisecond

	result, err := rt.RunPython(ctx, cwd, mode, code, p.Args, timeout)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	return sandbox.FormatCommandResult(result), nil
}
