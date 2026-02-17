package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// DefaultBaseURL is the default OpenAI API base URL.
const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultSessionDir is the default sessions directory under home.
// Each session is stored at session_dir/<uuid>/ with workspace at session_dir/<uuid>/workspace/.
const DefaultSessionDir = "~/.valinor/sessions"

// DefaultMaxAgentIterations is the default cap on agent tool-call iterations per user message.
const DefaultMaxAgentIterations = 20

// Config holds OpenAI client configuration and paths.
type Config struct {
	APIKey               string `toml:"api_key"`
	BaseURL              string `toml:"base_url"`
	Model                string `toml:"model"`
	SessionDir           string `toml:"session_dir"`
	MaxAgentIterations   int    `toml:"max_agent_iterations"`
}

// LoadFromFile loads configuration from a TOML file.
// If base_url is empty, DefaultBaseURL is used.
// If session_dir is empty, DefaultSessionDir is used.
func LoadFromFile(path string) (Config, error) {
	var cfg Config
	_, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.SessionDir == "" {
		cfg.SessionDir = DefaultSessionDir
	}
	if cfg.MaxAgentIterations <= 0 {
		cfg.MaxAgentIterations = DefaultMaxAgentIterations
	}
	return cfg, nil
}

// ResolveSessionDir returns the absolute path for the sessions directory.
// If the path starts with ~, it is expanded to the user's home directory.
// Otherwise the path is resolved relative to the current working directory.
func (c Config) ResolveSessionDir() (string, error) {
	dir := c.SessionDir
	if dir == "" {
		dir = DefaultSessionDir
	}
	if len(dir) > 1 && dir[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, dir[2:])
	} else if len(dir) == 1 && dir == "~" {
		return os.UserHomeDir()
	}
	return filepath.Abs(dir)
}

// NewClient creates an OpenAI client configured with this config's API key and base URL.
func (c Config) NewClient() openai.Client {
	opts := []option.RequestOption{}
	if c.APIKey != "" {
		opts = append(opts, option.WithAPIKey(c.APIKey))
	}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	return openai.NewClient(opts...)
}
