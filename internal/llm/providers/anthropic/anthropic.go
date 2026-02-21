package anthropic

import (
	"context"
	"fmt"

	"valinor/internal/bus"
	"valinor/internal/llm"
)

// Provider implements the Anthropic (Claude) LLM provider.
// NOTE: This is a stub implementation. Full Anthropic support will be added in a future update.
type Provider struct {
	apiKey string
	bus    *bus.Bus
}

// New creates a new Anthropic provider instance.
func New(apiKey string, b *bus.Bus) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required for Anthropic provider")
	}
	return &Provider{
		apiKey: apiKey,
		bus:    b,
	}, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "anthropic"
}

// Stream implements the Provider interface for Anthropic.
// NOTE: This is a stub that returns an error. Full implementation coming soon.
func (p *Provider) Stream(ctx context.Context, params llm.StreamParams) (*llm.StreamResult, error) {
	return nil, fmt.Errorf("Anthropic provider is not fully implemented yet. Please use 'openai' or 'openai-compatible' provider types for now")
}
