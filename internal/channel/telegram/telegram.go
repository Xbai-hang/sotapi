package telegram

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/routing"
)

const (
	channelName       = "telegram"
	defaultAPIBaseURL = "https://api.telegram.org"
	maxMessageRunes   = 4096
	maxAPIResponse    = 1 << 20
	autoWebhookSecret = "auto"

	// UpdateModePolling receives Telegram updates with getUpdates.
	UpdateModePolling = "polling"
	// UpdateModeWebhook receives Telegram updates through an HTTPS callback.
	UpdateModeWebhook = "webhook"
)

// Config contains Telegram Bot API runtime settings.
type Config struct {
	BotToken           string
	APIBaseURL         string
	UpdateMode         string
	DropPendingUpdates bool
	PollTimeout        time.Duration
	RetryInterval      time.Duration
	WebhookURL         string
	WebhookSecretToken string
}

// ReplyHandler accepts a parsed human reply from Telegram.
type ReplyHandler interface {
	// SubmitReply associates content with a still-pending completion request.
	SubmitReply(requestID, content string) error
	// SetOnline restores the human identified by a Channel recipient.
	SetOnline(channel, recipient string) error
}

// Client delivers tasks through Telegram and receives replies through the
// configured polling or webhook update mode.
type Client struct {
	config            Config
	httpClient        *http.Client
	replies           ReplyHandler
	logger            *slog.Logger
	correlations      *correlationStore
	webhookPath       string
	webhookSecretHash [sha256.Size]byte
}

// NewClient validates dependencies and constructs a Telegram Client.
func NewClient(cfg Config, httpClient *http.Client, replies ReplyHandler, logger *slog.Logger) (*Client, error) {
	if strings.TrimSpace(cfg.BotToken) == "" {
		return nil, errors.New("telegram: bot token is required")
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultAPIBaseURL
	}
	if cfg.UpdateMode == "" {
		cfg.UpdateMode = UpdateModePolling
	}
	parsedURL, err := url.Parse(cfg.APIBaseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("telegram: invalid API base URL %q", cfg.APIBaseURL)
	}
	if cfg.PollTimeout <= 0 {
		return nil, errors.New("telegram: poll timeout must be positive")
	}
	if cfg.RetryInterval <= 0 {
		return nil, errors.New("telegram: retry interval must be positive")
	}
	var webhookPath string
	var webhookSecretHash [sha256.Size]byte
	switch cfg.UpdateMode {
	case UpdateModePolling:
	case UpdateModeWebhook:
		webhookURL, err := url.Parse(cfg.WebhookURL)
		if err != nil || webhookURL.Scheme != "https" || webhookURL.Host == "" || webhookURL.Path == "" || webhookURL.Path == "/" || webhookURL.RawQuery != "" || webhookURL.Fragment != "" {
			return nil, errors.New("telegram: webhook URL must be absolute HTTPS with a dedicated path")
		}
		if cfg.WebhookSecretToken == autoWebhookSecret {
			cfg.WebhookSecretToken = deriveWebhookSecret(cfg.BotToken)
		}
		if !validWebhookSecret(cfg.WebhookSecretToken) {
			return nil, errors.New("telegram: invalid webhook secret token")
		}
		webhookPath = webhookURL.Path
		webhookSecretHash = sha256.Sum256([]byte(cfg.WebhookSecretToken))
	default:
		return nil, fmt.Errorf("telegram: unsupported update mode %q", cfg.UpdateMode)
	}
	if replies == nil {
		return nil, errors.New("telegram: reply handler is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.PollTimeout + 15*time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg.APIBaseURL = strings.TrimRight(cfg.APIBaseURL, "/")
	return &Client{
		config:            cfg,
		httpClient:        httpClient,
		replies:           replies,
		logger:            logger,
		correlations:      newCorrelationStore(),
		webhookPath:       webhookPath,
		webhookSecretHash: webhookSecretHash,
	}, nil
}

func deriveWebhookSecret(botToken string) string {
	mac := hmac.New(sha256.New, []byte(botToken))
	_, _ = mac.Write([]byte("sotapi/telegram-webhook/v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

// WebhookPath returns the configured callback path in webhook mode and an
// empty string in polling mode.
func (c *Client) WebhookPath() string {
	return c.webhookPath
}

// Deliver sends a human-readable task to Telegram. When a task exceeds
// Telegram's 4096-character limit, it is split into ordered messages; every
// part carries the request ID and can be used as a reply target.
func (c *Client) Deliver(ctx context.Context, target routing.Target, task completion.Task) (completion.Delivery, error) {
	if target.User.Channel != channelName {
		return completion.Delivery{}, fmt.Errorf("telegram: unsupported target channel %q", target.User.Channel)
	}

	parts, err := formatTaskParts(target, task)
	if err != nil {
		return completion.Delivery{}, err
	}
	var sent telegramMessage
	for _, part := range parts {
		message, err := c.sendMessage(ctx, target.User.Recipient, part)
		if err != nil {
			return completion.Delivery{}, err
		}
		sent = message
	}
	if sent.MessageID == 0 || sent.Chat.ID == 0 {
		return completion.Delivery{}, errors.New("telegram: sendMessage returned an invalid message")
	}

	correlationID, err := c.correlations.register(sent.Chat.ID, sent.MessageID, task.RequestID)
	if err != nil {
		return completion.Delivery{}, err
	}
	c.logger.Info("telegram task delivered",
		"request_id", task.RequestID,
		"chat_id", sent.Chat.ID,
		"message_id", sent.MessageID,
		"parts", len(parts),
	)
	return completion.Delivery{ID: correlationID}, nil
}

// Forget removes reply correlation for a completed, canceled or timed-out
// delivery. Telegram messages themselves are intentionally retained.
func (c *Client) Forget(delivery completion.Delivery) {
	c.correlations.forget(delivery.ID)
}

// Notify presents a channel-independent lifecycle event to a Telegram user.
// Delivery is best effort and cannot change the caller's completion result.
func (c *Client) Notify(ctx context.Context, target routing.Target, notification completion.Notification) {
	if target.User.Channel != channelName {
		c.logger.Warn("telegram notification ignored", "user_id", target.User.ID, "channel", target.User.Channel)
		return
	}
	var text string
	switch notification.Kind {
	case completion.NotificationAutoOffline:
		text = fmt.Sprintf("你已连续 %d 次未在规定时间内回复，现已自动离线。发送 /online 可恢复在线。", notification.MissedReplies)
	default:
		c.logger.Warn("telegram notification ignored", "user_id", target.User.ID, "kind", notification.Kind)
		return
	}
	if _, err := c.sendMessage(ctx, target.User.Recipient, text); err != nil {
		c.logger.Warn("telegram notification failed", "user_id", target.User.ID, "kind", notification.Kind, "error", err)
	}
}

func (c *Client) sendMessage(ctx context.Context, recipient, text string) (telegramMessage, error) {
	request := sendMessageRequest{ChatID: recipient, Text: text}
	var response telegramMessage
	if err := c.call(ctx, "sendMessage", request, &response); err != nil {
		return telegramMessage{}, err
	}
	return response, nil
}

func (c *Client) call(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: encode %s request: %w", method, err)
	}
	endpoint := c.config.APIBaseURL + "/bot" + c.config.BotToken + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: create %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		// url.Error includes the full Bot API URL, whose path contains the bot
		// token. Unwrap it before logging or returning the transport failure.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return fmt.Errorf("telegram: %s request: %w", method, err)
	}
	defer response.Body.Close()

	var envelope apiEnvelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxAPIResponse))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("telegram: decode %s response with status %d: %w", method, response.StatusCode, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || !envelope.OK {
		return fmt.Errorf("telegram: %s failed with code %d: %s", method, envelope.ErrorCode, envelope.Description)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("telegram: decode %s result: %w", method, err)
	}
	return nil
}

func formatTaskParts(target routing.Target, task completion.Task) ([]string, error) {
	if strings.TrimSpace(task.RequestID) == "" {
		return nil, errors.New("telegram: task request ID is required")
	}
	footer := fmt.Sprintf("\n\n请直接回复此消息提交最终答案。\nSotAPI-Request-ID: %s", task.RequestID)
	contentLimit := maxMessageRunes - utf8.RuneCountInString(footer)
	if contentLimit <= 0 {
		return nil, errors.New("telegram: task request ID is too long")
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "SotAPI 请求\n模型：%s\n回答者：%s\n\n", task.Model, target.User.ID)
	for _, message := range task.Messages {
		fmt.Fprintf(&builder, "[%s]\n%s\n\n", message.Role, message.Content)
	}

	parts := splitText(builder.String(), contentLimit)
	for index := range parts {
		parts[index] += footer
	}
	return parts, nil
}

func splitText(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	runes := []rune(text)
	parts := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		end := min(limit, len(runes))
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

type sendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type telegramMessage struct {
	MessageID      int              `json:"message_id"`
	Chat           telegramChat     `json:"chat"`
	Text           string           `json:"text"`
	ReplyToMessage *telegramMessage `json:"reply_to_message,omitempty"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func validWebhookSecret(secret string) bool {
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
