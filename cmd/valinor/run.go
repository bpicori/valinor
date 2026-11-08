package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"valinor/internal/config"
	"valinor/internal/logger"
	"valinor/internal/wizard"

	"github.com/spf13/cobra"
)

var (
	runMode       string
	runGatewayURL string
	runIdentity   string
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start a Valinor instance (gateway plus configured clients)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateMode(runMode); err != nil {
			return err
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if err := config.Init(cfg); err != nil {
			return fmt.Errorf("init config: %w", err)
		}

		if err := runRuntime(runMode, runGatewayURL, runIdentity); err != nil {
			logger.Error("valinor exit error", logger.BuildMetadata("error", err))
			return err
		}
		return nil
	},
}

func init() {
	runCmd.Flags().StringVar(&runMode, "mode", "auto", "Run mode: auto, gateway, tui, telegram, or all")
	runCmd.Flags().StringVar(&runGatewayURL, "gateway-url", "", "Gateway WebSocket URL (for tui/telegram mode, defaults to config)")
	runCmd.Flags().StringVar(&runIdentity, "identity", "default", "Client identity for session routing (tui mode)")
}

var validModes = map[string]struct{}{
	"auto":     {},
	"gateway":  {},
	"tui":      {},
	"telegram": {},
	"all":      {},
}

func validateMode(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "auto"
	}
	if _, ok := validModes[mode]; !ok {
		return fmt.Errorf("invalid mode %q: must be one of auto, gateway, tui, telegram, or all", mode)
	}
	return nil
}

func loadConfig() (config.Config, error) {
	cfg, _, err := loadSelectedConfig()
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, config.ErrNotFound) {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	if strings.TrimSpace(configPath) != "" {
		return config.Config{}, fmt.Errorf("config file not found: %s", configPath)
	}

	name := selectedInstanceName()
	instance, err := config.ResolveInstance(name)
	if err != nil {
		return config.Config{}, err
	}
	fmt.Fprintf(os.Stderr, "No configuration found at %s.\n", instance.ConfigPath)
	fmt.Fprint(os.Stderr, "Run setup wizard? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	answer, readErr := reader.ReadString('\n')
	if readErr != nil {
		return config.Config{}, fmt.Errorf("read setup prompt: %w", readErr)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "n" || answer == "no" {
		return config.Config{}, fmt.Errorf("no configuration found; run `valinor init --instance %s` to set up", name)
	}

	initial, err := config.LoadWizardTemplateForInstance(name)
	if err != nil {
		return config.Config{}, fmt.Errorf("load config template: %w", err)
	}
	if _, err := wizard.Run(initial, instance.ConfigPath); err != nil {
		return config.Config{}, err
	}
	return config.LoadForInstance(name)
}
