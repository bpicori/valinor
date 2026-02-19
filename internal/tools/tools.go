package tools

import (
	"encoding/json"
	"path/filepath"

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

// Tool holds a tool's definition and execution function.
type Tool struct {
	Name string
	Def  openai.ChatCompletionToolUnionParam
	Exec func(json.RawMessage) (string, error)
}

// BuildTools returns the file tools for the given sandbox.
func BuildTools(sb *Sandbox) []Tool {
	return []Tool{
		{readFileName, readFileDef(), func(a json.RawMessage) (string, error) { return readFileExec(sb, a) }},
		{writeFileName, writeFileDef(), func(a json.RawMessage) (string, error) { return writeFileExec(sb, a) }},
		{listFilesName, listFilesDef(), func(a json.RawMessage) (string, error) { return listFilesExec(sb, a) }},
		{grepName, grepDef(), func(a json.RawMessage) (string, error) { return grepExec(sb, a) }},
	}
}
