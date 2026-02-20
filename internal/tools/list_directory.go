package tools

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"valinor/internal/sandbox"
)

const listFilesName = "list_directory"

func listFilesDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        listFilesName,
		Description: openai.String("List files and directories at the given path. Path is relative to the workspace root. Use \".\" for the root. Directories are marked with a trailing slash."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the directory (default \".\")",
				},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	})
}

func listFilesExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, msg := validateRelativePath(p.Path)
	if msg != "" {
		return msg, nil
	}
	return rt.ListDirectory(path)
}
