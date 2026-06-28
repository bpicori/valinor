package factory

import (
	"context"

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