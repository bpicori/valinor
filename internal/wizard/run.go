package wizard

import (
	"fmt"

	"valinor/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the setup wizard and returns the resulting config.
func Run(initial config.Config) (config.Config, error) {
	model := New(initial)
	program := tea.NewProgram(&model, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return config.Config{}, fmt.Errorf("run wizard: %w", err)
	}

	result, ok := final.(*Model)
	if !ok {
		return config.Config{}, fmt.Errorf("unexpected wizard model type")
	}
	if result.cancelled {
		return config.Config{}, fmt.Errorf("setup cancelled")
	}
	if !result.saved {
		return config.Config{}, fmt.Errorf("setup incomplete")
	}
	return result.cfg, nil
}