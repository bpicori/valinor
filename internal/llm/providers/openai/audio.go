package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"valinor/internal/llm"

	"github.com/openai/openai-go/v3"
)

// TranscribeAudio transcribes audio bytes into text via the STT API.
func (p *Provider) TranscribeAudio(ctx context.Context, params llm.AudioTranscribeParams) (string, error) {
	if len(params.Audio) == 0 {
		return "", fmt.Errorf("audio data is empty")
	}
	if params.Model == "" {
		return "", fmt.Errorf("transcription model is required")
	}

	format := params.Format
	if format == "" {
		format = "ogg"
	}

	if p.baseURL != "" {
		return p.transcribeAudioJSON(ctx, params.Model, params.Audio, format)
	}

	res, err := p.client.Audio.Transcriptions.New(ctx, openai.AudioTranscriptionNewParams{
		Model: openai.AudioModel(params.Model),
		File:  bytes.NewReader(params.Audio),
	})
	if err != nil {
		return "", fmt.Errorf("audio transcription: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Text) == "" {
		return "", fmt.Errorf("audio transcription returned empty text")
	}
	return strings.TrimSpace(res.Text), nil
}

type transcriptionJSONRequest struct {
	Model      string             `json:"model"`
	InputAudio transcriptionAudio `json:"input_audio"`
}

type transcriptionAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type transcriptionJSONResponse struct {
	Text string `json:"text"`
}

// transcribeAudioJSON calls OpenAI-compatible STT endpoints (e.g. OpenRouter /audio/transcriptions).
func (p *Provider) transcribeAudioJSON(ctx context.Context, model string, audio []byte, format string) (string, error) {
	baseURL := strings.TrimRight(p.baseURL, "/")
	body, err := json.Marshal(transcriptionJSONRequest{
		Model: model,
		InputAudio: transcriptionAudio{
			Data:   base64.StdEncoding.EncodeToString(audio),
			Format: format,
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal transcription request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/audio/transcriptions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create transcription request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute transcription request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("transcription request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result transcriptionJSONResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse transcription response: %w", err)
	}
	if strings.TrimSpace(result.Text) == "" {
		return "", fmt.Errorf("audio transcription returned empty text")
	}
	return strings.TrimSpace(result.Text), nil
}