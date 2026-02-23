package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"valinor/internal/applog"
)

// DefaultValinorDir is the default base directory under home.
const DefaultValinorDir = "~/.valinor"

// DefaultMaxAgentIterations is the default cap on agent tool-call iterations per user message.
const DefaultMaxAgentIterations = 20

// Default web server configuration.
const (
	DefaultWebEnabled = true
	DefaultWebHost    = "127.0.0.1"
	DefaultWebPort    = 7777
)

// Default gateway configuration.
const (
	DefaultGatewayEnabled = true
	DefaultGatewayHost    = "127.0.0.1"
	DefaultGatewayPort    = 7778
	DefaultGatewayPath    = "/ws"
)

// Config holds provider configurations and application paths.
type Config struct {
	DefaultProvider    string                    `toml:"default_provider"`
	Providers          map[string]ProviderConfig `toml:"providers"`
	ValinorDir         string                    `toml:"valinor_dir"`
	SessionDir         string                    `toml:"session_dir"`
	WorkspaceDir       string                    `toml:"workspace_dir"`
	MaxAgentIterations int                       `toml:"max_agent_iterations"`
	WebEnabled         bool                      `toml:"web_enabled"`
	WebHost            string                    `toml:"web_host"`
	WebPort            int                       `toml:"web_port"`

	// Gateway configuration for WebSocket client connections.
	GatewayEnabled bool   `toml:"gateway_enabled"`
	GatewayHost    string `toml:"gateway_host"`
	GatewayPort    int    `toml:"gateway_port"`
	GatewayPath    string `toml:"gateway_path"`
}

// ProviderConfig holds configuration for a specific LLM provider.
type ProviderConfig struct {
	Type         string `toml:"type"`          // "openai", "anthropic", "gemini", "openai-compatible"
	APIKey       string `toml:"api_key"`       // API key for the provider
	BaseURL      string `toml:"base_url"`      // Base URL (for openai-compatible providers)
	DefaultModel string `toml:"default_model"` // Default model for new sessions (bootstrap fallback)
}

// LoadFromFile loads configuration from a TOML file.
func LoadFromFile(path string) (Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		applog.Error("config load failed", applog.M("path", path, "error", err))
		return Config{}, err
	}

	// Validate default provider
	if cfg.DefaultProvider == "" {
		return Config{}, fmt.Errorf("default_provider is required")
	}
	if len(cfg.Providers) == 0 {
		return Config{}, fmt.Errorf("at least one provider must be configured")
	}
	if _, ok := cfg.Providers[cfg.DefaultProvider]; !ok {
		return Config{}, fmt.Errorf("default_provider '%s' not found in providers", cfg.DefaultProvider)
	}

	// Validate each provider
	for name, provider := range cfg.Providers {
		if provider.Type == "" {
			return Config{}, fmt.Errorf("provider '%s' missing type field", name)
		}
		if provider.Type != "openai" && provider.Type != "anthropic" && provider.Type != "gemini" && provider.Type != "openai-compatible" {
			return Config{}, fmt.Errorf("provider '%s' has invalid type '%s' (must be: openai, anthropic, gemini, or openai-compatible)", name, provider.Type)
		}
		if provider.APIKey == "" {
			return Config{}, fmt.Errorf("provider '%s' missing api_key field", name)
		}
		if provider.DefaultModel == "" {
			return Config{}, fmt.Errorf("provider '%s' missing default_model field", name)
		}
		cfg.Providers[name] = provider
		if provider.Type == "openai-compatible" && provider.BaseURL == "" {
			return Config{}, fmt.Errorf("provider '%s' of type 'openai-compatible' requires base_url field", name)
		}
	}

	// Apply defaults
	if cfg.ValinorDir == "" {
		cfg.ValinorDir = DefaultValinorDir
	}
	if cfg.MaxAgentIterations <= 0 {
		cfg.MaxAgentIterations = DefaultMaxAgentIterations
	}
	if cfg.WebHost == "" {
		cfg.WebHost = DefaultWebHost
	}
	if cfg.WebPort <= 0 {
		cfg.WebPort = DefaultWebPort
	}
	if !md.IsDefined("web_enabled") {
		cfg.WebEnabled = DefaultWebEnabled
	}

	// Apply gateway defaults
	if cfg.GatewayHost == "" {
		cfg.GatewayHost = DefaultGatewayHost
	}
	if cfg.GatewayPort <= 0 {
		cfg.GatewayPort = DefaultGatewayPort
	}
	if cfg.GatewayPath == "" {
		cfg.GatewayPath = DefaultGatewayPath
	}
	if !md.IsDefined("gateway_enabled") {
		cfg.GatewayEnabled = DefaultGatewayEnabled
	}

	return cfg, nil
}

// ResolveValinorDir returns the absolute base directory for Valinor data.
func (c Config) ResolveValinorDir() (string, error) {
	dir := c.ValinorDir
	if dir == "" {
		dir = DefaultValinorDir
	}
	return resolvePathWithTilde(dir)
}

// ResolveSessionDir returns the absolute path for the sessions directory
func (c Config) ResolveSessionDir() (string, error) {
	if c.SessionDir != "" {
		return resolvePathWithTilde(c.SessionDir)
	}
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "sessions"), nil
}

// ResolveWorkspaceDir returns the absolute path for the global tool workspace
func (c Config) ResolveWorkspaceDir() (string, error) {
	if c.WorkspaceDir != "" {
		return resolvePathWithTilde(c.WorkspaceDir)
	}
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "workspace"), nil
}

// ResolveProviderModel resolves provider/model with config defaults.
func (c Config) ResolveProviderModel(provider, model string) (string, string, error) {
	resolvedProvider := provider
	if resolvedProvider == "" {
		resolvedProvider = c.DefaultProvider
	}

	providerCfg, ok := c.Providers[resolvedProvider]
	if !ok {
		return "", "", fmt.Errorf("provider '%s' not found in config", resolvedProvider)
	}

	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = providerCfg.DefaultModel
	}
	if resolvedModel == "" {
		return "", "", fmt.Errorf("provider and model are required")
	}

	return resolvedProvider, resolvedModel, nil
}

// ResolveLogsDir returns the absolute path for application logs.
func (c Config) ResolveLogsDir() (string, error) {
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "logs"), nil
}

// GatewayWebSocketURL returns the WebSocket URL for the gateway.
func (c Config) GatewayWebSocketURL() string {
	return fmt.Sprintf("ws://%s:%d%s", c.GatewayHost, c.GatewayPort, c.GatewayPath)
}

// GatewayHTTPURL returns the HTTP URL for the gateway health endpoint.
func (c Config) GatewayHTTPURL() string {
	return fmt.Sprintf("http://%s:%d/health", c.GatewayHost, c.GatewayPort)
}

// resolvePathWithTilde returns an absolute path.
// If the path starts with ~, it is expanded to the user's home directory.
// Otherwise relative paths are resolved from the current working directory.
func resolvePathWithTilde(path string) (string, error) {
	if len(path) > 1 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			applog.Error("config path expand error", applog.M("input", path, "error", err))
			return "", err
		}
		path = filepath.Join(home, path[2:])
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			applog.Error("config path expand error", applog.M("input", path, "error", err))
			return "", err
		}
		return home, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		applog.Error("config path expand error", applog.M("input", path, "error", err))
		return "", err
	}
	return abs, nil
}
