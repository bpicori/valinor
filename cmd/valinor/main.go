package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	agentcontext "valinor/internal/context"
	"valinor/internal/buildinfo"
	"valinor/internal/config"
	"valinor/internal/cron"
	"valinor/internal/gateway"
	"valinor/internal/llm/factory"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
	"valinor/internal/scheduler"
	"valinor/internal/session"
	"valinor/internal/telegram"
	"valinor/internal/tui"
	"valinor/internal/wizard"
	"valinor/internal/requiredskills"

	tea "github.com/charmbracelet/bubbletea"
)

// gatewayDeps holds initialized gateway dependencies for reuse.
type gatewayDeps struct {
	closeLogger func() error
	cronJobs    *cron.Jobs
	sm          *session.Manager
	rtr         *gateway.Router
	gw          *gateway.Server
}

var validModes = map[string]struct{}{
	"gateway":  {},
	"tui":      {},
	"telegram": {},
	"all":      {},
}

var validCommands = map[string]struct{}{
	"build-docker-image": {},
	"init":               {},
}

// cleanup releases all resources in reverse initialization order.
func (d *gatewayDeps) cleanup() {
	if d.rtr != nil {
		d.rtr.Stop()
	}
	if err := sandbox.CloseRuntime(); err != nil {
		logger.Error("sandbox runtime close error", logger.BuildMetadata("error", err))
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
		fmt.Println(versionString())
		return
	}

	command := strings.TrimSpace(flag.Arg(0))
	if command == "init" {
		if err := runInit(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := config.Init(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: init config: %v\n", err)
		os.Exit(1)
	}

	if command != "" {
		if err := validateCommand(command); err != nil {
			flag.Usage()
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			os.Exit(2)
		}
		if err := runCommand(command); err != nil {
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

	if err := run(*mode, *gatewayURL, *identity); err != nil {
		logger.Error("valinor exit error", logger.BuildMetadata("error", err))
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s -mode=<gateway|tui|telegram|all> [flags]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s init\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build-docker-image\n\n", os.Args[0])
	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintf(os.Stderr, "  %s init\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -mode=tui\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -mode=gateway\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s build-docker-image\n\n", os.Args[0])
	flag.PrintDefaults()
}

func runInit() error {
	initial, err := config.LoadInitial()
	if err != nil {
		return fmt.Errorf("load initial config: %w", err)
	}
	if _, err := wizard.Run(initial); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Configuration saved to ~/.valinor/config.toml")
	return nil
}

func loadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, config.ErrNotFound) {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}

	fmt.Fprint(os.Stderr, "No configuration found at ~/.valinor/config.toml.\n")
	fmt.Fprint(os.Stderr, "Run setup wizard? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, readErr := reader.ReadString('\n')
	if readErr != nil {
		return config.Config{}, fmt.Errorf("read setup prompt: %w", readErr)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "n" || answer == "no" {
		return config.Config{}, fmt.Errorf("no configuration found; run `valinor init` to set up")
	}

	initial, err := config.LoadWizardTemplate()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config template: %w", err)
	}
	if _, err := wizard.Run(initial); err != nil {
		return config.Config{}, err
	}
	return config.Load()
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

func runCommand(command string) error {
	switch command {
	case "build-docker-image":
		image := buildinfo.SandboxImage()
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

func run(mode, gatewayURL, identity string) error {
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
	default:
		return fmt.Errorf("invalid mode '%s': must be 'gateway', 'tui', 'telegram', or 'all'", mode)
	}
}

// initGateway initializes all common gateway components without starting the server.
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

// runGateway starts the gateway and session core (no TUI).
func runGateway(ctx context.Context) error {
	cfg := config.Must()
	deps, err := initGateway("GATEWAY", []logger.TransportType{logger.TransportFile, logger.TransportStdout})
	if err != nil {
		return err
	}
	defer deps.cleanup()

	logger.Info("starting valinor in gateway mode", nil)

	runtime := session.NewRuntime(ctx, deps.sm)
	runtime.Start()
	defer runtime.Close()

	rtr := gateway.NewRouter(runtime, deps.sm)
	rtr.Start(ctx)
	deps.rtr = rtr

	if cfg.Scheduler.Enabled && deps.cronJobs != nil {
		sched := scheduler.New(deps.cronJobs, deps.sm)
		sched.Start(ctx)
		defer sched.Stop()
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

	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("gateway server: %w", err)
	}

	if err := gw.Stop(); err != nil {
		return fmt.Errorf("stop gateway server: %w", err)
	}

	return nil
}

// runTUI starts the TUI client connecting to a gateway.
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

// runTelegram starts the Telegram bot mode connecting to a gateway.
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

// runAll starts both gateway and Telegram services in one process.
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

	runtime := session.NewRuntime(ctx, deps.sm)
	runtime.Start()
	defer runtime.Close()

	rtr := gateway.NewRouter(runtime, deps.sm)
	rtr.Start(ctx)
	deps.rtr = rtr

	if cfg.Scheduler.Enabled && deps.cronJobs != nil {
		sched := scheduler.New(deps.cronJobs, deps.sm)
		sched.Start(ctx)
		defer sched.Stop()
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