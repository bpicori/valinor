package wizard

import (
	"fmt"

	"valinor/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the setup wizard and saves to savePath when non-empty.
func Run(initial config.Config, savePath string) (config.Config, error) {
	if savePath == "" {
		path, err := config.DefaultConfigPath()
		if err != nil {
			return config.Config{}, err
		}
		savePath = path
	}
	model := New(initial, savePath)
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
