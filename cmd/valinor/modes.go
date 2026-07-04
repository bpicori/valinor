package main

import (
	"fmt"
	"strings"
)

var validModes = map[string]struct{}{
	"gateway":  {},
	"tui":      {},
	"telegram": {},
	"all":      {},
}

func validateMode(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return fmt.Errorf("--mode is required")
	}
	if _, ok := validModes[mode]; !ok {
		return fmt.Errorf("invalid mode %q: must be one of gateway, tui, telegram, or all", mode)
	}
	return nil
}