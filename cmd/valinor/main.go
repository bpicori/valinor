package main

import (
	"context"
	"os"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/session"
	"valinor/internal/tui"
	"valinor/internal/web"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if err := run(); err != nil {
		applog.Error("tui exit error", applog.M("error", err))
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		applog.Error("load config failed", applog.M("error", err))
		return err
	}

	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		applog.Error("resolve valinor dir failed", applog.M("error", err))
		return err
	}
	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		applog.Error("resolve session dir failed", applog.M("error", err))
		return err
	}
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		applog.Error("resolve workspace dir failed", applog.M("error", err))
		return err
	}

	logPath, closeLogger, err := applog.Init(valinorDir)
	if err != nil {
		applog.Error("init logger failed", applog.M("error", err))
		return err
	}
	defer func() {
		if err := closeLogger(); err != nil {
			applog.Error("close logger failed", applog.M("error", err))
		}
	}()
	applog.Info("logging to file", applog.M("log_path", logPath))

	b := bus.New()
	sm := session.NewManager(sessionsDir, workspaceDir)
	createCfg := session.CreateConfig{
		Config:             cfg,
		Bus:                b,
		MaxAgentIterations: cfg.MaxAgentIterations,
	}

	var sess *session.Session
	sessions, err := sm.List()
	if err != nil {
		applog.Error("list sessions failed", applog.M("error", err))
		return err
	}
	if len(sessions) > 0 {
		latestSession := sessions[0]
		sess, err = sm.Resume(context.Background(), latestSession.ID, createCfg)
		if err != nil {
			applog.Error("resume latest session failed", applog.M("session_id", latestSession.ID, "error", err))
		} else {
			applog.Info("resumed session", applog.M("session_id", latestSession.ID))
		}
	}
	if sess == nil {
		sess, err = sm.Create(context.Background(), createCfg)
		if err != nil {
			applog.Error("create session failed", applog.M("error", err))
			return err
		}
		applog.Info("created new session", applog.M("session_id", sess.Meta().ID))
	}
	defer sess.Close()

	go sess.Run(ctx)

	if cfg.WebEnabled {
		webServer := web.NewServer(cfg.WebHost, cfg.WebPort, cfg, b, sm, sess.Meta().ID)
		if err := webServer.Start(ctx); err != nil {
			applog.Error("start web server failed", applog.M("error", err))
			return err
		}
	}

	// Use session's provider/model for display; will be updated via bus when changed
	providerName := sess.Meta().Provider
	if providerName == "" {
		providerName = cfg.DefaultProvider
	}
	modelName := sess.Meta().Model
	if modelName == "" {
		if pc, ok := cfg.Providers[providerName]; ok {
			modelName = pc.DefaultModel
		}
		if modelName == "" && len(cfg.Providers) > 0 {
			for _, pc := range cfg.Providers {
				modelName = pc.DefaultModel
				break
			}
		}
	}
	model := tui.New(b, cfg, providerName, modelName)
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	uiSub := tui.StartBusBridge(ctx, p, b)
	defer b.Unsubscribe(uiSub)

	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}
