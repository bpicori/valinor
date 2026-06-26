package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"valinor/internal/config"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
	"valinor/internal/toolschema"
)

// Tool is the agent-callable tool definition.
type Tool = toolschema.Tool

// RunContext carries per-run metadata for tools that emit client events.
type RunContext = toolschema.RunContext

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

// BindRunContext updates shared run metadata for tools built together.
func BindRunContext(run *RunContext, sessionID, requestID string, emitEvents bool) {
	if run == nil {
		return
	}
	run.Bind(sessionID, requestID, emitEvents)
}

// Exclude returns tools whose names are not in exclude.
func Exclude(tools []Tool, names ...string) []Tool {
	return toolschema.Exclude(tools, names...)
}

// BuildTools returns base tools plus any provider-specific tools for the run.
func BuildTools(rt *sandbox.Runtime, workspaceDir string, cfg config.Config, providerName, model string) ([]Tool, *RunContext, error) {
	_ = workspaceDir

	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		return nil, nil, fmt.Errorf("provider %q not found in config", providerName)
	}

	run := &RunContext{}
	result := []Tool{
		buildTool(readFileName, "Read the contents of a file. Path is relative to the workspace root.", readFileParams(), func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }),
		buildTool(writeFileName, "Write content to a file. Path is relative to the workspace root. Creates parent directories if needed. Overwrites existing files.", writeFileParams(), func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }),
		buildTool(editFileName, "Apply multiple targeted text edits to a file. Path is relative to the workspace root. Edits are applied in order.", editFileParams(), func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }),
		buildTool(memoryLogName, "Append a brief entry to today's daily memory log (memory/daily/YYYY-MM-DD.md). Use after completing tasks, learning preferences, or making decisions worth remembering. Entries are timestamped and append-only.", memoryLogParams(), func(a json.RawMessage) (string, error) { return memoryLogExec(rt, a) }),
		buildTool(memoryUpdateName, "Create or update a named section in MEMORY.md. Use for durable information: user preferences, project conventions, recurring constraints. Provide a section name and content; existing sections are replaced, new ones are appended.", memoryUpdateParams(), func(a json.RawMessage) (string, error) { return memoryUpdateExec(rt, a) }),
		buildTool(listFilesName, "List files and directories at the given path. Path is relative to the workspace root. Use \".\" for the root. Directories are marked with a trailing slash.", listFilesParams(), func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }),
		buildTool(grepName, "Search for a regex pattern in files. Returns matching lines with file paths and line numbers.", grepParams(), func(a json.RawMessage) (string, error) { return grepExec(rt, a) }),
		buildTool(pythonExecName, "Execute Python code in a sandboxed container. Use for running Python scripts, data analysis, file processing, or Python automation.", pythonExecParams(), func(a json.RawMessage) (string, error) { return pythonExec(rt, a) }),
		buildTool(SendImageName, "Send an image file to the current chat/client, including Telegram. Use whenever the user asks to show or send an existing image file from the workspace. Path is relative to the workspace root, and image bytes are not added to conversation history.", sendImageParams(), func(a json.RawMessage) (string, error) { return sendImageExec(rt, run, a) }),
		buildTool(bashExecName, "Execute bash commands in a sandboxed container. Use for running shell commands, git operations, package management, builds, tests, and system automation. Commands run with workspace directory access.", bashExecParams(), func(a json.RawMessage) (string, error) { return bashExec(rt, a) }),
		buildTool(todoWriteName, "Create or update a structured task list for the current job. Use to plan multi-step work, track progress, and ensure all steps are completed. Each todo has an id, content, and status (pending, in_progress, completed, cancelled).", todoWriteParams(), func(a json.RawMessage) (string, error) { return todoWriteExec(run, a) }),
		buildTool(todoReadName, "Read the current task list for this session. Returns all todos with their status.", todoReadParams(), func(a json.RawMessage) (string, error) { return todoReadExec(run, a) }),
		buildTool(CronjobName,
			"Manage scheduled cron jobs. For monitoring/alerts (price thresholds, health checks, status pings): write a script to scripts/<name>.sh first, then action=add with no_agent=true and script=<name>.sh. For tasks needing reasoning each run: action=add with message only. Other actions: list, remove, history, pause, resume, run, edit.",
			cronjobParams(), func(a json.RawMessage) (string, error) { return cronjobExec(run, a) }),
		buildTool(webSearchName, webSearchDescription, webSearchParams(), func(a json.RawMessage) (string, error) {
			return webSearchExec(rt, a)
		}),
	}

	providerTools, err := buildProviderTools(providerCfg, model)
	if err != nil {
		return nil, nil, err
	}

	return append(result, providerTools...), run, nil
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