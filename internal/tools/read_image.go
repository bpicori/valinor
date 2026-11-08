package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"valinor/internal/media"
	"valinor/internal/sandbox"
)

const readImageName = "read_image"

func readImageParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to an image in the workspace (png, jpeg, gif, or webp).",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func readImageExec(rt *sandbox.Runtime, args json.RawMessage) (ToolResult, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ToolResult{}, err
	}

	path, msg := validateRelativePath(p.Path)
	if msg != "" {
		return ToolResult{Text: msg}, nil
	}

	data, err := os.ReadFile(filepath.Join(rt.WorkspaceDir(), filepath.FromSlash(path)))
	if err != nil {
		return ToolResult{Text: "Tool Error: " + err.Error()}, nil
	}

	mimeType, imageErr := validateImageBytes(data)
	if imageErr != "" {
		return ToolResult{Text: imageErr}, nil
	}

	return ToolResult{
		Text: fmt.Sprintf("Loaded image %s (%s, %d bytes); it is attached to the conversation for inspection.", path, mimeType, len(data)),
		Images: []media.Image{{
			Path:     path,
			MimeType: mimeType,
			Data:     data,
		}},
	}, nil
}
