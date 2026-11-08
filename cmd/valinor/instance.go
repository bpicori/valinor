package main

import (
	"fmt"
	"os"
	"strings"

	"valinor/internal/config"
)

// selectedInstanceName returns --instance, VALINOR_INSTANCE, or default.
func selectedInstanceName() string {
	if name := strings.TrimSpace(instanceName); name != "" {
		return name
	}
	if name := strings.TrimSpace(os.Getenv("VALINOR_INSTANCE")); name != "" {
		return name
	}
	return config.DefaultInstanceName
}

func rejectConfigForService() error {
	if strings.TrimSpace(configPath) != "" {
		return fmt.Errorf("--config is not supported for service commands; use --instance")
	}
	return nil
}

func validateInstanceFlags() error {
	if strings.TrimSpace(instanceName) != "" && strings.TrimSpace(configPath) != "" {
		return fmt.Errorf("--instance and --config cannot be used together")
	}
	return nil
}

// loadSelectedConfig loads the config selected by --config or --instance.
func loadSelectedConfig() (config.Config, config.Instance, error) {
	if err := validateInstanceFlags(); err != nil {
		return config.Config{}, config.Instance{}, err
	}
	if path := strings.TrimSpace(configPath); path != "" {
		cfg, err := config.LoadFromPath(path)
		if err != nil {
			return config.Config{}, config.Instance{}, err
		}
		root, err := cfg.ResolveValinorDir()
		if err != nil {
			return config.Config{}, config.Instance{}, err
		}
		return cfg, config.Instance{Root: root, ConfigPath: path}, nil
	}

	name := selectedInstanceName()
	instance, err := config.ResolveInstance(name)
	if err != nil {
		return config.Config{}, config.Instance{}, err
	}
	cfg, err := config.LoadForInstance(name)
	if err != nil {
		return config.Config{}, config.Instance{}, err
	}
	return cfg, instance, nil
}
