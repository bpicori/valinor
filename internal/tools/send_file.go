package tools

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"valinor/internal/event"
	"valinor/internal/sandbox"
)

const (
	// SendFileName is exported so scheduled agent runs can exclude it.
	SendFileName      = "send_file"
	maxSendFileBytes  = 50 << 20
	maxFileCaptionLen = 1024
)

func sendFileParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to an existing workspace file.",
			},
			"caption": map[string]string{
				"type":        "string",
				"description": "Optional caption shown with the file (max 1024 characters).",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func sendFileExec(rt *sandbox.Runtime, run *RunContext, args json.RawMessage) (ToolResult, error) {
	if rt == nil {
		return ToolResult{}, fmt.Errorf("sandbox runtime is required")
	}

	var p struct {
		Path    string `json:"path"`
		Caption string `json:"caption"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ToolResult{}, err
	}

	cleaned, msg := validateRelativePath(p.Path)
	if msg != "" {
		return ToolResult{Text: msg}, nil
	}

	workspace, err := filepath.Abs(rt.WorkspaceDir())
	if err != nil {
		return ToolResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return ToolResult{}, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	fullPath := filepath.Join(workspace, filepath.FromSlash(cleaned))
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return ToolResult{Text: "Tool Error: " + err.Error()}, nil
	}
	rel, err := filepath.Rel(workspace, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ToolResult{Text: "Tool Error: path must stay inside the workspace."}, nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return ToolResult{Text: "Tool Error: " + err.Error()}, nil
	}
	if !info.Mode().IsRegular() {
		return ToolResult{Text: "Tool Error: only regular files can be sent."}, nil
	}
	if info.Size() == 0 {
		return ToolResult{Text: "Tool Error: file is empty."}, nil
	}
	if info.Size() > maxSendFileBytes {
		return ToolResult{Text: fmt.Sprintf("Tool Error: file is %d bytes; the limit is %d MB.", info.Size(), maxSendFileBytes/(1<<20))}, nil
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return ToolResult{Text: "Tool Error: " + err.Error()}, nil
	}
	if int64(len(data)) > maxSendFileBytes {
		return ToolResult{Text: fmt.Sprintf("Tool Error: file is %d bytes; the limit is %d MB.", len(data), maxSendFileBytes/(1<<20))}, nil
	}

	caption := strings.TrimSpace(p.Caption)
	if utf8.RuneCountInString(caption) > maxFileCaptionLen {
		caption = string([]rune(caption)[:maxFileCaptionLen])
	}

	mimeType := mime.TypeByExtension(filepath.Ext(cleaned))
	if mimeType == "" {
		mimeType = strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0])
	}

	snapshot, ok := run.Snapshot()
	if !ok || !snapshot.EmitEvents || snapshot.SessionID == "" {
		return ToolResult{Text: "Tool Error: no active client to send files to."}, nil
	}

	event.Publish(event.Event{
		Type: event.AgentFile,
		Payload: event.FilePayload{
			Path:     cleaned,
			FileName: filepath.Base(cleaned),
			MIMEType: mimeType,
			Size:     info.Size(),
			Caption:  caption,
		},
		SessionID: snapshot.SessionID,
		RequestID: snapshot.RequestID,
	})

	return ToolResult{Text: fmt.Sprintf("Sent %s (%s) to the current client.", filepath.Base(cleaned), formatFileSize(info.Size()))}, nil
}

func formatFileSize(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", size)
	}
}
