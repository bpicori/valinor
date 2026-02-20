package tools

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
	"valinor/internal/sandbox"
)

const readFileName = "read_file"

func readFileDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        readFileName,
		Description: openai.String("Read the contents of a file. Path is relative to the workspace root."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the file",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	})
}

func readFileExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
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
	return rt.ReadFile(path)
}
