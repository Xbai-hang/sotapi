package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndEnvironmentOverrides(t *testing.T) {
	path := writeConfig(t, `
auth:
  mode: api_key
  api_keys: [file-key]
telegram:
  bot_token: file-token
models:
  - id: human
    pool: friends
pools:
  - id: friends
    users: [alice]
users:
  - id: alice
    channel: telegram
    recipient: "123"
`)
	t.Setenv("SOTAPI_AUTH_API_KEYS", "environment-key,second-key")
	t.Setenv("SOTAPI_TELEGRAM_BOT_TOKEN", "environment-token")
	t.Setenv("SOTAPI_TELEGRAM_WEBHOOK_SECRET_TOKEN", "environment-webhook-secret")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Auth.APIKeys) != 2 || cfg.Auth.APIKeys[0] != "environment-key" || cfg.Auth.APIKeys[1] != "second-key" || cfg.Telegram.BotToken != "environment-token" {
		t.Fatalf("environment overrides not applied: %#v", cfg)
	}
	if cfg.Telegram.Webhook.SecretToken != "environment-webhook-secret" {
		t.Fatalf("Telegram webhook secret environment override = %q", cfg.Telegram.Webhook.SecretToken)
	}
	if cfg.Auth.Mode != "api_key" {
		t.Fatalf("auth mode = %q", cfg.Auth.Mode)
	}
	if cfg.Server.ListenAddress != ":8080" || cfg.Server.MaxBodyBytes != 1<<20 {
		t.Fatalf("server defaults = %#v", cfg.Server)
	}
	if cfg.Server.StreamKeepAlive != 15*time.Second {
		t.Fatalf("stream keep-alive default = %v", cfg.Server.StreamKeepAlive)
	}
	if cfg.Human.ResponseTimeout != 5*time.Minute || cfg.Telegram.PollTimeout != 30*time.Second {
		t.Fatalf("duration defaults: response=%v poll=%v", cfg.Human.ResponseTimeout, cfg.Telegram.PollTimeout)
	}
	if cfg.Telegram.UpdateMode != "polling" || cfg.Telegram.DropPendingUpdates {
		t.Fatalf("Telegram update defaults = %#v", cfg.Telegram)
	}
	if !cfg.Human.AutoOffline.Enabled || cfg.Human.AutoOffline.AfterMissedReplies != 3 || cfg.Human.ReasoningTemplate == "" {
		t.Fatalf("human defaults = %#v", cfg.Human)
	}
	if cfg.Fallback.Mode != "template" || cfg.Fallback.Template == "" {
		t.Fatalf("fallback defaults = %#v", cfg.Fallback)
	}
}

func TestLoadDecodesExplicitValues(t *testing.T) {
	path := writeConfig(t, `
server:
  listen: 127.0.0.1:9090
  base_url: https://sotapi.example.com
  read_header_timeout: 3s
  idle_timeout: 1m
  shutdown_timeout: 4s
  stream_keep_alive: 5s
  max_body_bytes: 2048
auth:
  mode: none
human:
  response_timeout: 45s
  reasoning_template: waiting
  auto_offline:
    enabled: false
    after_missed_replies: 5
fallback:
  mode: template
  template: fallback answer
telegram:
  bot_token: token
  api_base_url: https://telegram.example.com
  update_mode: webhook
  drop_pending_updates: true
  poll_timeout: 10s
  retry_interval: 250ms
  webhook:
    url: https://sotapi.example.com/hooks/telegram
    secret_token: webhook-secret_1
models:
  - id: human
    pool: friends
pools:
  - id: friends
    users: [alice]
users:
  - id: alice
    channel: telegram
    recipient: "123"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.ListenAddress != "127.0.0.1:9090" || cfg.Server.ReadHeaderTimeout != 3*time.Second || cfg.Server.StreamKeepAlive != 5*time.Second || cfg.Server.MaxBodyBytes != 2048 {
		t.Fatalf("server config = %#v", cfg.Server)
	}
	if cfg.Human.ResponseTimeout != 45*time.Second || cfg.Telegram.RetryInterval != 250*time.Millisecond || cfg.Human.AutoOffline.AfterMissedReplies != 5 || cfg.Human.AutoOffline.Enabled {
		t.Fatalf("runtime config = %#v", cfg)
	}
	if cfg.Human.ReasoningTemplate != "waiting" || cfg.Fallback.Mode != "template" || cfg.Fallback.Template != "fallback answer" {
		t.Fatalf("response config = %#v", cfg)
	}
	if cfg.Auth.Mode != "none" || len(cfg.Auth.APIKeys) != 0 {
		t.Fatalf("auth config = %#v", cfg.Auth)
	}
	if cfg.Telegram.UpdateMode != "webhook" || !cfg.Telegram.DropPendingUpdates || cfg.Telegram.Webhook.SecretToken != "webhook-secret_1" {
		t.Fatalf("Telegram update config = %#v", cfg.Telegram)
	}
}

func TestLoadDerivesWebhookURLFromServerBaseURL(t *testing.T) {
	path := writeConfig(t, `
server:
  base_url: https://sotapi.example.com/
auth:
  mode: api_key
  api_keys: [secret]
telegram:
  bot_token: token
  update_mode: webhook
  webhook:
    secret_token: auto
models:
  - id: human
    pool: friends
pools:
  - id: friends
    users: [alice]
users:
  - id: alice
    channel: telegram
    recipient: "123"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Telegram.Webhook.URL != "https://sotapi.example.com/webhooks/telegram" {
		t.Fatalf("derived webhook URL = %q", cfg.Telegram.Webhook.URL)
	}
}

func TestLoadRejectsMalformedAndUnknownConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "malformed YAML", content: "models: [", wantErr: "read"},
		{name: "unknown field", content: validYAML() + "\nsurprise: true\n", wantErr: "surprise"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
	if _, err := Load(""); err == nil {
		t.Fatal("Load(empty path) succeeded")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load(missing file) succeeded")
	}
}

func TestValidateRejectsInvalidRuntimeValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "listen", mutate: func(c *Config) { c.Server.ListenAddress = "" }, wantErr: "server.listen"},
		{name: "base URL", mutate: func(c *Config) { c.Server.BaseURL = "localhost" }, wantErr: "server.base_url"},
		{name: "read header timeout", mutate: func(c *Config) { c.Server.ReadHeaderTimeout = 0 }, wantErr: "read_header_timeout"},
		{name: "idle timeout", mutate: func(c *Config) { c.Server.IdleTimeout = 0 }, wantErr: "idle_timeout"},
		{name: "shutdown timeout", mutate: func(c *Config) { c.Server.ShutdownTimeout = 0 }, wantErr: "shutdown_timeout"},
		{name: "stream keep-alive", mutate: func(c *Config) { c.Server.StreamKeepAlive = 0 }, wantErr: "stream_keep_alive"},
		{name: "body limit", mutate: func(c *Config) { c.Server.MaxBodyBytes = 0 }, wantErr: "max_body_bytes"},
		{name: "auth mode", mutate: func(c *Config) { c.Auth.Mode = "magic" }, wantErr: "auth.mode"},
		{name: "missing API keys", mutate: func(c *Config) { c.Auth.APIKeys = nil }, wantErr: "auth.api_keys"},
		{name: "blank API key", mutate: func(c *Config) { c.Auth.APIKeys = []string{" "} }, wantErr: "auth.api_keys[0]"},
		{name: "duplicate API key", mutate: func(c *Config) { c.Auth.APIKeys = []string{"same", "same"} }, wantErr: "duplicate"},
		{name: "response timeout", mutate: func(c *Config) { c.Human.ResponseTimeout = 0 }, wantErr: "human.response_timeout"},
		{name: "reasoning", mutate: func(c *Config) { c.Human.ReasoningTemplate = " " }, wantErr: "human.reasoning_template"},
		{name: "missed replies", mutate: func(c *Config) { c.Human.AutoOffline.AfterMissedReplies = 0 }, wantErr: "human.auto_offline.after_missed_replies"},
		{name: "fallback mode", mutate: func(c *Config) { c.Fallback.Mode = "openai" }, wantErr: "fallback.mode"},
		{name: "fallback template", mutate: func(c *Config) { c.Fallback.Template = " " }, wantErr: "fallback.template"},
		{name: "bot token", mutate: func(c *Config) { c.Telegram.BotToken = "" }, wantErr: "bot_token"},
		{name: "Telegram URL", mutate: func(c *Config) { c.Telegram.APIBaseURL = "ftp://example.com" }, wantErr: "telegram.api_base_url"},
		{name: "update mode", mutate: func(c *Config) { c.Telegram.UpdateMode = "both" }, wantErr: "update_mode"},
		{name: "webhook URL", mutate: func(c *Config) { enableWebhook(c); c.Telegram.Webhook.URL = "http://example.com/hook" }, wantErr: "webhook.url"},
		{name: "webhook path", mutate: func(c *Config) { enableWebhook(c); c.Telegram.Webhook.URL = "https://example.com/" }, wantErr: "dedicated path"},
		{name: "webhook query", mutate: func(c *Config) { enableWebhook(c); c.Telegram.Webhook.URL += "?token=x" }, wantErr: "without query"},
		{name: "webhook secret", mutate: func(c *Config) { enableWebhook(c); c.Telegram.Webhook.SecretToken = "bad secret" }, wantErr: "secret_token"},
		{name: "long webhook secret", mutate: func(c *Config) { enableWebhook(c); c.Telegram.Webhook.SecretToken = strings.Repeat("x", 257) }, wantErr: "secret_token"},
		{name: "poll timeout", mutate: func(c *Config) { c.Telegram.PollTimeout = time.Millisecond }, wantErr: "poll_timeout"},
		{name: "retry interval", mutate: func(c *Config) { c.Telegram.RetryInterval = 0 }, wantErr: "retry_interval"},
		{name: "models", mutate: func(c *Config) { c.Models = nil }, wantErr: "model, pool and user"},
		{name: "pools", mutate: func(c *Config) { c.Pools = nil }, wantErr: "model, pool and user"},
		{name: "users", mutate: func(c *Config) { c.Users = nil }, wantErr: "model, pool and user"},
		{name: "channel", mutate: func(c *Config) { c.Users[0].Channel = "email" }, wantErr: "unsupported phase-one channel"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		Server: ServerConfig{
			ListenAddress:     ":8080",
			BaseURL:           "http://localhost:8080",
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       time.Minute,
			ShutdownTimeout:   10 * time.Second,
			StreamKeepAlive:   15 * time.Second,
			MaxBodyBytes:      1 << 20,
		},
		Auth: AuthConfig{Mode: "api_key", APIKeys: []string{"secret"}},
		Human: HumanConfig{
			ResponseTimeout:   time.Minute,
			ReasoningTemplate: "waiting",
			AutoOffline:       AutoOfflineConfig{Enabled: true, AfterMissedReplies: 3},
		},
		Fallback: FallbackConfig{Mode: "template", Template: "fallback answer"},
		Telegram: TelegramConfig{
			BotToken:      "token",
			APIBaseURL:    "https://api.telegram.org",
			UpdateMode:    "polling",
			PollTimeout:   30 * time.Second,
			RetryInterval: time.Second,
		},
		Models: []ModelConfig{{ID: "human", PoolID: "friends"}},
		Pools:  []PoolConfig{{ID: "friends", UserIDs: []string{"alice"}}},
		Users:  []UserConfig{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	}
}

func enableWebhook(config *Config) {
	config.Telegram.UpdateMode = "webhook"
	config.Telegram.Webhook = TelegramWebhookConfig{
		URL:         "https://example.com/hooks/telegram",
		SecretToken: "valid-secret_1",
	}
}

func validYAML() string {
	return `
auth:
  mode: api_key
  api_keys: [secret]
telegram:
  bot_token: token
models:
  - id: human
    pool: friends
pools:
  - id: friends
    users: [alice]
users:
  - id: alice
    channel: telegram
    recipient: "123"
`
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
