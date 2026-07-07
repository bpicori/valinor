package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"valinor/internal/config"
	"valinor/internal/event"
	"valinor/internal/sandbox"
)

const (
	sendImageName          = "send_image"
	generateImageName      = "generate_image"
	maxImageBytes          = 10 << 20
	imageGenerationTimeout = 120 * time.Second
)

type imageGenerateRequest struct {
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Size        string `json:"size,omitempty"`
}

type imageGenerateResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
}

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
				"description": "Text description of the image to generate.",
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
				"description": "Optional size tier or pixels, e.g. 2K or 1024x1024.",
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

func generateImageExec(rt *sandbox.Runtime, runContext *RunContext, providerCfg config.ProviderConfig, args json.RawMessage) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("sandbox runtime is required")
	}

	var payload struct {
		Prompt      string `json:"prompt"`
		Send        *bool  `json:"send"`
		AspectRatio string `json:"aspect_ratio"`
		Size        string `json:"size"`
	}
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", err
	}

	prompt := strings.TrimSpace(payload.Prompt)
	if prompt == "" {
		return "Tool Error: prompt is required.", nil
	}
	if configErr := imageGenerationConfigError(providerCfg); configErr != "" {
		return configErr, nil
	}

	send := true
	if payload.Send != nil {
		send = *payload.Send
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageGenerationTimeout)
	defer cancel()

	data, err := generateImageHTTP(ctx, providerCfg, prompt, payload.AspectRatio, payload.Size)
	if err != nil {
		return "", fmt.Errorf("generate image: %w", err)
	}

	mimeType, imageErr := validateImageBytes(data)
	if imageErr != "" {
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

func imageGenerationConfigError(cfg config.ProviderConfig) string {
	if !cfg.ImageGenerationEnabled {
		return "Tool Error: image generation is not enabled for this provider."
	}
	if cfg.Type != "openai" && cfg.Type != "openai-compatible" {
		return "Tool Error: image generation requires an openai-compatible provider."
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return "Tool Error: image generation requires base_url on the provider."
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return "Tool Error: image generation requires api_key on the provider."
	}
	if strings.TrimSpace(cfg.ImageModel) == "" {
		return "Tool Error: image_model is not configured for this provider."
	}
	return ""
}

func generateImageHTTP(ctx context.Context, cfg config.ProviderConfig, prompt, aspectRatio, size string) ([]byte, error) {
	body, err := json.Marshal(imageGenerateRequest{
		Model:       cfg.ImageModel,
		Prompt:      prompt,
		AspectRatio: strings.TrimSpace(aspectRatio),
		Size:        strings.TrimSpace(size),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal image request: %w", err)
	}

	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/images"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute image request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result imageGenerateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse image response: %w", err)
	}
	if len(result.Data) == 0 || strings.TrimSpace(result.Data[0].B64JSON) == "" {
		return nil, fmt.Errorf("image generation returned no images")
	}

	data, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated image: %w", err)
	}
	return data, nil
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