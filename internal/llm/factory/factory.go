package factory

import (
	"fmt"

	"valinor/internal/config"
	"valinor/internal/llm"
	"valinor/internal/llm/providers/anthropic"
	"valinor/internal/llm/providers/gemini"
	"valinor/internal/llm/providers/openai"
)

// CreateProvider instantiates a provider based on the given configuration.
func CreateProvider(cfg config.ProviderConfig) (llm.Provider, error) {
	client := httpClient()
	var provider llm.Provider
	var err error
	switch cfg.Type {
	case "openai":
		provider, err = openai.New(cfg.APIKey, "", client)
	case "anthropic":
		provider, err = anthropic.New(cfg.APIKey, client)
	case "gemini":
		provider, err = gemini.New(cfg.APIKey, client)
	case "openai-compatible":
		provider, err = openai.New(cfg.APIKey, cfg.BaseURL, client)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
	if err != nil {
		return nil, err
	}
	return withRequestTimeouts(provider), nil
}