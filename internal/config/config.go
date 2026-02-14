package config

import (
	"os"

	"github.com/BurntSushi/toml"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// DefaultBaseURL is the default OpenAI API base URL.
const DefaultBaseURL = "https://api.openai.com/v1"

// Config holds OpenAI client configuration.
type Config struct {
	APIKey  string `toml:"api_key"`
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
}

// LoadFromFile loads configuration from a TOML file.
// If base_url is empty, DefaultBaseURL is used.
func LoadFromFile(path string) (Config, error) {
	var cfg Config
	_, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return cfg, nil
}

// LoadFromFileOrEnv attempts to load from the given file path.
// If the file does not exist, falls back to environment variables.
func LoadFromFileOrEnv(path string) (Config, error) {
	if _, err := os.Stat(path); err == nil {
		return LoadFromFile(path)
	}
	return LoadFromEnv(), nil
}

// LoadFromEnv loads configuration from environment variables.
// API key is read from OPENAI_API_KEY; base URL from OPENAI_BASE_URL (defaults to DefaultBaseURL).
func LoadFromEnv() Config {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
	}
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
