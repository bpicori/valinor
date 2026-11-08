// Package factory instantiates the configured web search provider.
package factory

import (
	"fmt"

	"valinor/internal/config"
	"valinor/internal/search"
	"valinor/internal/search/providers/deepseek"
)

// Create returns the configured search provider, or (nil, nil) when search is
// disabled. New providers are added by implementing search.Provider and
// extending this switch.
func Create(cfg config.SearchConfig) (search.Provider, error) {
	switch cfg.Provider {
	case "":
		return nil, nil
	case "deepseek":
		return deepseek.New(cfg)
	default:
		return nil, fmt.Errorf("unknown search provider %q (supported: deepseek)", cfg.Provider)
	}
}
