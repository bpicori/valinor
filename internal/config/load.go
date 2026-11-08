package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"valinor/internal/logger"
)

//go:embed config.example.toml
var exampleConfigTOML string

// ConfigFileName is the canonical config file name under the valinor directory.
const ConfigFileName = "config.toml"

// ErrNotFound indicates the config file does not exist.
var ErrNotFound = errors.New("config file not found")

// DefaultConfigPath returns the absolute path to ~/.valinor/config.toml.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".valinor", ConfigFileName), nil
}

// Load loads configuration from ~/.valinor/config.toml.
func Load() (Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return Config{}, err
	}
	return LoadFromPath(path)
}

// LoadFromPath loads configuration from the given path.
func LoadFromPath(path string) (Config, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return Config{}, err
	}
	return LoadFromFile(path)
}

// LoadFromFile loads configuration from a TOML file.
func LoadFromFile(path string) (Config, error) {
	return loadFromFile(path, "", false)
}

func loadFromFile(path, defaultDataDir string, enforceDataDir bool) (Config, error) {
	cfg, md, err := decodeTOML(path)
	if err != nil {
		logger.Error("config load failed", logger.BuildMetadata("path", path, "error", err))
		return Config{}, err
	}
	if defaultDataDir != "" && !md.IsDefined("valinor_dir") {
		cfg.ValinorDir = defaultDataDir
	}
	cfg = ApplyDefaults(cfg, md)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	if enforceDataDir && defaultDataDir != "" {
		dataDir, err := cfg.ResolveValinorDir()
		if err != nil {
			return Config{}, err
		}
		if !pathWithin(defaultDataDir, dataDir) {
			return Config{}, fmt.Errorf("instance data directory %q must stay inside %q; remove valinor_dir or set it to the instance root", dataDir, defaultDataDir)
		}
	}
	return cfg, nil
}

// LoadTemplate loads the embedded example config without API-key validation.
func LoadTemplate() (Config, error) {
	cfg, md, err := decodeTOMLString(exampleConfigTOML)
	if err != nil {
		return Config{}, fmt.Errorf("decode example config: %w", err)
	}
	cfg = ApplyDefaults(cfg, md)
	return cfg, nil
}

// LoadInitial loads the saved config when present, otherwise wizard-ready template defaults.
func LoadInitial() (Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return Config{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return LoadFromFile(path)
	} else if !os.IsNotExist(err) {
		return Config{}, err
	}
	return LoadWizardTemplate()
}

// LoadWizardTemplate loads non-provider defaults for a fresh wizard session.
func LoadWizardTemplate() (Config, error) {
	cfg, err := LoadTemplate()
	if err != nil {
		return Config{}, err
	}
	cfg.Providers = map[string]ProviderConfig{}
	cfg.DefaultProvider = ""
	return cfg, nil
}

// Validate checks that a config is complete and consistent.
func Validate(cfg Config) error {
	if cfg.DefaultProvider == "" {
		return fmt.Errorf("default_provider is required")
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}
	if _, ok := cfg.Providers[cfg.DefaultProvider]; !ok {
		return fmt.Errorf("default_provider '%s' not found in providers", cfg.DefaultProvider)
	}

	for name, provider := range cfg.Providers {
		if provider.Type == "" {
			return fmt.Errorf("provider '%s' missing type field", name)
		}
		if provider.Type != "openai" && provider.Type != "anthropic" && provider.Type != "gemini" && provider.Type != "openai-compatible" {
			return fmt.Errorf("provider '%s' has invalid type '%s' (must be: openai, anthropic, gemini, or openai-compatible)", name, provider.Type)
		}
		if provider.APIKey == "" {
			return fmt.Errorf("provider '%s' missing api_key field", name)
		}
		if err := validateProviderAPIKey(name, provider.APIKey); err != nil {
			return err
		}
		if provider.DefaultModel == "" {
			return fmt.Errorf("provider '%s' missing default_model field", name)
		}
		if provider.Type == "openai-compatible" && provider.BaseURL == "" {
			return fmt.Errorf("provider '%s' of type 'openai-compatible' requires base_url field", name)
		}
		switch provider.Thinking {
		case "", "enabled", "disabled":
		default:
			return fmt.Errorf("provider '%s' thinking must be enabled, disabled, or empty", name)
		}
		if provider.TranscriptionEnabled {
			if provider.TranscriptionModel == "" {
				return fmt.Errorf("provider '%s' has transcription_enabled but missing transcription_model", name)
			}
			if provider.Type != "openai" && provider.Type != "openai-compatible" {
				return fmt.Errorf("provider '%s' transcription requires type openai or openai-compatible", name)
			}
		}
	}

	if cfg.Telegram.Enabled && cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram bot_token is required when telegram is enabled")
	}

	if cfg.Scheduler.ScriptsDir != "" {
		scriptsDir, err := cfg.ResolveScriptsDir()
		if err != nil {
			return err
		}
		workspaceDir, err := cfg.ResolveWorkspaceDir()
		if err != nil {
			return err
		}
		if !pathWithin(workspaceDir, scriptsDir) {
			return fmt.Errorf("scheduler.scripts_dir must be inside the workspace (scripts run in the sandbox)")
		}
	}
	if cfg.Search.Provider != "" {
		if cfg.Search.Provider != "deepseek" {
			return fmt.Errorf("search.provider %q is not supported (supported: deepseek)", cfg.Search.Provider)
		}
		if strings.TrimSpace(cfg.Search.APIKey) == "" {
			return fmt.Errorf("search.api_key is required (or set providers.%s.api_key)", cfg.Search.Provider)
		}
		if cfg.Search.MaxResults < 1 || cfg.Search.MaxResults > 50 {
			return fmt.Errorf("search.max_results must be between 1 and 50")
		}
	}
	if cfg.Context.WindowTokens > 0 && cfg.Context.WindowTokens < 8000 {
		return fmt.Errorf("context.window_tokens must be at least 8000")
	}
	if cfg.Context.MaxOutputTokens > 0 && cfg.Context.MaxOutputTokens < 256 {
		return fmt.Errorf("context.max_output_tokens must be at least 256")
	}

	return nil
}

// ApplyDefaults fills unset config fields with application defaults.
func ApplyDefaults(cfg Config, md toml.MetaData) Config {
	if cfg.ValinorDir == "" {
		cfg.ValinorDir = DefaultValinorDir
	}
	if cfg.MaxAgentIterations <= 0 {
		cfg.MaxAgentIterations = DefaultMaxAgentIterations
	}
	if cfg.GatewayHost == "" {
		cfg.GatewayHost = DefaultGatewayHost
	}
	if cfg.GatewayPort <= 0 {
		cfg.GatewayPort = DefaultGatewayPort
	}
	if cfg.GatewayPath == "" {
		cfg.GatewayPath = DefaultGatewayPath
	}
	cfg.GatewayEnabled = true
	if !md.IsDefined("telegram", "enabled") {
		cfg.Telegram.Enabled = true
	}

	if cfg.Telegram.PollTimeout <= 0 {
		cfg.Telegram.PollTimeout = DefaultTelegramPollTimeout
	}
	if cfg.Telegram.PollTimeout > 90 {
		cfg.Telegram.PollTimeout = 90
	}
	if cfg.Telegram.RetryBaseDelay <= 0 {
		cfg.Telegram.RetryBaseDelay = DefaultTelegramRetryBaseDelay
	}
	if cfg.Telegram.RetryMaxDelay <= 0 {
		cfg.Telegram.RetryMaxDelay = DefaultTelegramRetryMaxDelay
	}
	if !md.IsDefined("telegram", "require_mention") {
		cfg.Telegram.RequireMention = DefaultTelegramRequireMention
	}

	cfg.Scheduler.Enabled = true

	if cfg.Scheduler.ScriptTimeout == "" {
		cfg.Scheduler.ScriptTimeout = DefaultSchedulerScriptTimeout
	}

	if cfg.Context.PreserveRecentMessages <= 0 {
		cfg.Context.PreserveRecentMessages = DefaultPreserveRecentMessages
	}
	if cfg.Search.Provider != "" {
		if cfg.Search.APIKey == "" {
			if provider, ok := cfg.Providers[cfg.Search.Provider]; ok {
				cfg.Search.APIKey = provider.APIKey
			}
		}
		if cfg.Search.MaxResults <= 0 {
			cfg.Search.MaxResults = DefaultSearchMaxResults
		}
		if cfg.Search.MaxResults > 50 {
			cfg.Search.MaxResults = 50
		}
	}

	if cfg.Context.WindowTokens <= 0 {
		cfg.Context.WindowTokens = DefaultContextWindowTokens
	}
	if cfg.Context.MaxOutputTokens <= 0 {
		cfg.Context.MaxOutputTokens = DefaultMaxOutputTokens
	}

	return cfg
}

// Save writes config to ~/.valinor/config.toml.
func Save(cfg Config) error {
	path, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	return SaveToPath(path, cfg)
}

// SaveToPath writes config to the given path, creating parent directories as needed.
func SaveToPath(path string, cfg Config) error {
	telegramEnabled := cfg.Telegram.Enabled
	cfg = ApplyDefaults(cfg, toml.MetaData{})
	cfg.Telegram.Enabled = telegramEnabled
	if err := Validate(cfg); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()

	enc := toml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return nil
}

// ProviderTypes lists supported provider type values.
var ProviderTypes = []string{"openai", "anthropic", "gemini", "openai-compatible"}

func decodeTOML(path string) (Config, toml.MetaData, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, md, err
	}
	return cfg, md, nil
}

func decodeTOMLString(content string) (Config, toml.MetaData, error) {
	var cfg Config
	md, err := toml.NewDecoder(strings.NewReader(content)).Decode(&cfg)
	if err != nil {
		return Config{}, md, err
	}
	return cfg, md, nil
}

// ValidateProviderAPIKey rejects values that look like Telegram bot tokens.
func ValidateProviderAPIKey(apiKey string) error {
	return validateProviderAPIKey("provider", apiKey)
}

// pathWithin reports whether target is base or lives under base.
func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func validateProviderAPIKey(name, apiKey string) error {
	if looksLikeTelegramBotToken(apiKey) {
		return fmt.Errorf("%s api_key looks like a Telegram bot token; use your LLM provider API key instead", name)
	}
	return nil
}

func looksLikeTelegramBotToken(key string) bool {
	botID, secret, ok := strings.Cut(key, ":")
	if !ok || botID == "" || len(secret) < 20 {
		return false
	}
	for _, r := range botID {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
