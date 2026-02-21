package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"valinor/internal/applog"
)

// DefaultBaseURL is the default OpenAI API base URL.
const DefaultBaseURL = "https://api.openai.com/v1"

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

// Config holds OpenAI client configuration and paths.
type Config struct {
	APIKey             string `toml:"api_key"`
	BaseURL            string `toml:"base_url"`
	Model              string `toml:"model"`
	ValinorDir         string `toml:"valinor_dir"`
	SessionDir         string `toml:"session_dir"`
	WorkspaceDir       string `toml:"workspace_dir"`
	MaxAgentIterations int    `toml:"max_agent_iterations"`
	WebEnabled         bool   `toml:"web_enabled"`
	WebHost            string `toml:"web_host"`
	WebPort            int    `toml:"web_port"`
}

// LoadFromFile loads configuration from a TOML file.
func LoadFromFile(path string) (Config, error) {
	var cfg Config
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		applog.Error("config load failed", applog.M("path", path, "error", err))
		return Config{}, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
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

// ResolveLogsDir returns the absolute path for application logs.
func (c Config) ResolveLogsDir() (string, error) {
	baseDir, err := c.ResolveValinorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "logs"), nil
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
