package main

import (
	"context"
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/session"
	"valinor/internal/tui"
)

func main() {
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		log.Fatalf("resolve session dir: %v", err)
	}

	b := bus.New()
	sm := session.NewManager(sessionsDir)

	sess, err := sm.Create(context.Background(), session.CreateConfig{
		Model:              cfg.Model,
		Client:             cfg.NewClient(),
		Bus:                b,
		MaxAgentIterations: cfg.MaxAgentIterations,
	})
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sess.Run(ctx)

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
