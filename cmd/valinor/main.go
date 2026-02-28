package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/event_bus"
	"valinor/internal/gateway"
	"valinor/internal/logger"
	"valinor/internal/scheduler"
	"valinor/internal/session"
	"valinor/internal/telegram"
	"valinor/internal/tools"
	"valinor/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Define command-line flags
	var (
		mode       = flag.String("mode", "tui", "Run mode: 'gateway', 'tui', or 'telegram'")
		gatewayURL = flag.String("gateway-url", "", "Gateway WebSocket URL (for tui/telegram mode, defaults to config)")
		identity   = flag.String("identity", "default", "Client identity for session routing (tui mode)")
	)
	flag.Parse()

	if err := run(*mode, *gatewayURL, *identity); err != nil {
		logger.Error("valinor exit error", logger.BuildMetadata("error", err))
		os.Exit(1)
	}
}

func run(mode, gatewayURL, identity string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received", nil)
		cancel()
	}()

	// Load configuration (needed by both modes)
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		logger.Error("load config failed", logger.BuildMetadata("error", err))
		return err
	}

	switch mode {
	case "gateway":
		return runGateway(ctx, cfg)
	case "tui":
		return runTUI(ctx, cfg, gatewayURL, identity)
	case "telegram":
		return runTelegram(ctx, cfg)
	default:
		return fmt.Errorf("invalid mode '%s': must be 'gateway', 'tui', or 'telegram'", mode)
	}
}

// runGateway starts the gateway and session core (no TUI).
func runGateway(ctx context.Context, cfg config.Config) error {
	// Resolve directories
	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return fmt.Errorf("resolve valinor dir: %w", err)
	}
	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		return fmt.Errorf("resolve session dir: %w", err)
	}
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		return fmt.Errorf("resolve workspace dir: %w", err)
	}
	cronDir, err := cfg.ResolveCronDir()
	if err != nil {
		return fmt.Errorf("resolve cron dir: %w", err)
	}

	// Ensure workspace directory exists (required for sandbox operations)
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return fmt.Errorf("create workspace directory: %w", err)
	}

	// Initialize logger with both file and stdout transports
	logPath, closeLogger, err := logger.Init(valinorDir, []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	// Set log prefix for gateway mode
	logger.SetPrefix("GATEWAY")
	logger.Info("starting valinor in gateway mode", nil)
	logger.Info("logging to file and stdout", logger.BuildMetadata("log_path", logPath))

	// Initialize cron service
	cronService, err := cron.NewService(cronDir)
	if err != nil {
		logger.Error("failed to create cron service, continuing without scheduling", logger.BuildMetadata("error", err))
		cronService = nil
	} else {
		// Wire cron service to tools so the agent can schedule jobs
		tools.SetCronService(cronService)
		logger.Info("cron service initialized", logger.BuildMetadata("dir", cronDir))
	}

	// Initialize core components
	b := event_bus.New()
	sm := session.NewManager(sessionsDir, workspaceDir, cfg)

	// Session options used when router/runtime create or resume sessions on-demand.
	sessionOpts := session.SessionOptions{
		Bus:                b,
		MaxAgentIterations: cfg.MaxAgentIterations,
	}

	// Start session runtime with a worker registry.
	runtime := session.NewRuntime(ctx, sm, sessionOpts, b)
	runtime.Start()
	defer runtime.Close()

	// Create router
	rtr := gateway.NewRouter(b, runtime, sm, sessionOpts)
	rtr.Start(ctx)
	defer rtr.Stop()

	// Start scheduler if enabled and cron service is available
	if cfg.Scheduler.Enabled && cronService != nil {
		sched := scheduler.New(cronService, sm, sessionOpts, b, cfg.Scheduler)
		sched.Start(ctx)
		defer sched.Stop()
		logger.Info("scheduler enabled", logger.BuildMetadata("interval", cfg.Scheduler.CheckInterval))
	} else if cfg.Scheduler.Enabled && cronService == nil {
		logger.Warning("scheduler enabled but cron service failed to initialize", nil)
	}

	// Create and start gateway server
	gwCfg := gateway.Config{
		Host: cfg.GatewayHost,
		Port: cfg.GatewayPort,
		Path: cfg.GatewayPath,
	}
	gw := gateway.NewServer(gwCfg, rtr)

	logger.Info("gateway server starting",
		logger.BuildMetadata("url", cfg.GatewayWebSocketURL()))

	// Start gateway (blocks until shutdown)
	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("gateway server: %w", err)
	}

	if err := gw.Stop(); err != nil {
		return fmt.Errorf("stop gateway server: %w", err)
	}
	return nil
}

// runTUI starts the TUI client connecting to a gateway.
func runTUI(ctx context.Context, cfg config.Config, gatewayURL, identity string) error {
	// Use config URL if not provided via flag
	if gatewayURL == "" {
		gatewayURL = cfg.GatewayWebSocketURL()
	}

	// Resolve directories for logging
	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return fmt.Errorf("resolve valinor dir: %w", err)
	}

	// Initialize logger with file transport only (TUI has its own stdout display)
	logPath, closeLogger, err := logger.Init(valinorDir, []logger.TransportType{logger.TransportFile})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	// Set log prefix for TUI mode
	logger.SetPrefix("TUI")
	logger.Info("starting valinor in tui mode", nil)
	logger.Info("logging to file", logger.BuildMetadata("log_path", logPath))

	// Create WebSocket bridge first
	logger.Info("connecting to gateway", logger.BuildMetadata("url", gatewayURL, "identity", identity))
	wsBridge := tui.NewWSBridge(gatewayURL, identity)

	// Create TUI model with the bridge as command sender
	model := tui.New(wsBridge, cfg, cfg.DefaultProvider, "")

	// Create Bubble Tea program with the model
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Start the bridge with the program for message forwarding
	if err := wsBridge.Start(ctx, p); err != nil {
		return fmt.Errorf("connect to gateway: %w", err)
	}
	defer wsBridge.Stop()

	logger.Info("tui startup complete", logger.BuildMetadata("gateway", gatewayURL))

	// Run TUI
	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}

// runTelegram starts the Telegram bot mode connecting to a gateway.
func runTelegram(ctx context.Context, cfg config.Config) error {
	// Validate Telegram is enabled and configured
	if !cfg.Telegram.Enabled {
		return fmt.Errorf("telegram mode requires telegram.enabled = true in config")
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram mode requires telegram.bot_token in config")
	}

	// Resolve directories
	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return fmt.Errorf("resolve valinor dir: %w", err)
	}

	logPath, closeLogger, err := logger.Init(valinorDir, []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	// Set log prefix for Telegram mode
	logger.SetPrefix("TELEGRAM")
	logger.Info("starting valinor in telegram mode", nil)
	logger.Info("logging to file", logger.BuildMetadata("log_path", logPath))

	// Build gateway URL from config if not already set
	gatewayURL := cfg.GatewayWebSocketURL()
	logger.Info("connecting to gateway", logger.BuildMetadata("url", gatewayURL))

	// Create bot configuration
	botCfg := telegram.Config{
		BotToken:       cfg.Telegram.BotToken,
		GatewayURL:     gatewayURL,
		StorageDir:     valinorDir,
		PollTimeout:    cfg.Telegram.PollTimeout,
		RequireMention: cfg.Telegram.RequireMention,
	}

	// Create and start bot
	bot, err := telegram.NewBot(botCfg)
	if err != nil {
		return fmt.Errorf("create telegram bot: %w", err)
	}

	if err := bot.Start(ctx); err != nil {
		return fmt.Errorf("start telegram bot: %w", err)
	}
	defer bot.Stop()

	logger.Info("telegram bot started successfully", nil)

	// Wait for context cancellation (shutdown signal)
	<-ctx.Done()

	logger.Info("telegram bot shutting down", nil)
	return nil
}
