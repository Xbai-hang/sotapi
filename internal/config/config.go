package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const defaultTelegramWebhookPath = "/webhooks/telegram"

// Config is the complete phase-one SotAPI runtime configuration.
type Config struct {
	Server              ServerConfig   `mapstructure:"server"`
	Auth                AuthConfig     `mapstructure:"auth"`
	RequestTimeout      time.Duration  `mapstructure:"request_timeout"`
	ReasoningTemplate   string         `mapstructure:"reasoning_template"`
	UnansweredThreshold int            `mapstructure:"unanswered_threshold"`
	Telegram            TelegramConfig `mapstructure:"telegram"`
	Models              []ModelConfig  `mapstructure:"models"`
	Pools               []PoolConfig   `mapstructure:"pools"`
	Users               []UserConfig   `mapstructure:"users"`
}

// AuthConfig controls access to the public API. Mode is either api_key or
// none; api_key mode accepts any configured Bearer token.
type AuthConfig struct {
	Mode    string   `mapstructure:"mode"`
	APIKeys []string `mapstructure:"api_keys"`
}

// ServerConfig controls the public HTTP server.
type ServerConfig struct {
	ListenAddress     string        `mapstructure:"listen"`
	BaseURL           string        `mapstructure:"base_url"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	IdleTimeout       time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout   time.Duration `mapstructure:"shutdown_timeout"`
	StreamKeepAlive   time.Duration `mapstructure:"stream_keep_alive"`
	MaxBodyBytes      int64         `mapstructure:"max_body_bytes"`
}

// TelegramConfig controls Bot API delivery and update reception.
type TelegramConfig struct {
	BotToken           string                `mapstructure:"bot_token"`
	APIBaseURL         string                `mapstructure:"api_base_url"`
	UpdateMode         string                `mapstructure:"update_mode"`
	DropPendingUpdates bool                  `mapstructure:"drop_pending_updates"`
	PollTimeout        time.Duration         `mapstructure:"poll_timeout"`
	RetryInterval      time.Duration         `mapstructure:"retry_interval"`
	Webhook            TelegramWebhookConfig `mapstructure:"webhook"`
}

// TelegramWebhookConfig controls the public Telegram callback endpoint.
type TelegramWebhookConfig struct {
	URL         string `mapstructure:"url"`
	SecretToken string `mapstructure:"secret_token"`
}

// ModelConfig maps one public model name to one phase-one user pool.
type ModelConfig struct {
	ID     string `mapstructure:"id"`
	PoolID string `mapstructure:"pool"`
}

// PoolConfig lists users eligible to answer for a pool.
type PoolConfig struct {
	ID      string   `mapstructure:"id"`
	UserIDs []string `mapstructure:"users"`
}

// UserConfig describes one human and their Channel endpoint.
type UserConfig struct {
	ID        string `mapstructure:"id"`
	Channel   string `mapstructure:"channel"`
	Recipient string `mapstructure:"recipient"`
}

// Load reads a YAML file, overlays supported SOTAPI_* environment variables,
// rejects unknown fields and validates phase-one runtime requirements.
func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("config: file path is required")
	}

	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("SOTAPI")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := v.UnmarshalExact(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: decode %s: %w", path, err)
	}
	cfg.applyDerivedValues()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDerivedValues() {
	if c.Telegram.UpdateMode != "webhook" || strings.TrimSpace(c.Telegram.Webhook.URL) != "" {
		return
	}
	c.Telegram.Webhook.URL = strings.TrimRight(c.Server.BaseURL, "/") + defaultTelegramWebhookPath
}

// Validate checks values that are independent of the routing graph. Model,
// pool and user references are validated once by routing.NewRouter at startup.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.ListenAddress) == "" {
		return errors.New("config: server.listen is required")
	}
	if err := validateHTTPURL("server.base_url", c.Server.BaseURL); err != nil {
		return err
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return errors.New("config: server.read_header_timeout must be positive")
	}
	if c.Server.IdleTimeout <= 0 {
		return errors.New("config: server.idle_timeout must be positive")
	}
	if c.Server.ShutdownTimeout <= 0 {
		return errors.New("config: server.shutdown_timeout must be positive")
	}
	if c.Server.StreamKeepAlive <= 0 {
		return errors.New("config: server.stream_keep_alive must be positive")
	}
	if c.Server.MaxBodyBytes <= 0 {
		return errors.New("config: server.max_body_bytes must be positive")
	}
	if err := validateAuth(c.Auth); err != nil {
		return err
	}
	if c.RequestTimeout <= 0 {
		return errors.New("config: request_timeout must be positive")
	}
	if strings.TrimSpace(c.ReasoningTemplate) == "" {
		return errors.New("config: reasoning_template is required")
	}
	if c.UnansweredThreshold <= 0 {
		return errors.New("config: unanswered_threshold must be positive")
	}
	if strings.TrimSpace(c.Telegram.BotToken) == "" {
		return errors.New("config: telegram.bot_token or SOTAPI_TELEGRAM_BOT_TOKEN is required")
	}
	if err := validateHTTPURL("telegram.api_base_url", c.Telegram.APIBaseURL); err != nil {
		return err
	}
	if err := validateTelegramUpdates(c.Telegram); err != nil {
		return err
	}
	if c.Telegram.PollTimeout < time.Second {
		return errors.New("config: telegram.poll_timeout must be at least one second")
	}
	if c.Telegram.RetryInterval <= 0 {
		return errors.New("config: telegram.retry_interval must be positive")
	}
	if len(c.Models) == 0 || len(c.Pools) == 0 || len(c.Users) == 0 {
		return errors.New("config: at least one model, pool and user are required")
	}
	for _, user := range c.Users {
		if user.Channel != "telegram" {
			return fmt.Errorf("config: user %q uses unsupported phase-one channel %q", user.ID, user.Channel)
		}
	}
	return nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.listen", ":8080")
	v.SetDefault("server.base_url", "http://localhost:8080")
	v.SetDefault("server.read_header_timeout", "10s")
	v.SetDefault("server.idle_timeout", "2m")
	v.SetDefault("server.shutdown_timeout", "10s")
	v.SetDefault("server.stream_keep_alive", "15s")
	v.SetDefault("server.max_body_bytes", int64(1<<20))
	v.SetDefault("auth.mode", "api_key")
	v.SetDefault("auth.api_keys", []string{})
	v.SetDefault("request_timeout", "5m")
	v.SetDefault("reasoning_template", "A human is thinking about your request.")
	v.SetDefault("unanswered_threshold", 3)
	v.SetDefault("telegram.bot_token", "")
	v.SetDefault("telegram.api_base_url", "https://api.telegram.org")
	v.SetDefault("telegram.update_mode", "polling")
	v.SetDefault("telegram.drop_pending_updates", false)
	v.SetDefault("telegram.poll_timeout", "30s")
	v.SetDefault("telegram.retry_interval", "2s")
	v.SetDefault("telegram.webhook.url", "")
	v.SetDefault("telegram.webhook.secret_token", "")
}

func validateTelegramUpdates(telegram TelegramConfig) error {
	switch telegram.UpdateMode {
	case "polling":
		return nil
	case "webhook":
		parsed, err := url.Parse(telegram.Webhook.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return errors.New("config: telegram.webhook.url must be an absolute HTTPS URL in webhook mode")
		}
		if parsed.Path == "" || parsed.Path == "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("config: telegram.webhook.url must contain a dedicated path without query or fragment")
		}
		if !validTelegramSecret(telegram.Webhook.SecretToken) {
			return errors.New("config: telegram.webhook.secret_token must contain 1-256 letters, digits, underscores or hyphens")
		}
		return nil
	default:
		return fmt.Errorf("config: telegram.update_mode must be polling or webhook, got %q", telegram.UpdateMode)
	}
}

func validTelegramSecret(secret string) bool {
	if len(secret) == 0 || len(secret) > 256 {
		return false
	}
	for _, character := range secret {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateAuth(auth AuthConfig) error {
	switch auth.Mode {
	case "none":
		return nil
	case "api_key":
		if len(auth.APIKeys) == 0 {
			return errors.New("config: auth.api_keys requires at least one key in api_key mode")
		}
		seen := make(map[string]struct{}, len(auth.APIKeys))
		for index, key := range auth.APIKeys {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("config: auth.api_keys[%d] must not be empty", index)
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("config: auth.api_keys contains duplicate key at index %d", index)
			}
			seen[key] = struct{}{}
		}
		return nil
	default:
		return fmt.Errorf("config: auth.mode must be api_key or none, got %q", auth.Mode)
	}
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("config: %s must be an absolute HTTP(S) URL", name)
	}
	return nil
}
