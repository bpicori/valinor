package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	agentcontext "valinor/internal/context"
	"valinor/internal/buildinfo"
	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/gateway"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	"valinor/internal/requiredskills"
	"valinor/internal/sandbox"
	"valinor/internal/scheduler"
	"valinor/internal/session"
	"valinor/internal/telegram"
	"valinor/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

type gatewayDeps struct {
	closeLogger    func() error
	cronJobs       *cron.Jobs
	sm             *session.Manager
	sessionRuntime *session.Runtime
	sched          *scheduler.Scheduler
	rtr            *gateway.Router
	gw             *gateway.Server
}

func (d *gatewayDeps) cleanup() {
	if d.rtr != nil {
		d.rtr.Stop()
	}
	if d.sessionRuntime != nil {
		d.sessionRuntime.Close()
	}
	if d.sched != nil {
		d.sched.Stop()
	}
	if err := sandbox.CloseRuntime(); err != nil {
		logger.Error("sandbox runtime close error", logger.BuildMetadata("error", err))
	}
	if d.closeLogger != nil {
		d.closeLogger()
	}
}

func runRuntime(mode, gatewayURL, identity string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received", nil)
		cancel()
	}()

	switch mode {
	case "gateway":
		return runGateway(ctx)
	case "tui":
		return runTUI(ctx, gatewayURL, identity)
	case "telegram":
		return runTelegram(ctx)
	case "all":
		return runAll(ctx)
	}
	return nil
}

func initGateway(logPrefix string, transports []logger.TransportType) (*gatewayDeps, error) {
	cfg := config.Must()
	paths := config.MustPaths()

	if err := os.MkdirAll(paths.WorkspaceDir, 0755); err != nil {
		return nil, fmt.Errorf("create workspace directory: %w", err)
	}
	if err := requiredskills.EnsureRequiredSkills(paths.WorkspaceDir); err != nil {
		return nil, fmt.Errorf("ensure required skills: %w", err)
	}
	if err := agentcontext.EnsureMemory(paths.WorkspaceDir); err != nil {
		return nil, fmt.Errorf("ensure memory file: %w", err)
	}
	if err := agentcontext.EnsureIdentity(paths.WorkspaceDir); err != nil {
		return nil, fmt.Errorf("ensure identity file: %w", err)
	}
	if err := os.MkdirAll(paths.ScriptsDir, 0750); err != nil {
		return nil, fmt.Errorf("create scripts directory: %w", err)
	}

	logPath, closeLogger, err := logger.Init(paths.ValinorDir, transports)
	if err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}

	logger.SetPrefix(logPrefix)
	logger.Info("logging initialized", logger.BuildMetadata("log_path", logPath))

	if err := sandbox.InitRuntime(paths.WorkspaceDir, cfg.SearXNG); err != nil {
		closeLogger()
		return nil, fmt.Errorf("create sandbox runtime: %w", err)
	}
	logger.Info("sandbox runtime initialized", logger.BuildMetadata("workspace", paths.WorkspaceDir, "image", buildinfo.SandboxImage()))

	cronJobs, err := cron.NewJobs(paths.CronDir)
	if err != nil {
		logger.Error("failed to create cron jobs store, continuing without scheduling", logger.BuildMetadata("error", err))
		cronJobs = nil
	} else {
		cron.Init(cronJobs)
		logger.Info("cron jobs store initialized", logger.BuildMetadata("dir", paths.CronDir))
	}

	sm, err := session.NewManager(paths.SessionsDir, paths.WorkspaceDir)
	if err != nil {
		_ = sandbox.CloseRuntime()
		closeLogger()
		return nil, fmt.Errorf("create session manager: %w", err)
	}

	return &gatewayDeps{
		closeLogger: closeLogger,
		cronJobs:    cronJobs,
		sm:          sm,
	}, nil
}

func startGatewayStack(ctx context.Context, deps *gatewayDeps) (*gateway.Server, error) {
	cfg := config.Must()

	sessionRuntime := session.NewRuntime(ctx, deps.sm)
	sessionRuntime.Start()
	deps.sessionRuntime = sessionRuntime

	rtr := gateway.NewRouter(sessionRuntime, deps.sm)
	rtr.Start(ctx)
	deps.rtr = rtr

	if cfg.Scheduler.Enabled && deps.cronJobs != nil {
		sched := scheduler.New(deps.cronJobs, deps.sm)
		sched.Start(ctx)
		deps.sched = sched
		logger.Info("scheduler enabled", nil)
	} else if cfg.Scheduler.Enabled && deps.cronJobs == nil {
		logger.Warning("scheduler enabled but cron jobs store failed to initialize", nil)
	}

	gwCfg := gateway.Config{
		Host: cfg.GatewayHost,
		Port: cfg.GatewayPort,
		Path: cfg.GatewayPath,
	}
	gw := gateway.NewServer(gwCfg, rtr)
	deps.gw = gw

	logger.Info("gateway server starting", logger.BuildMetadata("url", cfg.GatewayWebSocketURL()))
	return gw, nil
}

func runGateway(ctx context.Context) error {
	deps, err := initGateway("GATEWAY", []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return err
	}
	defer deps.cleanup()

	logger.Info("starting valinor in gateway mode", nil)

	gw, err := startGatewayStack(ctx, deps)
	if err != nil {
		return err
	}

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("gateway server: %w", err)
	}

	if err := gw.Stop(); err != nil {
		return fmt.Errorf("stop gateway server: %w", err)
	}

	return nil
}

func runTUI(ctx context.Context, gatewayURL, identity string) error {
	cfg := config.Must()
	paths := config.MustPaths()

	if gatewayURL == "" {
		gatewayURL = cfg.GatewayWebSocketURL()
	}

	logPath, closeLogger, err := logger.Init(paths.ValinorDir, []logger.TransportType{logger.TransportFile})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	logger.SetPrefix("TUI")
	logger.Info("starting valinor in tui mode", nil)
	logger.Info("logging to file", logger.BuildMetadata("log_path", logPath))

	logger.Info("connecting to gateway", logger.BuildMetadata("url", gatewayURL, "identity", identity))
	wsBridge := tui.NewWSBridge(gatewayURL, identity)

	model := tui.New(wsBridge, cfg.DefaultProvider, "")

	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if err := wsBridge.Start(ctx, p); err != nil {
		return fmt.Errorf("connect to gateway: %w", err)
	}
	defer wsBridge.Stop()

	logger.Info("tui startup complete", logger.BuildMetadata("gateway", gatewayURL))

	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}

func runTelegram(ctx context.Context) error {
	cfg := config.Must()
	paths := config.MustPaths()

	if !cfg.Telegram.Enabled {
		return fmt.Errorf("telegram mode requires telegram.enabled = true in config")
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram mode requires telegram.bot_token in config")
	}

	logPath, closeLogger, err := logger.Init(paths.ValinorDir, []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	logger.SetPrefix("TELEGRAM")
	logger.Info("starting valinor in telegram mode", nil)
	logger.Info("logging to file", logger.BuildMetadata("log_path", logPath))

	gatewayURL := cfg.GatewayWebSocketURL()
	logger.Info("connecting to gateway", logger.BuildMetadata("url", gatewayURL))

	botCfg, err := newTelegramBotConfig(cfg, paths.ValinorDir, gatewayURL)
	if err != nil {
		return err
	}

	bot, err := telegram.NewBot(botCfg)
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}

	if err := bot.Start(ctx); err != nil {
		return fmt.Errorf("start telegram bot: %w", err)
	}
	defer bot.Stop()

	logger.Info("telegram bot started successfully", nil)

	<-ctx.Done()

	logger.Info("telegram bot shutting down", nil)
	return nil
}

func runAll(ctx context.Context) error {
	cfg := config.Must()
	paths := config.MustPaths()

	if !cfg.Telegram.Enabled {
		return fmt.Errorf("telegram mode requires telegram.enabled = true in config")
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram mode requires telegram.bot_token in config")
	}

	deps, err := initGateway("ALL", []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return err
	}

	logger.Info("starting valinor in all mode (gateway + telegram)", nil)

	gw, err := startGatewayStack(ctx, deps)
	if err != nil {
		deps.cleanup()
		return err
	}

	gwErrCh := make(chan error, 1)
	go func() {
		if err := gw.Start(ctx); err != nil {
			gwErrCh <- fmt.Errorf("gateway server: %w", err)
			return
		}
		gwErrCh <- nil
	}()

	gatewayURL := cfg.GatewayWebSocketURL()
	logger.Info("connecting telegram to gateway", logger.BuildMetadata("url", gatewayURL))

	botCfg, err := newTelegramBotConfig(cfg, paths.ValinorDir, gatewayURL)
	if err != nil {
		deps.cleanup()
		return err
	}

	bot, err := telegram.NewBot(botCfg)
	if err != nil {
		deps.cleanup()
		return fmt.Errorf("create telegram bot: %w", err)
	}

	if err := bot.Start(ctx); err != nil {
		deps.cleanup()
		return fmt.Errorf("start telegram bot: %w", err)
	}
	defer bot.Stop()

	logger.Info("telegram bot started successfully", nil)
	logger.Info("all services running (gateway + telegram)", nil)

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, stopping all services", nil)
	case err := <-gwErrCh:
		if err != nil {
			bot.Stop()
			deps.cleanup()
			return err
		}
	}

	bot.Stop()
	if err := gw.Stop(); err != nil {
		deps.cleanup()
		return fmt.Errorf("stop gateway server: %w", err)
	}

	deps.cleanup()
	logger.Info("all services stopped", nil)
	return nil
}

func newTelegramBotConfig(cfg config.Config, storageDir, gatewayURL string) (telegram.Config, error) {
	botCfg := telegram.Config{
		BotToken:    cfg.Telegram.BotToken,
		GatewayURL:  gatewayURL,
		StorageDir:  storageDir,
		PollTimeout: cfg.Telegram.PollTimeout,
		Providers:   cfg.Providers,
	}

	if name, providerCfg, ok := cfg.TranscriptionProvider(); ok {
		transcriber, err := factory.CreateAudioTranscriber(providerCfg)
		if err != nil {
			return telegram.Config{}, fmt.Errorf("create audio transcriber for provider %s: %w", name, err)
		}
		botCfg.Transcriber = transcriber
		botCfg.TranscriptionModel = providerCfg.TranscriptionModel
		logger.Info("telegram voice transcription enabled",
			logger.BuildMetadata("provider", name, "model", providerCfg.TranscriptionModel))
	}

	return botCfg, nil
}