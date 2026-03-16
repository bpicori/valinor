package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/gateway"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
	"valinor/internal/scheduler"
	"valinor/internal/session"
	"valinor/internal/telegram"
	"valinor/internal/tools"
	"valinor/internal/tui"
	"valinor/internal/version"

	tea "github.com/charmbracelet/bubbletea"
)

// gatewayDeps holds initialized gateway dependencies for reuse.
type gatewayDeps struct {
	valinorDir   string
	sessionsDir  string
	workspaceDir string
	cronDir      string
	closeLogger  func() error
	runtime      *sandbox.Runtime
	cronService  *cron.Service
	sm           *session.Manager
	sessionOpts  session.CreateSessionOptions
	rtr          *gateway.Router
	gw           *gateway.Server
}

var validModes = map[string]struct{}{
	"gateway":  {},
	"tui":      {},
	"telegram": {},
	"all":      {},
}

var validCommands = map[string]struct{}{
	"build-docker-image": {},
}

// cleanup releases all resources in reverse initialization order.
func (d *gatewayDeps) cleanup() {
	if d.rtr != nil {
		d.rtr.Stop()
	}
	if d.runtime != nil {
		d.runtime.Close()
	}
	if d.closeLogger != nil {
		d.closeLogger()
	}
}

func main() {
	flag.CommandLine.SetOutput(os.Stderr)
	flag.Usage = printUsage

	var (
		mode        = flag.String("mode", "", "Run mode: 'gateway', 'tui', 'telegram', or 'all'")
		gatewayURL  = flag.String("gateway-url", "", "Gateway WebSocket URL (for tui/telegram mode, defaults to config)")
		identity    = flag.String("identity", "default", "Client identity for session routing (tui mode)")
		showVersion = flag.Bool("version", false, "Print version information and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: load config.toml: %v\n", err)
		os.Exit(1)
	}

	command := strings.TrimSpace(flag.Arg(0))
	if command != "" {
		if err := validateCommand(command); err != nil {
			flag.Usage()
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			os.Exit(2)
		}
		if err := runCommand(command, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := validateMode(*mode); err != nil {
		flag.Usage()
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		os.Exit(2)
	}

	if err := run(*mode, *gatewayURL, *identity, cfg); err != nil {
		logger.Error("valinor exit error", logger.BuildMetadata("error", err))
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s -mode=<gateway|tui|telegram|all> [flags]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build-docker-image\n\n", os.Args[0])
	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintf(os.Stderr, "  %s -mode=tui\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -mode=gateway\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build-docker-image\n\n", os.Args[0])
	flag.PrintDefaults()
}

func validateMode(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return fmt.Errorf("-mode is required")
	}
	if _, ok := validModes[mode]; !ok {
		return fmt.Errorf("invalid mode %q: must be one of gateway, tui, telegram, or all", mode)
	}
	return nil
}

func validateCommand(command string) error {
	if _, ok := validCommands[command]; !ok {
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

func runCommand(command string, cfg config.Config) error {
	switch command {
	case "build-docker-image":
		image := cfg.SandboxImage
		fmt.Fprintf(os.Stdout, "Building sandbox image %q with Podman...\n", image)
		if err := sandbox.BuildImage(context.Background(), image, os.Stdout, os.Stderr); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Sandbox image %q built successfully.\n", image)
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func run(mode, gatewayURL, identity string, cfg config.Config) error {
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

	switch mode {
	case "gateway":
		return runGateway(ctx, cfg)
	case "tui":
		return runTUI(ctx, cfg, gatewayURL, identity)
	case "telegram":
		return runTelegram(ctx, cfg)
	case "all":
		return runAll(ctx, cfg)
	default:
		return fmt.Errorf("invalid mode '%s': must be 'gateway', 'tui', 'telegram', or 'all'", mode)
	}
}

// initGateway initializes all common gateway components without starting the server.
func initGateway(cfg config.Config, logPrefix string, transports []logger.TransportType) (*gatewayDeps, error) {
	// Resolve directories
	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return nil, fmt.Errorf("resolve valinor dir: %w", err)
	}
	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		return nil, fmt.Errorf("resolve session dir: %w", err)
	}
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		return nil, fmt.Errorf("resolve workspace dir: %w", err)
	}
	cronDir, err := cfg.ResolveCronDir()
	if err != nil {
		return nil, fmt.Errorf("resolve cron dir: %w", err)
	}

	// Ensure workspace directory exists (required for sandbox operations)
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return nil, fmt.Errorf("create workspace directory: %w", err)
	}

	// Initialize logger
	logPath, closeLogger, err := logger.Init(valinorDir, transports)
	if err != nil {
		return nil, fmt.Errorf("init logger: %w", err)
	}

	logger.SetPrefix(logPrefix)
	logger.Info("logging initialized", logger.BuildMetadata("log_path", logPath))

	// Create shared sandbox runtime
	rt, err := sandbox.NewRuntime(workspaceDir, cfg.SandboxImage)
	if err != nil {
		closeLogger()
		return nil, fmt.Errorf("create sandbox runtime: %w", err)
	}
	logger.Info("sandbox runtime initialized", logger.BuildMetadata("workspace", workspaceDir, "image", cfg.SandboxImage))

	// Initialize cron service
	cronService, err := cron.NewService(cronDir)
	if err != nil {
		logger.Error("failed to create cron service, continuing without scheduling", logger.BuildMetadata("error", err))
		cronService = nil
	} else {
		tools.SetCronService(cronService)
		logger.Info("cron service initialized", logger.BuildMetadata("dir", cronDir))
	}

	// Initialize session manager
	sm, err := session.NewManager(sessionsDir, workspaceDir, cfg, rt)
	if err != nil {
		rt.Close()
		closeLogger()
		return nil, fmt.Errorf("create session manager: %w", err)
	}

	sessionOpts := session.CreateSessionOptions{
		MaxAgentIterations: cfg.MaxAgentIterations,
	}

	return &gatewayDeps{
		valinorDir:   valinorDir,
		sessionsDir:  sessionsDir,
		workspaceDir: workspaceDir,
		cronDir:      cronDir,
		closeLogger:  closeLogger,
		runtime:      rt,
		cronService:  cronService,
		sm:           sm,
		sessionOpts:  sessionOpts,
	}, nil
}

// runGateway starts the gateway and session core (no TUI).
func runGateway(ctx context.Context, cfg config.Config) error {
	deps, err := initGateway(cfg, "GATEWAY", []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return err
	}
	defer deps.cleanup()

	logger.Info("starting valinor in gateway mode", nil)

	// Start session runtime
	runtime := session.NewRuntime(ctx, deps.sm, deps.sessionOpts)
	runtime.Start()
	defer runtime.Close()

	// Create and start router
	rtr := gateway.NewRouter(runtime, deps.sm, deps.sessionOpts)
	rtr.Start(ctx)
	deps.rtr = rtr

	// Start scheduler if enabled
	if cfg.Scheduler.Enabled && deps.cronService != nil {
		sched := scheduler.New(deps.cronService, deps.sm, deps.sessionOpts, cfg.Scheduler)
		sched.Start(ctx)
		defer sched.Stop()
		logger.Info("scheduler enabled", logger.BuildMetadata("interval", cfg.Scheduler.CheckInterval))
	} else if cfg.Scheduler.Enabled && deps.cronService == nil {
		logger.Warning("scheduler enabled but cron service failed to initialize", nil)
	}

	// Create and start gateway server
	gwCfg := gateway.Config{
		Host: cfg.GatewayHost,
		Port: cfg.GatewayPort,
		Path: cfg.GatewayPath,
	}
	gw := gateway.NewServer(gwCfg, rtr)
	deps.gw = gw

	logger.Info("gateway server starting", logger.BuildMetadata("url", cfg.GatewayWebSocketURL()))

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
		BotToken:    cfg.Telegram.BotToken,
		GatewayURL:  gatewayURL,
		StorageDir:  valinorDir,
		PollTimeout: cfg.Telegram.PollTimeout,
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

// runAll starts both gateway and Telegram services in one process.
func runAll(ctx context.Context, cfg config.Config) error {
	// Validate Telegram is enabled and configured
	if !cfg.Telegram.Enabled {
		return fmt.Errorf("telegram mode requires telegram.enabled = true in config")
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram mode requires telegram.bot_token in config")
	}

	// Initialize gateway components
	deps, err := initGateway(cfg, "ALL", []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return err
	}

	logger.Info("starting valinor in all mode (gateway + telegram)", nil)

	// Start session runtime
	runtime := session.NewRuntime(ctx, deps.sm, deps.sessionOpts)
	runtime.Start()
	defer runtime.Close()

	// Create and start router
	rtr := gateway.NewRouter(runtime, deps.sm, deps.sessionOpts)
	rtr.Start(ctx)
	deps.rtr = rtr

	// Start scheduler if enabled
	if cfg.Scheduler.Enabled && deps.cronService != nil {
		sched := scheduler.New(deps.cronService, deps.sm, deps.sessionOpts, cfg.Scheduler)
		sched.Start(ctx)
		defer sched.Stop()
		logger.Info("scheduler enabled", logger.BuildMetadata("interval", cfg.Scheduler.CheckInterval))
	} else if cfg.Scheduler.Enabled && deps.cronService == nil {
		logger.Warning("scheduler enabled but cron service failed to initialize", nil)
	}

	// Create gateway server
	gwCfg := gateway.Config{
		Host: cfg.GatewayHost,
		Port: cfg.GatewayPort,
		Path: cfg.GatewayPath,
	}
	gw := gateway.NewServer(gwCfg, rtr)
	deps.gw = gw

	logger.Info("gateway server starting", logger.BuildMetadata("url", cfg.GatewayWebSocketURL()))

	// Start gateway in a goroutine
	gwErrCh := make(chan error, 1)
	go func() {
		if err := gw.Start(ctx); err != nil {
			gwErrCh <- fmt.Errorf("gateway server: %w", err)
			return
		}
		gwErrCh <- nil
	}()

	// Create and start Telegram bot
	gatewayURL := cfg.GatewayWebSocketURL()
	logger.Info("connecting telegram to gateway", logger.BuildMetadata("url", gatewayURL))

	botCfg := telegram.Config{
		BotToken:    cfg.Telegram.BotToken,
		GatewayURL:  gatewayURL,
		StorageDir:  deps.valinorDir,
		PollTimeout: cfg.Telegram.PollTimeout,
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

	// Wait for context cancellation or gateway error
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

	// Graceful shutdown
	bot.Stop()
	if err := gw.Stop(); err != nil {
		deps.cleanup()
		return fmt.Errorf("stop gateway server: %w", err)
	}

	deps.cleanup()
	logger.Info("all services stopped", nil)
	return nil
}
