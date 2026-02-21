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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		log.Fatalf("[ERROR] load config: %v", err)
	}

	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		log.Fatalf("resolve valinor dir: %v", err)
	}
	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		log.Fatalf("resolve session dir: %v", err)
	}
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		log.Fatalf("resolve workspace dir: %v", err)
	}

	logPath, closeLogger, err := applog.Init(valinorDir)
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
	createCfg := session.CreateConfig{
		Model:              cfg.Model,
		APIKey:             cfg.APIKey,
		BaseURL:            cfg.BaseURL,
		Bus:                b,
		MaxAgentIterations: cfg.MaxAgentIterations,
	}

	var sess *session.Session
	sessions, err := sm.List()
	if err != nil {
		log.Fatalf("[ERROR] list sessions: %v", err)
	}
	if len(sessions) > 0 {
		latestSession := sessions[0]
		sess, err = sm.Resume(context.Background(), latestSession.ID, createCfg)
		if err != nil {
			log.Printf("[ERROR] resume latest session failed (%s): %v", latestSession.ID, err)
		} else {
			log.Printf("[INFO] resumed session: %s", latestSession.ID)
		}
	}
	if sess == nil {
		sess, err = sm.Create(context.Background(), createCfg)
		if err != nil {
			log.Fatalf("[ERROR] create session: %v", err)
		}
		log.Printf("[INFO] created new session: %s", sess.Meta().ID)
	}
	defer sess.Close()

	go sess.Run(ctx)

	model := tui.New(b, sess.Meta().Model)
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
