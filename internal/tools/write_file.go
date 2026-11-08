package tools

import (
	"encoding/json"
	"strings"

	"valinor/internal/sandbox"
)

const writeFileName = "write_file"

func writeFileParams() map[string]any {
	return map[string]any{
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
	}
}

func writeFileExec(rt *sandbox.Runtime, run *RunContext, args json.RawMessage) (string, error) {
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
	result, err := rt.WriteFile(path, p.Content)
	if err != nil {
		return result, err
	}
	if !strings.HasPrefix(result, "Tool Error: ") {
		if sig, sigErr := fileSignature(rt, path); sigErr == nil {
			run.RecordFileSignature(path, sig)
		}
	}
	return result, nil
}
