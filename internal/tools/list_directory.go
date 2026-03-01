package tools

import (
	"encoding/json"

	"valinor/internal/sandbox"
)

const listFilesName = "list_directory"

func listFilesParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to the directory (default \".\")",
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	}
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
