package factory

import (
	"fmt"

	"valinor/internal/config"
	"valinor/internal/llmapi"
	"valinor/internal/llm/providers/anthropic"
	"valinor/internal/llm/providers/gemini"
	"valinor/internal/llm/providers/openai"
)

// CreateProvider instantiates a provider based on the given configuration.
func CreateProvider(cfg config.ProviderConfig) (llmapi.Provider, error) {
	switch cfg.Type {
	case "openai":
		return openai.New(cfg.APIKey, "")
	case "anthropic":
		return anthropic.New(cfg.APIKey)
	case "gemini":
		return gemini.New(cfg.APIKey)
	case "openai-compatible":
		return openai.New(cfg.APIKey, cfg.BaseURL)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}