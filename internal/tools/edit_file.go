package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/sandbox"
)

const (
	editFileName         = "edit_file"
	maxEditableFileBytes = 8 << 20
)

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

func editFileExec(rt *sandbox.Runtime, run *RunContext, args json.RawMessage) (string, error) {
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

	hostPath := filepath.Join(rt.WorkspaceDir(), filepath.FromSlash(path))
	if info, statErr := os.Stat(hostPath); statErr == nil && info.Size() > maxEditableFileBytes {
		return fmt.Sprintf(
			"Tool Error: file is %d bytes; edit_file supports up to %d MiB. Use bash_exec or python_exec for larger files.",
			info.Size(), maxEditableFileBytes/(1<<20)), nil
	}

	original, err := rt.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(original, "Tool Error: ") {
		return original, nil
	}

	if known, ok := run.FileSignature(path); !ok {
		return "Tool Error: read the file before editing it (read_file).", nil
	} else if sig, sigErr := fileSignature(rt, path); sigErr != nil || sig != known {
		return "Tool Error: file changed since it was read; read it again.", nil
	}

	updated := original
	applied := 0
	appliedEdits := make([][2]string, 0, len(p.Edits))
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
			appliedEdits = append(appliedEdits, [2]string{edit.Old, edit.New})
			continue
		}

		if !strings.Contains(updated, edit.Old) {
			return fmt.Sprintf("Tool Error: edits[%d].old not found in file.", i), nil
		}
		updated = strings.Replace(updated, edit.Old, edit.New, 1)
		applied++
		appliedEdits = append(appliedEdits, [2]string{edit.Old, edit.New})
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
	if sig, sigErr := fileSignature(rt, path); sigErr == nil {
		run.RecordFileSignature(path, sig)
	}
	return formatEditDiff(applied, appliedEdits), nil
}

// formatEditDiff renders the applied edits as a compact, reviewable diff.
func formatEditDiff(applied int, edits [][2]string) string {
	const maxDiffLines = 100

	var b strings.Builder
	fmt.Fprintf(&b, "ok: applied %d edit(s)\n", applied)

	remaining := maxDiffLines
	for i, edit := range edits {
		if remaining <= 0 {
			b.WriteString("\n... (diff truncated)\n")
			break
		}
		fmt.Fprintf(&b, "\n@@ edit %d @@\n", i+1)
		for _, line := range strings.Split(edit[0], "\n") {
			if remaining <= 0 {
				break
			}
			fmt.Fprintf(&b, "-%s\n", line)
			remaining--
		}
		for _, line := range strings.Split(edit[1], "\n") {
			if remaining <= 0 {
				break
			}
			fmt.Fprintf(&b, "+%s\n", line)
			remaining--
		}
	}
	return b.String()
}
