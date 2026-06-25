package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"valinor/internal/logger"
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

// Default Telegram configuration.
const (
	DefaultTelegramPollTimeout    = 30
	DefaultTelegramRetryBaseDelay = 1  // seconds
	DefaultTelegramRetryMaxDelay  = 30 // seconds
	DefaultTelegramRequireMention = false
)

// Default scheduler configuration.
const (
	DefaultSchedulerEnabled       = false
	DefaultSchedulerScriptTimeout = "120s"
)

// Default sandbox configuration.
const (
	DefaultSandboxImage = "valinor-sandbox:latest"
)

// Default context compression configuration.
const DefaultPreserveRecentMessages = 20

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

	// Telegram bot configuration.
	Telegram TelegramConfig `toml:"telegram"`

	// Scheduler configuration for Cron.md based scheduled jobs.
	Scheduler SchedulerConfig `toml:"scheduler"`

	// SandboxImage is the container image used for the sandbox runtime (default: "valinor-sandbox:latest").
	SandboxImage string `toml:"sandbox_image"`

	// Context holds context-window compression settings.
	Context ContextConfig `toml:"context"`
}

// ContextConfig holds context compression settings.
type ContextConfig struct {
	PreserveRecentMessages int `toml:"preserve_recent_messages"`
}

// TelegramConfig holds Telegram Bot API configuration.
type TelegramConfig struct {
	// Enabled enables the Telegram bot mode (default: false).
	Enabled bool `toml:"enabled"`
	// BotToken is the Telegram Bot API token from @BotFather (required if enabled).
	BotToken string `toml:"bot_token"`
	// PollTimeout is the long-polling timeout in seconds (default: 30, max: 90).
	PollTimeout int `toml:"poll_timeout"`
	// RetryBaseDelay is the base delay for exponential backoff in seconds (default: 1).
	RetryBaseDelay int `toml:"retry_base_delay"`
	// RetryMaxDelay is the maximum retry delay in seconds (default: 30).
	RetryMaxDelay int `toml:"retry_max_delay"`
	// RequireMention requires the bot to be mentioned in group chats (default: false).
	RequireMention bool `toml:"require_mention"`
}

// SchedulerConfig holds scheduled job (cron) configuration.
type SchedulerConfig struct {
	// Enabled enables the scheduled job runner (default: false).
	Enabled bool `toml:"enabled"`
	// ScriptsDir overrides the no_agent scripts directory (default: <workspace>/scripts).
	ScriptsDir string `toml:"scripts_dir"`
	// ScriptTimeout is the max runtime for no_agent scripts (default: "120s").
	ScriptTimeout string `toml:"script_timeout"`
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
		logger.Error("config load failed", logger.BuildMetadata("path", path, "error", err))
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

	// Validate and apply Telegram defaults
	if cfg.Telegram.Enabled {
		if cfg.Telegram.BotToken == "" {
			return Config{}, fmt.Errorf("telegram bot_token is required when telegram is enabled")
		}
	}
	if cfg.Telegram.PollTimeout <= 0 {
		cfg.Telegram.PollTimeout = DefaultTelegramPollTimeout
	}
	if cfg.Telegram.PollTimeout > 90 {
		cfg.Telegram.PollTimeout = 90 // Telegram API maximum
	}
	if cfg.Telegram.RetryBaseDelay <= 0 {
		cfg.Telegram.RetryBaseDelay = DefaultTelegramRetryBaseDelay
	}
	if cfg.Telegram.RetryMaxDelay <= 0 {
		cfg.Telegram.RetryMaxDelay = DefaultTelegramRetryMaxDelay
	}

	// Apply scheduler defaults
	if cfg.Scheduler.ScriptTimeout == "" {
		cfg.Scheduler.ScriptTimeout = DefaultSchedulerScriptTimeout
	}

	// Apply sandbox defaults
	if cfg.SandboxImage == "" {
		cfg.SandboxImage = DefaultSandboxImage
	}

	// Apply context compression defaults
	if cfg.Context.PreserveRecentMessages <= 0 {
		cfg.Context.PreserveRecentMessages = DefaultPreserveRecentMessages
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

// ResolveCronDir returns the absolute path for cron job storage.
func (c Config) ResolveCronDir() (string, error) {
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "cron"), nil
}

// CronScriptsSubdir is the workspace-relative directory for no_agent cron scripts.
const CronScriptsSubdir = "scripts"

// ResolveScriptsDir returns the absolute path for no_agent cron scripts.
// Defaults to <workspace>/scripts so the agent can create scripts via write_file.
func (c Config) ResolveScriptsDir() (string, error) {
	if c.Scheduler.ScriptsDir != "" {
		return resolvePathWithTilde(c.Scheduler.ScriptsDir)
	}
	workspaceDir, err := c.ResolveWorkspaceDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(workspaceDir, CronScriptsSubdir), nil
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
			logger.Error("config path expand error", logger.BuildMetadata("input", path, "error", err))
			return "", err
		}
		path = filepath.Join(home, path[2:])
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.Error("config path expand error", logger.BuildMetadata("input", path, "error", err))
			return "", err
		}
		return home, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		logger.Error("config path expand error", logger.BuildMetadata("input", path, "error", err))
		return "", err
	}
	return abs, nil
}
