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
		mode        = flag.String("mode", "tui", "Run mode: 'gateway' or 'tui'")
		gatewayURL  = flag.String("gateway-url", "", "Gateway WebSocket URL (for tui mode, defaults to config)")
	)
	flag.Parse()

	if err := run(*mode, *gatewayURL); err != nil {
		applog.Error("valinor exit error", applog.M("error", err))
		os.Exit(1)
	}
}

func run(mode, gatewayURL string) error {
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
		applog.Error("load config failed", applog.M("error", err))
		return err
	}

	switch mode {
	case "gateway":
		return runGateway(ctx, cfg)
	case "tui":
		return runTUI(ctx, cfg, gatewayURL)
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
	applog.Info("logging to file and stdout", applog.M("log_path", logPath))

	// Initialize core components
	b := bus.New()
	sm := session.NewManager(sessionsDir, workspaceDir, cfg)

	// Create initial session
	createCfg := session.CreateOptions{
		Bus:                b,
		MaxAgentIterations: cfg.MaxAgentIterations,
	}

	var sess *session.Session
	sessions, err := sm.List()
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) > 0 {
		latestSession := sessions[0]
		sess, err = sm.Resume(context.Background(), latestSession.ID, createCfg)
		if err != nil {
			applog.Warning("resume latest session failed", applog.M("session_id", latestSession.ID, "error", err))
		} else {
			applog.Info("resumed session", applog.M("session_id", latestSession.ID))
		}
	}
	if sess == nil {
		sess, err = sm.Create(context.Background(), createCfg)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		applog.Info("created new session", applog.M("session_id", sess.Meta().ID))
	}

	// Start session runtime
	runtime := session.NewRuntime(ctx, sm, createCfg, sess, b, nil)
	runtime.Start()
	defer runtime.Close()

	// Create identity map store
	identityMap, err := session.NewIdentityMapStore(valinorDir)
	if err != nil {
		applog.Warning("failed to create identity map store, continuing without persistence",
			applog.M("error", err))
		identityMap = nil
	}

	// Create router
	rtr := router.NewRouter(b, runtime, sm, createCfg, identityMap)
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
		applog.M("url", cfg.GatewayWebSocketURL()))

	// Start gateway (blocks until shutdown)
	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("gateway server: %w", err)
	}

	applog.Info("gateway server stopped", nil)
	return nil
}

// runTUI starts the TUI client connecting to a gateway.
func runTUI(ctx context.Context, cfg config.Config, gatewayURL string) error {
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
	applog.Info("logging to file", applog.M("log_path", logPath))

	// Create TUI model and program
	// Note: We still need bus for now because tui.Model expects it
	// In a future refactoring, we can remove this dependency
	b := bus.New()
	model := tui.New(b, cfg, cfg.DefaultProvider, "")
	p := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Connect TUI via WebSocket to gateway
	applog.Info("connecting to gateway", applog.M("url", gatewayURL))

	wsBridge := tui.NewWSBridge(gatewayURL)
	if err := wsBridge.Start(ctx, p); err != nil {
		return fmt.Errorf("connect to gateway: %w", err)
	}
	defer wsBridge.Stop()

	applog.Info("tui startup complete", applog.M("gateway", gatewayURL))

	// Run TUI
	if _, err := p.Run(); err != nil {
		return err
	}

	return nil
}
