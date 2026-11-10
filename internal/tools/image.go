package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/event"
	"valinor/internal/imagegen"
	"valinor/internal/sandbox"
)

const (
	sendImageName          = "send_image"
	generateImageName      = "generate_image"
	maxImageBytes          = 10 << 20
	imageGenerationTimeout = 120 * time.Second
)

func sendImageParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]string{
				"type":        "string",
				"description": "Relative path to an existing image file inside the workspace. Use this whenever the user asks to show or send an image to the current chat/client, including Telegram.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func generateImageParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]string{
				"type":        "string",
				"description": "Text description of the image to generate, or of the change to apply when images are supplied.",
			},
			"images": map[string]any{
				"type":        "array",
				"items":       map[string]string{"type": "string"},
				"description": "Optional workspace-relative paths of reference images to edit, restyle, or combine (at most 14). Read them with read_image first. When set, the prompt describes the desired change.",
			},
			"send": map[string]any{
				"type":        "boolean",
				"description": "When true (default), deliver the image to the current client (e.g. Telegram). When false, save to workspace only.",
			},
			"aspect_ratio": map[string]string{
				"type":        "string",
				"description": "Optional aspect ratio, e.g. 1:1, 16:9, 9:16.",
			},
			"size": map[string]string{
				"type":        "string",
				"description": "Optional resolution tier (512, 768, 1K, 1.5K, 2K, 4K) or explicit pixels (e.g. 1024x1024).",
			},
		},
		"required":             []string{"prompt"},
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

	mimeType, imageErr := validateImageBytes(data)
	if imageErr != "" {
		return imageErr, nil
	}

	publishAgentImage(runContext, filepath.Base(cleaned), mimeType, data)
	return cleaned, nil
}

func generateImageExec(rt *sandbox.Runtime, runContext *RunContext, generator imagegen.Provider, args json.RawMessage) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("sandbox runtime is required")
	}
	if generator == nil {
		return "Tool Error: image generation is not configured.", nil
	}

	var payload struct {
		Prompt      string   `json:"prompt"`
		Images      []string `json:"images"`
		Send        *bool    `json:"send"`
		AspectRatio string   `json:"aspect_ratio"`
		Size        string   `json:"size"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", err
	}

	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		return "Tool Error: prompt is required.", nil
	}

	references, referenceErr := loadImageReferences(rt, payload.Images)
	if referenceErr != "" {
		return referenceErr, nil
	}

	send := true
	if payload.Send != nil {
		send = *payload.Send
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageGenerationTimeout)
	defer cancel()

	data, err := generator.Generate(ctx, imagegen.Params{
		Prompt:      prompt,
		AspectRatio: payload.AspectRatio,
		Size:        payload.Size,
		References:  references,
	})
	if err != nil {
		return "", fmt.Errorf("generate image: %w", err)
	}

	mimeType, imageErr := validateImageBytes(data)
	if imageErr != "" {
		if strings.Contains(imageErr, "exceeds") {
			imageErr += " Retry with a smaller size (e.g. 1K)."
		}
		return imageErr, nil
	}

	imagesDir := filepath.Join(rt.WorkspaceDir(), "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return "", fmt.Errorf("create images directory: %w", err)
	}

	fileName := fmt.Sprintf("generated-%d%s", time.Now().Unix(), imageExtensionForMIME(mimeType))
	relativePath := filepath.ToSlash(filepath.Join("images", fileName))
	if err := os.WriteFile(filepath.Join(rt.WorkspaceDir(), relativePath), data, 0o644); err != nil {
		return "", fmt.Errorf("save generated image: %w", err)
	}

	if send {
		publishAgentImage(runContext, fileName, mimeType, data)
		return fmt.Sprintf("%s (sent to client)", relativePath), nil
	}
	return relativePath, nil
}

// loadImageReferences reads workspace images used to guide generation or
// editing. It returns a tool error message when a path or file is invalid.
func loadImageReferences(rt *sandbox.Runtime, paths []string) ([]imagegen.Reference, string) {
	if len(paths) == 0 {
		return nil, ""
	}
	if len(paths) > imagegen.MaxReferences {
		return nil, fmt.Sprintf("Tool Error: at most %d reference images are supported.", imagegen.MaxReferences)
	}

	references := make([]imagegen.Reference, 0, len(paths))
	for _, raw := range paths {
		cleaned, validationErr := validateRelativePath(raw)
		if validationErr != "" {
			return nil, validationErr
		}
		data, err := os.ReadFile(filepath.Join(rt.WorkspaceDir(), filepath.FromSlash(cleaned)))
		if err != nil {
			return nil, fmt.Sprintf("Tool Error: read reference image %q: %v", raw, err)
		}
		mimeType, imageErr := validateImageBytes(data)
		if imageErr != "" {
			return nil, imageErr
		}
		references = append(references, imagegen.Reference{Data: data, MimeType: mimeType})
	}
	return references, ""
}

func validateImageBytes(data []byte) (string, string) {
	if len(data) == 0 {
		return "", "Tool Error: image file is empty."
	}
	if len(data) > maxImageBytes {
		return "", fmt.Sprintf("Tool Error: image exceeds %d byte limit.", maxImageBytes)
	}

	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mimeType, ""
	default:
		return "", fmt.Sprintf("Tool Error: unsupported image type %q.", mimeType)
	}
}

func publishAgentImage(runContext *RunContext, fileName, mimeType string, data []byte) {
	if snap, ok := runContext.Snapshot(); ok && snap.EmitEvents && snap.SessionID != "" {
		event.Publish(event.Event{
			Type: event.AgentImage,
			Payload: event.ImagePayload{
				FileName: fileName,
				MIMEType: mimeType,
				Base64:   base64.StdEncoding.EncodeToString(data),
			},
			SessionID: snap.SessionID,
			RequestID: snap.RequestID,
		})
	}
}

func imageExtensionForMIME(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
