package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"valinor/internal/applog"
	"valinor/internal/bus"
	"valinor/internal/config"
	"valinor/internal/gateway"
	"valinor/internal/router"
	"valinor/internal/session"
	"valinor/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Define command-line flags
	var (
		mode       = flag.String("mode", "tui", "Run mode: 'gateway' or 'tui'")
		gatewayURL = flag.String("gateway-url", "", "Gateway WebSocket URL (for tui mode, defaults to config)")
		identity   = flag.String("identity", "default", "Client identity for session routing (tui mode)")
	)
	flag.Parse()

	if err := run(*mode, *gatewayURL, *identity); err != nil {
		applog.Error("valinor exit error", applog.BuildMetadata("error", err))
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
		applog.Info("shutdown signal received", nil)
		cancel()
	}()

	// Load configuration (needed by both modes)
	cfg, err := config.LoadFromFile("config.toml")
	if err != nil {
		applog.Error("load config failed", applog.BuildMetadata("error", err))
		return err
	}

	switch mode {
	case "gateway":
		return runGateway(ctx, cfg)
	case "tui":
		return runTUI(ctx, cfg, gatewayURL, identity)
	default:
		return fmt.Errorf("invalid mode '%s': must be 'gateway' or 'tui'", mode)
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

	// Initialize logger with both file and stdout transports
	logPath, closeLogger, err := applog.Init(valinorDir, []applog.TransportType{applog.TransportFile, applog.TransportStdout})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	// Set log prefix for gateway mode
	applog.SetPrefix("GATEWAY")
	applog.Info("starting valinor in gateway mode", nil)
	applog.Info("logging to file and stdout", applog.BuildMetadata("log_path", logPath))

	// Initialize core components
	b := bus.New()
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

	// Create identity map store
	identityMap, err := session.NewIdentityMapStore(valinorDir)
	if err != nil {
		applog.Warning("failed to create identity map store, continuing without persistence",
			applog.BuildMetadata("error", err))
		identityMap = nil
	}

	// Create router
	rtr := router.NewRouter(b, runtime, sm, sessionOpts, identityMap)
	rtr.Start(ctx)
	defer rtr.Stop()

	// Create and start gateway server
	gwCfg := gateway.Config{
		Host: cfg.GatewayHost,
		Port: cfg.GatewayPort,
		Path: cfg.GatewayPath,
	}
	gw := gateway.NewServer(gwCfg, rtr)

	applog.Info("gateway server starting",
		applog.BuildMetadata("url", cfg.GatewayWebSocketURL()))

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
	logPath, closeLogger, err := applog.Init(valinorDir, []applog.TransportType{applog.TransportFile})
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer closeLogger()

	// Set log prefix for TUI mode
	applog.SetPrefix("TUI")
	applog.Info("starting valinor in tui mode", nil)
	applog.Info("logging to file", applog.BuildMetadata("log_path", logPath))

	// Create WebSocket bridge first
	applog.Info("connecting to gateway", applog.BuildMetadata("url", gatewayURL, "identity", identity))
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

	applog.Info("tui startup complete", applog.BuildMetadata("gateway", gatewayURL))

	// Run TUI
	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}
