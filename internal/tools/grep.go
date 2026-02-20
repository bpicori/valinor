package tools

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"valinor/internal/sandbox"
)

const grepName = "grep"

func grepDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        grepName,
		Description: openai.String("Search for a regex pattern in files. Returns matching lines with file paths and line numbers."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]string{
					"type":        "string",
					"description": "Regular expression pattern to search for",
				},
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to search (file or directory, default \".\")",
				},
				"include": map[string]string{
					"type":        "string",
					"description": "Glob pattern to filter files (e.g. \"*.go\", \"*.txt\")",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	})
}

func grepExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	if p.Pattern == "" {
		return "Tool Error: pattern is required", nil
	}

	_, err := regexp.Compile(p.Pattern)
	if err != nil {
		return "Tool Error: invalid regex: " + err.Error(), nil
	}

	path := normalizePath(p.Path)
	if path == "" {
		path = "."
	}
	if path != "." && (strings.HasPrefix(path, "..") || strings.Contains(path, "/..")) {
		return "Tool Error: path must not escape workspace (\"..\" not allowed)", nil
	}
	if filepath.IsAbs(path) {
		return "Tool Error: path must be relative to workspace (absolute paths not allowed)", nil
	}
	return rt.Grep(p.Pattern, path, p.Include)
}
