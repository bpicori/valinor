package main

import (
	"context"
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/agent"
	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/tui"
)

func main() {
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	b := bus.New()

	ag, err := agent.New(cfg.NewClient(), cfg.Model, "./agent_workspace", b)
	if err != nil {
		log.Fatalf("init agent: %v", err)
	}
	defer ag.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)

	model := tui.New(b, cfg.Model)
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
