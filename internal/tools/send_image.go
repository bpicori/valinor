package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/event"
	"valinor/internal/event_bus"
	"valinor/internal/sandbox"
)

const (
	// SendImageName is the tool name exposed to the agent.
	SendImageName = "send_image"
	maxImageBytes = 10 << 20
)

func sendImageParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to an image file inside the workspace. Use this after generating an image with python.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func sendImageExec(rt *sandbox.Runtime, runContext *RunContext, args json.RawMessage) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("sandbox runtime is required")
	}

	var payload struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", err
	}

	cleaned, validationErr := validateRelativePath(payload.Path)
	if validationErr != "" {
		return validationErr, nil
	}

	data, err := os.ReadFile(filepath.Join(rt.WorkspaceDir(), filepath.FromSlash(cleaned)))
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	if len(data) == 0 {
		return "Tool Error: image file is empty.", nil
	}
	if len(data) > maxImageBytes {
		return fmt.Sprintf("Tool Error: image exceeds %d byte limit.", maxImageBytes), nil
	}

	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return fmt.Sprintf("Tool Error: unsupported image type %q.", mimeType), nil
	}

	if runContext != nil {
		runContext.mu.RLock()
		sessionID := runContext.SessionID
		requestID := runContext.RequestID
		emitEvents := runContext.EmitEvents
		runContext.mu.RUnlock()

		if emitEvents && sessionID != "" {
			event_bus.Publish(event.Event{
				Type: event.AgentImage,
				Payload: event.ImagePayload{
					FileName: filepath.Base(cleaned),
					MIMEType: mimeType,
					Base64:   base64.StdEncoding.EncodeToString(data),
				},
				SessionID: sessionID,
				RequestID: requestID,
			})
		}
	}

	return cleaned, nil
}
