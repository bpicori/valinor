package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"valinor/internal/config"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
)

// Tool represents a tool with its schema definition and execution function.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
	Exec        func(json.RawMessage) (string, error)
}

// normalizePath converts path separators for LLM-provided paths that may mix
// Windows and Unix separators, and cleans the path.
func normalizePath(path string) string {
	if path == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func validateRelativePath(path string) (string, string) {
	cleaned := normalizePath(path)
	if cleaned == "" {
		cleaned = "."
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/..") {
		logger.Warning("tool path validation failed", logger.BuildMetadata("path", path, "reason", "absolute or .. not allowed"))
		return "", "Tool Error: path must be relative to workspace (absolute paths and \"..\" not allowed)"
	}
	return cleaned, ""
}

// buildTool creates a tool with the given name, description, and parameters schema.
func buildTool(name, description string, parameters map[string]any, exec func(json.RawMessage) (string, error)) Tool {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		logger.Error("tool definition marshal error", logger.BuildMetadata("tool", name, "error", err))
		paramsJSON = json.RawMessage("{}")
	}
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  paramsJSON,
		Exec:        exec,
	}
}

// BuildTools returns base tools plus any provider-specific tools for the run.
func BuildTools(rt *sandbox.Runtime, workspaceDir string, providerCfg config.ProviderConfig, model string) ([]Tool, error) {
	result := []Tool{
		buildTool(readFileName, "Read the contents of a file. Path is relative to the workspace root.", readFileParams(), func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }),
		buildTool(writeFileName, "Write content to a file. Path is relative to the workspace root. Creates parent directories if needed. Overwrites existing files.", writeFileParams(), func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }),
		buildTool(editFileName, "Apply multiple targeted text edits to a file. Path is relative to the workspace root. Edits are applied in order.", editFileParams(), func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }),
		buildTool(listFilesName, "List files and directories at the given path. Path is relative to the workspace root. Use \".\" for the root. Directories are marked with a trailing slash.", listFilesParams(), func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }),
		buildTool(grepName, "Search for a regex pattern in files. Returns matching lines with file paths and line numbers.", grepParams(), func(a json.RawMessage) (string, error) { return grepExec(rt, a) }),
		buildTool(pythonExecName, "Execute Python code in a sandboxed container. Use for running Python scripts, data analysis, file processing, or Python automation.", pythonExecParams(), func(a json.RawMessage) (string, error) { return pythonExec(rt, a) }),
		buildTool(bashExecName, "Execute bash commands in a sandboxed container. Use for running shell commands, git operations, package management, builds, tests, and system automation. Commands run with workspace directory access.", bashExecParams(), func(a json.RawMessage) (string, error) { return bashExec(rt, a) }),
		// Cron tools are stateless and use a globally-set service; they don't need the sandbox runtime
		buildTool(cronAddName, "Create a new scheduled cron job that runs at a specified interval or one-time. Use for recurring tasks like reminders, periodic checks, or scheduled reports. Returns the created job ID.", cronAddParams(), cronAddExec),
		buildTool(cronListName, "List scheduled cron jobs. Shows job names, IDs, schedules, and next run times. Use to see what tasks are scheduled or to find a job ID for removal.", cronListParams(), cronListExec),
		buildTool(cronRemoveName, "Remove a scheduled cron job permanently by its ID. Use cron_list first to find the job ID. This cannot be undone.", cronRemoveParams(), cronRemoveExec),
	}

	providerTools, err := buildProviderTools(providerCfg, model)
	if err != nil {
		return nil, err
	}
	return append(result, providerTools...), nil
}

func buildProviderTools(providerCfg config.ProviderConfig, model string) ([]Tool, error) {
	switch providerCfg.Type {
	case "gemini":
		searchTool, err := BuildGeminiGoogleSearchTool(providerCfg.APIKey, model)
		if err != nil {
			return nil, err
		}
		return []Tool{searchTool}, nil
	default:
		return nil, nil
	}
}
