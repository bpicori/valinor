package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"valinor/internal/sandbox"
)

const editFileName = "edit_file"

func editFileParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to the file",
			},
			"edits": map[string]any{
				"type":        "array",
				"description": "List of edits to apply in order",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old": map[string]string{
							"type":        "string",
							"description": "Exact text to find",
						},
						"new": map[string]string{
							"type":        "string",
							"description": "Replacement text",
						},
						"replace_all": map[string]string{
							"type":        "boolean",
							"description": "Replace all occurrences (default false)",
						},
					},
					"required":             []string{"old", "new"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"path", "edits"},
		"additionalProperties": false,
	}
}

func editFileExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Path  string `json:"path"`
		Edits []struct {
			Old        string `json:"old"`
			New        string `json:"new"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	path, msg := validateRelativePath(p.Path)
	if msg != "" {
		return msg, nil
	}
	if len(p.Edits) == 0 {
		return "Tool Error: edits must contain at least one edit.", nil
	}

	original, err := rt.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(original, "Tool Error: ") {
		return original, nil
	}

	updated := original
	applied := 0
	for i, edit := range p.Edits {
		if edit.Old == "" {
			return fmt.Sprintf("Tool Error: edits[%d].old must not be empty.", i), nil
		}

		if edit.ReplaceAll {
			count := strings.Count(updated, edit.Old)
			if count == 0 {
				return fmt.Sprintf("Tool Error: edits[%d].old not found in file.", i), nil
			}
			updated = strings.ReplaceAll(updated, edit.Old, edit.New)
			applied += count
			continue
		}

		if !strings.Contains(updated, edit.Old) {
			return fmt.Sprintf("Tool Error: edits[%d].old not found in file.", i), nil
		}
		updated = strings.Replace(updated, edit.Old, edit.New, 1)
		applied++
	}

	if updated == original {
		return "ok: no changes", nil
	}
	writeMsg, err := rt.WriteFile(path, updated)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(writeMsg, "Tool Error: ") {
		return writeMsg, nil
	}
	return fmt.Sprintf("ok: applied %d edit(s)", applied), nil
}
