package telegram

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

const webhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

func (c *Client) configureUpdateMode(ctx context.Context) error {
	var acknowledged bool
	switch c.config.UpdateMode {
	case UpdateModePolling:
		request := deleteWebhookRequest{DropPendingUpdates: c.config.DropPendingUpdates}
		if err := c.call(ctx, "deleteWebhook", request, &acknowledged); err != nil {
			return fmt.Errorf("telegram: switch to polling: %w", err)
		}
	case UpdateModeWebhook:
		request := setWebhookRequest{
			URL:                c.config.WebhookURL,
			AllowedUpdates:     []string{"message"},
			DropPendingUpdates: c.config.DropPendingUpdates,
			SecretToken:        c.config.WebhookSecretToken,
		}
		if err := c.call(ctx, "setWebhook", request, &acknowledged); err != nil {
			return fmt.Errorf("telegram: switch to webhook: %w", err)
		}
	}
	if !acknowledged {
		return fmt.Errorf("telegram: %s registration was not acknowledged", c.config.UpdateMode)
	}
	return nil
}

// ServeHTTP receives one Telegram webhook update. The handler is active only
// in webhook mode and authenticates Telegram's configured secret-token header.
func (c *Client) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if c.config.UpdateMode != UpdateModeWebhook || request.URL.Path != c.webhookPath {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "only POST is supported", http.StatusMethodNotAllowed)
		return
	}
	if !c.authorizedWebhook(request.Header.Get(webhookSecretHeader)) {
		http.Error(writer, "invalid Telegram webhook secret", http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(writer, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxAPIResponse)
	decoder := json.NewDecoder(request.Body)
	var update telegramUpdate
	if err := decoder.Decode(&update); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			http.Error(writer, "Telegram update is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(writer, "invalid Telegram update", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(writer, "request body must contain one Telegram update", http.StatusBadRequest)
		return
	}

	if err := c.handleUpdateContext(request.Context(), update); err != nil {
		c.logUpdateError(request.Context(), update.UpdateID, err)
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (c *Client) authorizedWebhook(secret string) bool {
	provided := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(provided[:], c.webhookSecretHash[:]) == 1
}

type setWebhookRequest struct {
	URL                string   `json:"url"`
	AllowedUpdates     []string `json:"allowed_updates"`
	DropPendingUpdates bool     `json:"drop_pending_updates"`
	SecretToken        string   `json:"secret_token"`
}

type deleteWebhookRequest struct {
	DropPendingUpdates bool `json:"drop_pending_updates"`
}
