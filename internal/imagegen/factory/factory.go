// Package factory instantiates the configured image generation provider.
package factory

import (
	"fmt"

	"valinor/internal/config"
	"valinor/internal/imagegen"
	"valinor/internal/imagegen/providers/openrouter"
)

// Create returns the configured image provider, or (nil, nil) when image
// generation is disabled. New providers are added by implementing
// imagegen.Provider and extending this switch.
func Create(cfg config.ImageGenerationConfig) (imagegen.Provider, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "openrouter":
		return openrouter.New(cfg)
	default:
		return nil, fmt.Errorf("unknown image generation provider %q (supported: openrouter)", cfg.Provider)
	}
}
