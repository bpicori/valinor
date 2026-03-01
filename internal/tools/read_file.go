package tools

import (
	"encoding/json"

	"valinor/internal/sandbox"
)

const readFileName = "read_file"

func readFileParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to the file",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
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
