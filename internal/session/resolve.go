package session

import (
	"fmt"

	"valinor/internal/config"
)

func resolveProviderModel(cfg config.Config, provider, model string) (string, string, error) {
	resolvedProvider := provider
	if resolvedProvider == "" {
		resolvedProvider = cfg.DefaultProvider
	}
	providerCfg, ok := cfg.Providers[resolvedProvider]
	if !ok {
		return "", "", fmt.Errorf("provider '%s' not found in config", resolvedProvider)
	}

	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = providerCfg.DefaultModel
	}
	if resolvedModel == "" {
		return "", "", fmt.Errorf("provider and model are required")
	}

	return resolvedProvider, resolvedModel, nil
}
