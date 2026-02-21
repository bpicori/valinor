package tools

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"valinor/internal/applog"
	"valinor/internal/sandbox"

	"github.com/openai/openai-go/v3"
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
		applog.Warning("tool path validation failed", applog.M("path", path, "reason", "absolute or .. not allowed"))
		return "", "Tool Error: path must be relative to workspace (absolute paths and \"..\" not allowed)"
	}
	return cleaned, ""
}

// Tool holds a tool's definition and execution function.
type Tool struct {
	Name string
	Def  openai.ChatCompletionToolUnionParam
	Exec func(json.RawMessage) (string, error)
}

// AvailableTool describes a tool exposed to the model.
type AvailableTool struct {
	Name        string
	Description string
}

// AvailableTools returns the deterministic list of tool metadata.
func AvailableTools() []AvailableTool {
	defs := []openai.ChatCompletionToolUnionParam{
		readFileDef(),
		writeFileDef(),
		editFileDef(),
		listFilesDef(),
		grepDef(),
		runCommandDef(),
	}
	out := make([]AvailableTool, 0, len(defs))
	for _, def := range defs {
		fn := def.GetFunction()
		if fn == nil {
			continue
		}
		description := ""
		if fn.Description.Valid() {
			description = fn.Description.Or("")
		}
		out = append(out, AvailableTool{
			Name:        fn.Name,
			Description: description,
		})
	}
	return out
}

// BuildTools returns tool definitions for the given runtime.
func BuildTools(rt *sandbox.Runtime) []Tool {
	return []Tool{
		{readFileName, readFileDef(), func(a json.RawMessage) (string, error) { return readFileExec(rt, a) }},
		{writeFileName, writeFileDef(), func(a json.RawMessage) (string, error) { return writeFileExec(rt, a) }},
		{editFileName, editFileDef(), func(a json.RawMessage) (string, error) { return editFileExec(rt, a) }},
		{listFilesName, listFilesDef(), func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }},
		{grepName, grepDef(), func(a json.RawMessage) (string, error) { return grepExec(rt, a) }},
		{runCommandName, runCommandDef(), func(a json.RawMessage) (string, error) { return runCommandExec(rt, a) }},
	}
}
