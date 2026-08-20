package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunPollingDeletesWebhookBeforeGetUpdates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method := strings.TrimPrefix(request.URL.Path, "/bottoken/")
		mu.Lock()
		methods = append(methods, method)
		mu.Unlock()
		switch method {
		case "deleteWebhook":
			var payload deleteWebhookRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.DropPendingUpdates {
				t.Errorf("deleteWebhook payload = %#v, error = %v", payload, err)
			}
			writeTelegramResult(t, writer, true)
		case "getUpdates":
			writeTelegramResult(t, writer, []telegramUpdate{})
			cancel()
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newModeTestClient(t, server.URL, Config{UpdateMode: UpdateModePolling}, &fakeReplyHandler{})
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) < 2 || methods[0] != "deleteWebhook" || methods[1] != "getUpdates" {
		t.Fatalf("Bot API methods = %#v", methods)
	}
}

func TestWebhookRegistration(t *testing.T) {
	registration := make(chan setWebhookRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bottoken/setWebhook" {
			http.NotFound(writer, request)
			return
		}
		var payload setWebhookRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode setWebhook: %v", err)
			return
		}
		registration <- payload
		writeTelegramResult(t, writer, true)
	}))
	defer server.Close()

	client := newModeTestClient(t, server.URL, Config{
		UpdateMode:         UpdateModeWebhook,
		DropPendingUpdates: true,
		WebhookURL:         "https://sotapi.example.com/hooks/telegram",
		WebhookSecretToken: "webhook-secret_1",
	}, &fakeReplyHandler{})
	if err := client.configureUpdateMode(context.Background()); err != nil {
		t.Fatalf("configureUpdateMode() error = %v", err)
	}
	payload := receiveTelegram(t, registration)
	if payload.URL != "https://sotapi.example.com/hooks/telegram" || payload.SecretToken != "webhook-secret_1" || !payload.DropPendingUpdates || len(payload.AllowedUpdates) != 1 || payload.AllowedUpdates[0] != "message" {
		t.Fatalf("setWebhook payload = %#v", payload)
	}
	if client.WebhookPath() != "/hooks/telegram" {
		t.Fatalf("WebhookPath() = %q", client.WebhookPath())
	}
}

func TestWebhookDerivesStableAutomaticSecret(t *testing.T) {
	client := newModeTestClient(t, "https://api.telegram.org", Config{
		UpdateMode:         UpdateModeWebhook,
		WebhookURL:         "https://sotapi.example.com/hooks/telegram",
		WebhookSecretToken: autoWebhookSecret,
	}, &fakeReplyHandler{})
	want := deriveWebhookSecret("token")
	if client.config.WebhookSecretToken != want || len(want) != 64 {
		t.Fatalf("derived webhook secret length = %d, matches = %v", len(client.config.WebhookSecretToken), client.config.WebhookSecretToken == want)
	}
	if !client.authorizedWebhook(want) || client.authorizedWebhook(autoWebhookSecret) {
		t.Fatal("automatic webhook secret authorization mismatch")
	}
}

func TestRunReturnsUnacknowledgedRegistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTelegramResult(t, writer, false)
	}))
	defer server.Close()
	client := newModeTestClient(t, server.URL, Config{
		UpdateMode: UpdateModePolling,
	}, &fakeReplyHandler{})
	if err := client.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "not acknowledged") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWebhookHandlerSubmitsAuthenticatedReply(t *testing.T) {
	replies := &fakeReplyHandler{submissions: make(chan submittedReply, 1)}
	client := newModeTestClient(t, "https://api.telegram.org", Config{
		UpdateMode:         UpdateModeWebhook,
		WebhookURL:         "https://sotapi.example.com/hooks/telegram",
		WebhookSecretToken: "webhook-secret_1",
	}, replies)
	if _, err := client.correlations.register(100, 20, "request-1"); err != nil {
		t.Fatalf("register() error = %v", err)
	}
	body := `{"update_id":7,"message":{"message_id":21,"chat":{"id":100},"text":"human answer","reply_to_message":{"message_id":20}}}`
	request := httptest.NewRequest(http.MethodPost, client.WebhookPath(), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set(webhookSecretHeader, "webhook-secret_1")
	response := httptest.NewRecorder()
	client.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	submission := receiveTelegram(t, replies.submissions)
	if submission.requestID != "request-1" || submission.content != "human answer" {
		t.Fatalf("submission = %#v", submission)
	}
}

func TestWebhookHandlerRejectsInvalidRequests(t *testing.T) {
	client := newModeTestClient(t, "https://api.telegram.org", Config{
		UpdateMode:         UpdateModeWebhook,
		WebhookURL:         "https://sotapi.example.com/hooks/telegram",
		WebhookSecretToken: "webhook-secret_1",
	}, &fakeReplyHandler{})
	validBody := `{"update_id":1}`
	tests := []struct {
		name        string
		path        string
		method      string
		secret      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "path", path: "/wrong", method: http.MethodPost, secret: "webhook-secret_1", contentType: "application/json", body: validBody, wantStatus: http.StatusNotFound},
		{name: "method", path: client.WebhookPath(), method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
		{name: "secret", path: client.WebhookPath(), method: http.MethodPost, secret: "wrong", contentType: "application/json", body: validBody, wantStatus: http.StatusUnauthorized},
		{name: "content type", path: client.WebhookPath(), method: http.MethodPost, secret: "webhook-secret_1", contentType: "text/plain", body: validBody, wantStatus: http.StatusUnsupportedMediaType},
		{name: "JSON", path: client.WebhookPath(), method: http.MethodPost, secret: "webhook-secret_1", contentType: "application/json", body: "{", wantStatus: http.StatusBadRequest},
		{name: "multiple updates", path: client.WebhookPath(), method: http.MethodPost, secret: "webhook-secret_1", contentType: "application/json", body: validBody + validBody, wantStatus: http.StatusBadRequest},
		{name: "body limit", path: client.WebhookPath(), method: http.MethodPost, secret: "webhook-secret_1", contentType: "application/json", body: `{"padding":"` + strings.Repeat("x", maxAPIResponse) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.secret != "" {
				request.Header.Set(webhookSecretHeader, test.secret)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			client.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestNewClientValidatesUpdateModes(t *testing.T) {
	replies := &fakeReplyHandler{}
	base := Config{
		BotToken:      "token",
		APIBaseURL:    "https://api.telegram.org",
		PollTimeout:   time.Second,
		RetryInterval: time.Second,
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "mode", mutate: func(config *Config) { config.UpdateMode = "both" }},
		{name: "URL", mutate: func(config *Config) {
			config.UpdateMode = UpdateModeWebhook
			config.WebhookURL = "http://example.com/hook"
			config.WebhookSecretToken = "secret"
		}},
		{name: "secret", mutate: func(config *Config) {
			config.UpdateMode = UpdateModeWebhook
			config.WebhookURL = "https://example.com/hook"
			config.WebhookSecretToken = "bad secret"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if _, err := NewClient(config, nil, replies, nil); err == nil {
				t.Fatal("NewClient() succeeded")
			}
		})
	}
}

func TestPollingClientHasNoWebhookPath(t *testing.T) {
	client := newModeTestClient(t, "https://api.telegram.org", Config{UpdateMode: UpdateModePolling}, &fakeReplyHandler{})
	if client.WebhookPath() != "" {
		t.Fatalf("WebhookPath() = %q", client.WebhookPath())
	}
	response := httptest.NewRecorder()
	client.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/hooks/telegram", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func newModeTestClient(t *testing.T, apiBaseURL string, mode Config, replies ReplyHandler) *Client {
	t.Helper()
	mode.BotToken = "token"
	mode.APIBaseURL = apiBaseURL
	mode.PollTimeout = time.Second
	mode.RetryInterval = time.Millisecond
	client, err := NewClient(mode, nil, replies, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestConfigureUpdateModePropagatesBotAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 400, "description": "registration rejected"})
	}))
	defer server.Close()
	client := newModeTestClient(t, server.URL, Config{UpdateMode: UpdateModePolling}, &fakeReplyHandler{})
	err := client.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "registration rejected") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWebhookHandlerAcknowledgesReplyHandlerError(t *testing.T) {
	client := newModeTestClient(t, "https://api.telegram.org", Config{
		UpdateMode:         UpdateModeWebhook,
		WebhookURL:         "https://sotapi.example.com/hooks/telegram",
		WebhookSecretToken: "webhook-secret_1",
	}, &fakeReplyHandler{err: errors.New("expired")})
	if _, err := client.correlations.register(100, 20, "request-1"); err != nil {
		t.Fatalf("register() error = %v", err)
	}
	body := `{"update_id":7,"message":{"chat":{"id":100},"text":"answer","reply_to_message":{"message_id":20}}}`
	request := httptest.NewRequest(http.MethodPost, client.WebhookPath(), strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webhookSecretHeader, "webhook-secret_1")
	response := httptest.NewRecorder()
	client.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}
