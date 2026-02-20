package tools

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"valinor/internal/sandbox"
)

const writeFileName = "write_file"

func writeFileDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        writeFileName,
		Description: openai.String("Write content to a file. Path is relative to the workspace root. Creates parent directories if needed. Overwrites existing files."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the file",
				},
				"content": map[string]string{
					"type":        "string",
					"description": "Content to write",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
	})
}

func writeFileExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, msg := validateRelativePath(p.Path)
	if msg != "" {
		return msg, nil
	}
	return rt.WriteFile(path, p.Content)
}
