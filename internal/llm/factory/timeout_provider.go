package factory

import (
	"context"
	"fmt"

	"valinor/internal/llm"
)

type timeoutProvider struct {
	inner llm.Provider
}

func withRequestTimeouts(provider llm.Provider) llm.Provider {
	return &timeoutProvider{inner: provider}
}

func (p *timeoutProvider) Name() string {
	return p.inner.Name()
}

func (p *timeoutProvider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	return p.inner.Stream(ctx, params)
}

func (p *timeoutProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	return p.inner.ListModels(ctx)
}

func (p *timeoutProvider) TranscribeAudio(ctx context.Context, params llm.AudioTranscribeParams) (string, error) {
	transcriber, ok := p.inner.(llm.AudioTranscriber)
	if !ok {
		return "", fmt.Errorf("provider does not support audio transcription")
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	return transcriber.TranscribeAudio(ctx, params)
}