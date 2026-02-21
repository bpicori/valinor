package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/session"
	"valinor/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
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
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		log.Fatalf("resolve workspace dir: %v", err)
	}

	logPath, closeLogger, err := applog.Init(workspaceDir)
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer func() {
		if err := closeLogger(); err != nil {
			fmt.Fprintf(os.Stderr, "close logger: %v\n", err)
		}
	}()
	log.Printf("logging to file: %s", logPath)

	b := bus.New()
	sm := session.NewManager(sessionsDir, workspaceDir)

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
	uiSub := tui.StartBusBridge(ctx, p, b)
	defer b.Unsubscribe(uiSub)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
