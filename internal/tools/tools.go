package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"

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
	runContext  *RunContext
}

// RunContext carries per-run metadata needed by tools that emit client events.
type RunContext struct {
	mu         sync.RWMutex
	SessionID  string
	RequestID  string
	EmitEvents bool
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
func buildTool(name, description string, parameters map[string]any, runContext *RunContext, exec func(json.RawMessage) (string, error)) Tool {
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
		runContext:  runContext,
	}
}

// BindRunContext updates the current run metadata for a tool slice.
func BindRunContext(tools []Tool, sessionID, requestID string, emitEvents bool) {
	for i := range tools {
		if tools[i].runContext == nil {
			continue
		}
		tools[i].runContext.mu.Lock()
		tools[i].runContext.SessionID = sessionID
		tools[i].runContext.RequestID = requestID
		tools[i].runContext.EmitEvents = emitEvents
		tools[i].runContext.mu.Unlock()
	}
}

// BuildTools returns base tools plus any provider-specific tools for the run.
func BuildTools(rt *sandbox.Runtime, workspaceDir string, providerCfg config.ProviderConfig, model string) ([]Tool, error) {
	runContext := &RunContext{}
	result := []Tool{
		buildTool(readFileName, "Read the contents of a file. Path is relative to the workspace root.", readFileParams(), runContext, func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }),
		buildTool(writeFileName, "Write content to a file. Path is relative to the workspace root. Creates parent directories if needed. Overwrites existing files.", writeFileParams(), runContext, func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }),
		buildTool(editFileName, "Apply multiple targeted text edits to a file. Path is relative to the workspace root. Edits are applied in order.", editFileParams(), runContext, func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }),
		buildTool(memoryLogName, "Append a brief entry to today's daily memory log (memory/daily/YYYY-MM-DD.md). Use after completing tasks, learning preferences, or making decisions worth remembering. Entries are timestamped and append-only.", memoryLogParams(), runContext, func(a json.RawMessage) (string, error) { return memoryLogExec(rt, a) }),
		buildTool(memoryUpdateName, "Create or update a named section in MEMORY.md. Use for durable information: user preferences, project conventions, recurring constraints. Provide a section name and content; existing sections are replaced, new ones are appended.", memoryUpdateParams(), runContext, func(a json.RawMessage) (string, error) { return memoryUpdateExec(rt, a) }),
		buildTool(listFilesName, "List files and directories at the given path. Path is relative to the workspace root. Use \".\" for the root. Directories are marked with a trailing slash.", listFilesParams(), runContext, func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }),
		buildTool(grepName, "Search for a regex pattern in files. Returns matching lines with file paths and line numbers.", grepParams(), runContext, func(a json.RawMessage) (string, error) { return grepExec(rt, a) }),
		buildTool(pythonExecName, "Execute Python code in a sandboxed container. Use for running Python scripts, data analysis, file processing, or Python automation.", pythonExecParams(), runContext, func(a json.RawMessage) (string, error) { return pythonExec(rt, a) }),
		buildTool(SendImageName, "Send an image file to the connected client. Use after generating an image with python. Path is relative to the workspace root, and image bytes are not added to conversation history.", sendImageParams(), runContext, func(a json.RawMessage) (string, error) { return sendImageExec(rt, runContext, a) }),
		buildTool(bashExecName, "Execute bash commands in a sandboxed container. Use for running shell commands, git operations, package management, builds, tests, and system automation. Commands run with workspace directory access.", bashExecParams(), runContext, func(a json.RawMessage) (string, error) { return bashExec(rt, a) }),
		// Cron tools are stateless and use a globally-set service; they don't need the sandbox runtime
		buildTool(cronAddName, "Create a new scheduled cron job that runs at a specified interval or one-time. Use for recurring tasks like reminders, periodic checks, or scheduled reports. Returns the created job ID.", cronAddParams(), runContext, cronAddExec),
		buildTool(cronListName, "List scheduled cron jobs. Shows job names, IDs, schedules, and next run times. Use to see what tasks are scheduled or to find a job ID for removal.", cronListParams(), runContext, cronListExec),
		buildTool(cronRemoveName, "Remove a scheduled cron job permanently by its ID. Use cron_list first to find the job ID. This cannot be undone.", cronRemoveParams(), runContext, cronRemoveExec),
		buildTool(cronHistoryName, "View the execution history of scheduled cron jobs. Shows when jobs ran, their status (ok or error), and any output. Use to audit job performance or debug failures.", cronHistoryParams(), runContext, cronHistoryExec),
		// Todo tools are session-scoped and use the global todo store via RunContext
		buildTool(todoWriteName, "Create or update a structured task list for the current job. Use to plan multi-step work, track progress, and ensure all steps are completed. Each todo has an id, content, and status (pending, in_progress, completed, cancelled).", todoWriteParams(), runContext, func(a json.RawMessage) (string, error) { return todoWriteExec(runContext, a) }),
		buildTool(todoReadName, "Read the current task list for this session. Returns all todos with their status.", todoReadParams(), runContext, func(a json.RawMessage) (string, error) { return todoReadExec(runContext, a) }),
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
