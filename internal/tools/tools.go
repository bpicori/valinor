package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"valinor/internal/applog"
	"valinor/internal/llm"
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
		applog.Warning("tool path validation failed", applog.BuildMetadata("path", path, "reason", "absolute or .. not allowed"))
		return "", "Tool Error: path must be relative to workspace (absolute paths and \"..\" not allowed)"
	}
	return cleaned, ""
}

// buildToolDef creates a provider-agnostic tool definition from a JSON Schema.
func buildToolDef(name, description string, parameters map[string]any) llm.ToolDefinition {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		applog.Error("tool definition marshal error", applog.BuildMetadata("tool", name, "error", err))
		paramsJSON = json.RawMessage("{}")
	}
	return llm.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  paramsJSON,
	}
}

// AvailableTools returns the deterministic list of tool metadata.
func AvailableTools() []llm.AvailableTool {
	defs := []llm.ToolDefinition{
		readFileDef(),
		writeFileDef(),
		editFileDef(),
		listFilesDef(),
		grepDef(),
		runCommandDef(),
	}
	out := make([]llm.AvailableTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, llm.AvailableTool{
			Name:        def.Name,
			Description: def.Description,
		})
	}
	return out
}

// BuildTools returns tool definitions for the given runtime.
func BuildTools(rt *sandbox.Runtime) []llm.Tool {
	return []llm.Tool{
		{readFileName, readFileDef(), func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }},
		{writeFileName, writeFileDef(), func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }},
		{editFileName, editFileDef(), func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }},
		{listFilesName, listFilesDef(), func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }},
		{grepName, grepDef(), func(a json.RawMessage) (string, error) { return grepExec(rt, a) }},
		{runCommandName, runCommandDef(), func(a json.RawMessage) (string, error) { return runCommandExec(rt, a) }},
	}
}
