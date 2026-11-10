// Package openrouter generates images through OpenRouter's Image API.
package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"valinor/internal/config"
	"valinor/internal/imagegen"
	"valinor/internal/logger"
)

const (
	name = "openrouter"

	// maxResponseSize bounds the image response before decoding. A base64
	// payload of a 10 MB image stays well below this limit.
	maxResponseSize = 20 << 20

	// maxReferenceBytes bounds the combined size of reference images sent in
	// one request.
	maxReferenceBytes = 30 << 20
)

// Provider generates images through OpenRouter's Image API.
type Provider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// New creates an OpenRouter image provider.
func New(cfg config.ImageGenerationConfig) (*Provider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("image generation api_key is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("image generation model is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = config.DefaultImageGenerationBaseURL
	}
	return &Provider{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		client:  newHTTPClient(),
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return name
}

type generateRequest struct {
	Model           string           `json:"model"`
	Prompt          string           `json:"prompt"`
	AspectRatio     string           `json:"aspect_ratio,omitempty"`
	Resolution      string           `json:"resolution,omitempty"`
	Size            string           `json:"size,omitempty"`
	InputReferences []referenceImage `json:"input_references,omitempty"`
}

type referenceImage struct {
	Type     string   `json:"type"`
	ImageURL imageURL `json:"image_url"`
}

type imageURL struct {
	URL string `json:"url"`
}

type imageData struct {
	B64JSON string `json:"b64_json"`
}

type generateResponse struct {
	Data  []imageData `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Generate creates one image from the given prompt.
func (p *Provider) Generate(ctx context.Context, params imagegen.Params) ([]byte, error) {
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	resolution, size, err := splitSize(params.Size)
	if err != nil {
		return nil, err
	}
	// Explicit pixels are authoritative; OpenRouter rejects them combined with
	// aspect_ratio or resolution.
	aspectRatio := strings.TrimSpace(params.AspectRatio)
	if size != "" {
		aspectRatio = ""
	}

	references, err := buildReferences(params.References)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(generateRequest{
		Model:           p.model,
		Prompt:          prompt,
		AspectRatio:     aspectRatio,
		Resolution:      resolution,
		Size:            size,
		InputReferences: references,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal image request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/images", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute image request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read image response: %w", err)
	}
	if len(respBody) > maxResponseSize {
		return nil, fmt.Errorf("image response exceeds %d byte limit", maxResponseSize)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image request failed (%d): %s", resp.StatusCode, responseError(respBody))
	}

	var result generateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse image response: %w", err)
	}
	if result.Error != nil {
		message := strings.TrimSpace(result.Error.Message)
		if message == "" {
			message = "unknown error"
		}
		return nil, fmt.Errorf("image request failed: %s", message)
	}
	if len(result.Data) == 0 || strings.TrimSpace(result.Data[0].B64JSON) == "" {
		return nil, fmt.Errorf("image generation returned no images")
	}

	data, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("decode generated image: %w", err)
	}

	logger.Info("image generated", logger.BuildMetadata(
		"provider", name,
		"model", p.model,
		"bytes", len(data),
		"duration_ms", time.Since(start).Milliseconds(),
	))
	return data, nil
}

// buildReferences encodes reference images as base64 data URIs. OpenRouter's
// Image API accepts HTTP(S) URLs or data URIs in input_references.
func buildReferences(refs []imagegen.Reference) ([]referenceImage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > imagegen.MaxReferences {
		return nil, fmt.Errorf("at most %d reference images are supported", imagegen.MaxReferences)
	}

	total := 0
	references := make([]referenceImage, 0, len(refs))
	for i, ref := range refs {
		if len(ref.Data) == 0 {
			return nil, fmt.Errorf("reference image %d is empty", i+1)
		}
		total += len(ref.Data)
		if total > maxReferenceBytes {
			return nil, fmt.Errorf("reference images exceed %d byte limit", maxReferenceBytes)
		}
		mimeType := strings.TrimSpace(ref.MimeType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		references = append(references, referenceImage{
			Type: "image_url",
			ImageURL: imageURL{
				URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(ref.Data),
			},
		})
	}
	return references, nil
}

// splitSize maps a tool size argument to the OpenRouter resolution or pixel
// field. Tiers become resolution so providers such as Seedream receive a value
// they support.
func splitSize(raw string) (resolution, pixels string, err error) {
	value := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(raw)), " ", "")
	if value == "" {
		return "", "", nil
	}
	if _, ok := resolutionTiers[value]; ok {
		return value, "", nil
	}
	if canonical, ok := canonicalPixels(value); ok {
		return "", canonical, nil
	}
	return "", "", fmt.Errorf("size %q is not supported (use a resolution tier such as 1K, 2K, 4K, or pixels such as 1024x1024)", strings.TrimSpace(raw))
}

var resolutionTiers = map[string]struct{}{
	"512":  {},
	"768":  {},
	"1K":   {},
	"1.5K": {},
	"2K":   {},
	"4K":   {},
}

// canonicalPixels validates and normalizes an explicit WxH size.
func canonicalPixels(value string) (string, bool) {
	width, height, ok := strings.Cut(strings.ToUpper(value), "X")
	if !ok {
		return "", false
	}
	w, err := strconv.Atoi(strings.TrimSpace(width))
	if err != nil || w < 64 || w > 8192 {
		return "", false
	}
	h, err := strconv.Atoi(strings.TrimSpace(height))
	if err != nil || h < 64 || h > 8192 {
		return "", false
	}
	return fmt.Sprintf("%dx%d", w, h), true
}

// responseError extracts OpenRouter's error message, falling back to the raw body.
func responseError(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		message := strings.TrimSpace(parsed.Error.Message)
		if message != "" {
			if parsed.Error.Code != nil {
				return fmt.Sprintf("%s (code %v)", message, parsed.Error.Code)
			}
			return message
		}
	}
	if message := strings.TrimSpace(string(body)); message != "" {
		return message
	}
	return "unknown error"
}

// newHTTPClient returns a client with bounded connection setup. The overall
// request duration is controlled by the caller's context.
func newHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	transport.TLSHandshakeTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}
