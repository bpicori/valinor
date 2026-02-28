package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"valinor/internal/llm"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
)

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

// buildToolDef creates a provider-agnostic tool definition from a JSON Schema.
func buildToolDef(name, description string, parameters map[string]any) llm.ToolDefinition {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		logger.Error("tool definition marshal error", logger.BuildMetadata("tool", name, "error", err))
		paramsJSON = json.RawMessage("{}")
	}
	return llm.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  paramsJSON,
	}
}

// BuildTools returns tool definitions for the given runtime.
func BuildTools(rt *sandbox.Runtime) []llm.Tool {
	return []llm.Tool{
		{Name: readFileName, Def: readFileDef(), Exec: func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }},
		{Name: writeFileName, Def: writeFileDef(), Exec: func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }},
		{Name: editFileName, Def: editFileDef(), Exec: func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }},
		{Name: listFilesName, Def: listFilesDef(), Exec: func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }},
		{Name: grepName, Def: grepDef(), Exec: func(a json.RawMessage) (string, error) { return grepExec(rt, a) }},
		{Name: pythonExecName, Def: pythonExecDef(), Exec: func(a json.RawMessage) (string, error) { return pythonExec(rt, a) }},
		// Cron tools are stateless and use a globally-set service; they don't need the sandbox runtime
		{Name: cronAddName, Def: cronAddDef(), Exec: func(a json.RawMessage) (string, error) { return cronAddExec(a) }},
		{Name: cronListName, Def: cronListDef(), Exec: func(a json.RawMessage) (string, error) { return cronListExec(a) }},
		{Name: cronRemoveName, Def: cronRemoveDef(), Exec: func(a json.RawMessage) (string, error) { return cronRemoveExec(a) }},
	}
}
