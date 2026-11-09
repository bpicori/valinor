package config

import (
	"fmt"
	"os"
	"path/filepath"

	"valinor/internal/logger"
)

// DefaultValinorDir is the default base directory under home.
const DefaultValinorDir = "~/.valinor"

// DefaultMaxAgentIterations is the default cap on agent tool-call iterations per user message.
const DefaultMaxAgentIterations = 20

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
	DefaultTelegramRequireMention = true
)

// Default scheduler configuration.
const (
	DefaultSchedulerEnabled       = true
	DefaultSchedulerScriptTimeout = "120s"
)

// Default search configuration.
const DefaultSearchMaxResults = 10

// Default context compression configuration.
const DefaultPreserveRecentMessages = 20

// Config holds provider configurations and application paths.
type Config struct {
	DefaultProvider    string                    `toml:"default_provider"`
	Providers          map[string]ProviderConfig `toml:"providers"`
	ValinorDir         string                    `toml:"valinor_dir"`
	MaxAgentIterations int                       `toml:"max_agent_iterations"`

	// Gateway configuration for WebSocket client connections.
	GatewayEnabled bool   `toml:"gateway_enabled"`
	GatewayHost    string `toml:"gateway_host"`
	GatewayPort    int    `toml:"gateway_port"`
	GatewayPath    string `toml:"gateway_path"`

	// Telegram bot configuration.
	Telegram TelegramConfig `toml:"telegram"`

	// Scheduler configuration for Cron.md based scheduled jobs.
	Scheduler SchedulerConfig `toml:"scheduler"`

	// Dreaming configures offline memory consolidation from past sessions.
	Dreaming DreamingConfig `toml:"dreaming"`

	// Search configures the web_search and web_fetch tools.
	Search SearchConfig `toml:"search"`

	// Context holds context-window compression settings.
	Context ContextConfig `toml:"context"`
}

// SearchConfig configures the web search provider.
// An empty provider disables the web_search and web_fetch tools.
type SearchConfig struct {
	// Provider selects the search backend (supported: "deepseek").
	Provider string `toml:"provider"`
	// APIKey authenticates the provider; falls back to the LLM provider entry
	// with the same name.
	APIKey string `toml:"api_key"`
	// BaseURL overrides the provider default endpoint.
	BaseURL string `toml:"base_url"`
	// MaxResults is the default number of results (1-50).
	MaxResults int `toml:"max_results"`
}

// Default context window and output settings.
const (
	DefaultContextWindowTokens = 200_000
	DefaultMaxOutputTokens     = 8192
)

// ContextConfig holds context window and compression settings.
type ContextConfig struct {
	// PreserveRecentMessages is the number of tail messages kept verbatim during compression.
	PreserveRecentMessages int `toml:"preserve_recent_messages"`
	// WindowTokens is the model context window used for compression thresholds.
	WindowTokens int `toml:"window_tokens"`
	// MaxOutputTokens caps the model's response length per call.
	MaxOutputTokens int `toml:"max_output_tokens"`
}

// TelegramConfig holds Telegram Bot API configuration.
type TelegramConfig struct {
	// Enabled enables the Telegram bot mode (default: false).
	Enabled bool `toml:"enabled"`
	// BotToken is the Telegram Bot API token from @BotFather (required if enabled).
	BotToken string `toml:"bot_token"`
	// AllowedUserIDs lists the Telegram user IDs allowed to use the bot.
	// An empty list denies every user.
	AllowedUserIDs []int64 `toml:"allowed_user_ids"`
	// AllowedChatIDs lists group/supergroup chat IDs the bot may respond in.
	// DMs do not need a chat ID. An empty list denies every group.
	AllowedChatIDs []int64 `toml:"allowed_chat_ids"`
	// PollTimeout is the long-polling timeout in seconds (default: 30, max: 90).
	PollTimeout int `toml:"poll_timeout"`
	// RetryBaseDelay is the base delay for exponential backoff in seconds (default: 1).
	RetryBaseDelay int `toml:"retry_base_delay"`
	// RetryMaxDelay is the maximum retry delay in seconds (default: 30).
	RetryMaxDelay int `toml:"retry_max_delay"`
	// RequireMention requires the bot to be mentioned on text messages in group chats (default: true).
	// Files from allowed users are kept and attached to the next instructed message.
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

// DreamingConfig holds offline memory consolidation settings.
// The only user-facing switch is enabled; consolidation limits and cadence are
// internal to the dreaming runtime.
type DreamingConfig struct {
	// Enabled runs offline memory consolidation while the instance is idle.
	Enabled bool `toml:"enabled"`
}

// ProviderConfig holds configuration for a specific LLM provider.
type ProviderConfig struct {
	Type         string `toml:"type"`          // "openai", "anthropic", "gemini", "openai-compatible"
	APIKey       string `toml:"api_key"`       // API key for the provider
	BaseURL      string `toml:"base_url"`      // Base URL (for openai-compatible providers)
	DefaultModel string `toml:"default_model"` // Default model for new sessions (bootstrap fallback)
	// Thinking enables or disables provider-side reasoning when supported.
	// Values: "", "enabled", "disabled". Empty leaves the provider default.
	Thinking string `toml:"thinking"`

	// TranscriptionEnabled enables Telegram voice message transcription via this provider.
	TranscriptionEnabled bool `toml:"transcription_enabled"`
	// TranscriptionModel is the STT model slug (e.g. openai/whisper-1 on OpenRouter).
	TranscriptionModel string `toml:"transcription_model"`

	// ImageGenerationEnabled enables the generate_image tool for sessions using this provider.
	ImageGenerationEnabled bool `toml:"image_generation_enabled"`
	// ImageModel is the image model slug (e.g. openai/gpt-image-1 on OpenRouter).
	ImageModel string `toml:"image_model"`
}

// TranscriptionProvider returns the provider configured for voice transcription.
// Prefers default_provider when it has transcription enabled; otherwise the first enabled provider.
func (c Config) TranscriptionProvider() (name string, cfg ProviderConfig, ok bool) {
	if p, found := c.Providers[c.DefaultProvider]; found && p.TranscriptionEnabled {
		return c.DefaultProvider, p, true
	}
	for name, p := range c.Providers {
		if p.TranscriptionEnabled {
			return name, p, true
		}
	}
	return "", ProviderConfig{}, false
}

// ResolveValinorDir returns the absolute base directory for Valinor data.
func (c Config) ResolveValinorDir() (string, error) {
	dir := c.ValinorDir
	if dir == "" {
		dir = DefaultValinorDir
	}
	return resolvePathWithTilde(dir)
}

// ResolveSessionDir returns the absolute path for the sessions directory.
func (c Config) ResolveSessionDir() (string, error) {
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "sessions"), nil
}

// ResolveWorkspaceDir returns the absolute path for the global tool workspace.
func (c Config) ResolveWorkspaceDir() (string, error) {
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

// ResolveDreamingDir returns the absolute path for dreaming state and backups.
func (c Config) ResolveDreamingDir() (string, error) {
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "dreaming"), nil
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
