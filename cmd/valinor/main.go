package main

import (
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/agent"
	"valinor/internal/config"
	"valinor/internal/ui"
)

func main() {
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ag, err := agent.New(cfg.NewClient(), cfg.Model, "./agent_workspace")
	if err != nil {
		log.Fatalf("init agent: %v", err)
	}
	defer ag.Close()

	model := ui.New(ag, cfg.Model)
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
