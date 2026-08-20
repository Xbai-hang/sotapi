package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
)

// Run prepares the configured Telegram update mode and receives updates until
// ctx is canceled. Cancellation is considered a clean stop.
func (c *Client) Run(ctx context.Context) error {
	if err := c.configureUpdateMode(ctx); err != nil {
		return err
	}
	c.logger.Info("telegram update mode configured", "mode", c.config.UpdateMode)
	if c.config.UpdateMode == UpdateModeWebhook {
		<-ctx.Done()
		return nil
	}
	return c.runPolling(ctx)
}

func (c *Client) runPolling(ctx context.Context) error {
	var offset int64
	for {
		updates, err := c.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.Warn("telegram polling failed", "error", err)
			if !waitForRetry(ctx, c.config.RetryInterval) {
				return nil
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if err := c.handleUpdate(update); err != nil {
				c.logUpdateError(ctx, update.UpdateID, err)
			}
		}
	}
}

func (c *Client) logUpdateError(ctx context.Context, updateID int64, err error) {
	level := slog.LevelWarn
	if errors.Is(err, completion.ErrUnknownRequest) {
		level = slog.LevelInfo
	}
	c.logger.Log(ctx, level, "telegram reply ignored", "update_id", updateID, "error", err)
}

func (c *Client) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	timeoutSeconds := max(1, int(c.config.PollTimeout/time.Second))
	request := getUpdatesRequest{
		Offset:         offset,
		Timeout:        timeoutSeconds,
		AllowedUpdates: []string{"message"},
	}
	var updates []telegramUpdate
	if err := c.call(ctx, "getUpdates", request, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) handleUpdate(update telegramUpdate) error {
	message := update.Message
	if message == nil {
		c.logger.Debug("telegram update ignored", "update_id", update.UpdateID, "reason", "no message")
		return nil
	}
	if message.ReplyToMessage == nil {
		c.logger.Debug("telegram update ignored",
			"update_id", update.UpdateID,
			"chat_id", message.Chat.ID,
			"message_id", message.MessageID,
			"reason", "not a reply",
		)
		return nil
	}
	if strings.TrimSpace(message.Text) == "" {
		c.logger.Debug("telegram update ignored",
			"update_id", update.UpdateID,
			"chat_id", message.Chat.ID,
			"message_id", message.MessageID,
			"reply_to_message_id", message.ReplyToMessage.MessageID,
			"reason", "no text",
		)
		return nil
	}
	c.logger.Info("telegram text reply received",
		"update_id", update.UpdateID,
		"chat_id", message.Chat.ID,
		"message_id", message.MessageID,
		"reply_to_message_id", message.ReplyToMessage.MessageID,
	)

	requestID, exists := c.correlations.consume(message.Chat.ID, message.ReplyToMessage.MessageID)
	if !exists {
		requestID = requestIDFromTaskText(message.ReplyToMessage.Text)
		if requestID == "" {
			c.logger.Info("telegram reply ignored",
				"update_id", update.UpdateID,
				"chat_id", message.Chat.ID,
				"message_id", message.MessageID,
				"reply_to_message_id", message.ReplyToMessage.MessageID,
				"reason", "no correlation",
			)
			return nil
		}
	}
	if err := c.replies.SubmitReply(requestID, message.Text); err != nil {
		return fmt.Errorf("submit reply for %s: %w", requestID, err)
	}
	c.logger.Info("telegram reply accepted",
		"update_id", update.UpdateID,
		"chat_id", message.Chat.ID,
		"message_id", message.MessageID,
		"reply_to_message_id", message.ReplyToMessage.MessageID,
		"request_id", requestID,
	)
	return nil
}

func requestIDFromTaskText(text string) string {
	const marker = "SotAPI-Request-ID:"
	markerIndex := strings.LastIndex(text, marker)
	if markerIndex < 0 {
		return ""
	}
	value := strings.TrimSpace(text[markerIndex+len(marker):])
	if newline := strings.IndexByte(value, '\n'); newline >= 0 {
		value = strings.TrimSpace(value[:newline])
	}
	return value
}

func waitForRetry(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
}
