package factory

import (
	"fmt"

	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/llm"
	"valinor/internal/llm/providers/anthropic"
	"valinor/internal/llm/providers/openai"
)

// CreateProvider instantiates a provider based on the given configuration.
func CreateProvider(cfg config.ProviderConfig, bus *bus.Bus) (llm.Provider, error) {
	switch cfg.Type {
	case "openai":
		return openai.New(cfg.APIKey, "", bus)
	case "anthropic":
		return anthropic.New(cfg.APIKey, bus)
	case "openai-compatible":
		return openai.New(cfg.APIKey, cfg.BaseURL, bus)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}
