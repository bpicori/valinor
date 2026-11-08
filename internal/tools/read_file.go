package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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
			"offset": map[string]any{
				"type":        "integer",
				"description": "1-based line to start reading from (default 1)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of lines to return (default 2000, max 2000)",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func readFileExec(rt *sandbox.Runtime, run *RunContext, args json.RawMessage) (string, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path, msg := validateRelativePath(p.Path)
	if msg != "" {
		return msg, nil
	}

	slice, err := rt.ReadFileRange(path, p.Offset, p.Limit)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}
	if hint := binaryReadHint(path, slice.Content); hint != "" {
		return hint, nil
	}
	if sig, sigErr := fileSignature(rt, path); sigErr == nil {
		run.RecordFileSignature(path, sig)
	}

	var b strings.Builder
	b.WriteString(slice.Content)

	if slice.Truncated {
		fmt.Fprintf(&b, "\n\n[output cut at the %d KB read limit; use a smaller limit]", sandbox.ReadRangeMaxBytes/1024)
	}
	if slice.TotalLines > 0 && slice.EndLine < slice.TotalLines {
		fmt.Fprintf(&b, "\n\n[lines %d-%d of %d; next offset=%d]",
			slice.StartLine, slice.EndLine, slice.TotalLines, slice.EndLine+1)
	}
	return b.String(), nil
}

// binaryReadHint points the model at the right tool when read_file hits an
// image or another binary format.
func binaryReadHint(path, content string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "Tool Error: this is an image; use read_image to attach it to the conversation."
	case ".pdf":
		return "Tool Error: PDFs are binary; use a PDF skill or bash/python to extract text."
	case ".docx", ".xlsx", ".pptx", ".zip":
		return "Tool Error: this is a binary document; use a skill or bash/python to inspect it."
	}
	if strings.IndexByte(content, 0) >= 0 {
		return "Tool Error: file looks binary; use read_image for images or bash/python for other formats."
	}
	return ""
}
