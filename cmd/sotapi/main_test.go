package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/availability"
	"github.com/Xbai-hang/sotapi/internal/channel/telegram"
	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/config"
	"github.com/Xbai-hang/sotapi/internal/routing"
	"github.com/Xbai-hang/sotapi/internal/stats"
)

func TestRunStartsAndStopsConfiguredServices(t *testing.T) {
	requestSeen := make(chan struct{})
	var once sync.Once
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/getUpdates") {
			_, _ = writer.Write([]byte(`{"ok":true,"result":[]}`))
			once.Do(func() { close(requestSeen) })
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer telegramServer.Close()
	configPath := writeMainConfig(t, telegramServer.URL)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- run(ctx, configPath) }()
	select {
	case <-requestSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("Telegram polling did not start")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not stop after cancellation")
	}
}

func TestRootCommandAndDefaultConfigPath(t *testing.T) {
	t.Setenv("SOTAPI_CONFIG", "/tmp/from-environment.yaml")
	if path := defaultConfigPath(); path != "/tmp/from-environment.yaml" {
		t.Fatalf("defaultConfigPath() = %q", path)
	}
	t.Setenv("SOTAPI_CONFIG", "")
	if path := defaultConfigPath(); path != "configs/config.yaml" {
		t.Fatalf("defaultConfigPath() = %q", path)
	}

	command := newRootCommand()
	command.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml")})
	if err := command.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "config: read") {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	command = newRootCommand()
	command.SetArgs([]string{"unexpected-argument"})
	if err := command.ExecuteContext(context.Background()); err == nil {
		t.Fatal("ExecuteContext(extra argument) succeeded")
	}
}

func TestConfigCommandValidates(t *testing.T) {
	command := newRootCommand()
	command.SetArgs([]string{"config", "validate", "--config", writeWebhookConfig(t, "https://sotapi.example.com")})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestConfigCommandRejectsInvalidRouting(t *testing.T) {
	routingPath := writeWebhookConfig(t, "https://sotapi.example.com")
	routingContent, err := os.ReadFile(routingPath)
	if err != nil {
		t.Fatalf("read routing config: %v", err)
	}
	routingContent = bytes.Replace(routingContent, []byte("users: [alice]"), []byte("users: [missing]"), 1)
	if err := os.WriteFile(routingPath, routingContent, 0o600); err != nil {
		t.Fatalf("write routing config: %v", err)
	}
	command := newRootCommand()
	command.SetArgs([]string{"config", "validate", "--config", routingPath})
	if err := command.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("routing validation error = %v", err)
	}
}

func TestBuildRouterMapsConfiguration(t *testing.T) {
	router, err := buildRouter(config.Config{
		Models: []config.ModelConfig{{ID: "human", PoolID: "friends"}},
		Pools:  []config.PoolConfig{{ID: "friends", UserIDs: []string{"alice"}}},
		Users:  []config.UserConfig{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	})
	if err != nil {
		t.Fatalf("buildRouter() error = %v", err)
	}
	target, err := router.Resolve("human")
	if err != nil || target.User.ID != "alice" || target.User.Recipient != "123" {
		t.Fatalf("Resolve() = %#v, %v", target, err)
	}
}

func TestBuildHTTPHandlerRoutesWebhookByExactPath(t *testing.T) {
	telegramClient := newMainWebhookClient(t, "https://sotapi.example.com/webhooks/{telegram}")
	chatHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
	})
	modelsHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusCreated)
	})
	handler, err := buildHTTPHandler(chatHandler, modelsHandler, telegramClient)
	if err != nil {
		t.Fatalf("buildHTTPHandler() error = %v", err)
	}

	webhookRequest := httptest.NewRequest(http.MethodPost, "/webhooks/{telegram}", strings.NewReader(`{"update_id":1}`))
	webhookRequest.Header.Set("Content-Type", "application/json")
	webhookRequest.Header.Set("X-Telegram-Bot-Api-Secret-Token", "webhook-secret")
	webhookResponse := httptest.NewRecorder()
	handler.ServeHTTP(webhookResponse, webhookRequest)
	if webhookResponse.Code != http.StatusNoContent {
		t.Fatalf("webhook status = %d, body = %s", webhookResponse.Code, webhookResponse.Body.String())
	}

	modelsResponse := httptest.NewRecorder()
	handler.ServeHTTP(modelsResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsResponse.Code != http.StatusCreated {
		t.Fatalf("models status = %d", modelsResponse.Code)
	}
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, healthPath, nil))
	if healthResponse.Code != http.StatusOK || healthResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("health response = %d, headers = %v", healthResponse.Code, healthResponse.Header())
	}

	// A ServeMux pattern would treat {telegram} as a wildcard. Exact dispatch
	// must leave a different path unmatched.
	unmatchedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unmatchedResponse, httptest.NewRequest(http.MethodPost, "/webhooks/not-telegram", nil))
	if unmatchedResponse.Code != http.StatusNotFound {
		t.Fatalf("unmatched status = %d", unmatchedResponse.Code)
	}
}

func TestBuildHTTPHandlerRejectsWebhookAPIPathConflict(t *testing.T) {
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, path := range []string{healthPath, "/v1/models", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			telegramClient := newMainWebhookClient(t, "https://sotapi.example.com"+path)
			if _, err := buildHTTPHandler(noop, noop, telegramClient); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("buildHTTPHandler() error = %v", err)
			}
		})
	}
}

func TestReplyForwarderDelegatesToService(t *testing.T) {
	forwarder := &replyForwarder{}
	if err := forwarder.SubmitReply("request", "answer"); err == nil {
		t.Fatal("SubmitReply() before service initialization succeeded")
	}
	router, err := routing.NewRouter(
		[]routing.Model{{ID: "human", PoolID: "pool"}},
		[]routing.Pool{{ID: "pool", UserIDs: []string{"alice"}}},
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	)
	if err != nil {
		t.Fatalf("routing.NewRouter() error = %v", err)
	}
	deliverer := &mainDeliverer{tasks: make(chan completion.Task, 1)}
	state, err := availability.NewStore(
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
		availability.Config{Enabled: true, AfterMissedReplies: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := completion.NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	service, err := completion.NewService(router, deliverer, nil, state, fallback, completion.ServiceConfig{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("completion.NewService() error = %v", err)
	}
	forwarder.service = service
	result := make(chan error, 1)
	go func() {
		_, err := service.Complete(context.Background(), completion.Request{
			ID:       "request",
			Model:    "human",
			Messages: []completion.Message{{Role: "user", Content: "hello"}},
		})
		result <- err
	}()
	select {
	case <-deliverer.tasks:
	case <-time.After(time.Second):
		t.Fatal("task was not delivered")
	}
	if err := forwarder.SubmitReply("request", "answer"); err != nil {
		t.Fatalf("SubmitReply() error = %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := forwarder.SetOnline("telegram", "123"); err != nil {
		t.Fatalf("SetOnline() error = %v", err)
	}
}

func TestLogStatisticsIncludesOperationalFields(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	logStatistics(logger, map[string]stats.UserStats{
		"alice": {
			Responded:           2,
			Unanswered:          1,
			AverageResponseTime: time.Second,
			ThresholdReached:    true,
		},
	})
	for _, expected := range []string{"user_id=alice", "responded=2", "unanswered=1", "threshold_reached=true"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("log output %q does not contain %q", output.String(), expected)
		}
	}
}

type mainDeliverer struct {
	tasks chan completion.Task
}

func (d *mainDeliverer) Deliver(_ context.Context, _ routing.Target, task completion.Task) (completion.Delivery, error) {
	d.tasks <- task
	return completion.Delivery{ID: task.RequestID}, nil
}

func (*mainDeliverer) Forget(completion.Delivery) {}

func (*mainDeliverer) Notify(context.Context, routing.Target, completion.Notification) {}

func newMainWebhookClient(t *testing.T, webhookURL string) *telegram.Client {
	t.Helper()
	client, err := telegram.NewClient(telegram.Config{
		BotToken:           "token",
		APIBaseURL:         "https://api.telegram.org",
		UpdateMode:         telegram.UpdateModeWebhook,
		PollTimeout:        time.Second,
		RetryInterval:      time.Second,
		WebhookURL:         webhookURL,
		WebhookSecretToken: "webhook-secret",
	}, nil, &replyForwarder{}, nil)
	if err != nil {
		t.Fatalf("telegram.NewClient() error = %v", err)
	}
	return client
}

func writeMainConfig(t *testing.T, telegramURL string) string {
	t.Helper()
	content := `
server:
  listen: "127.0.0.1:0"
  base_url: "http://127.0.0.1"
  shutdown_timeout: 1s
auth:
  mode: api_key
  api_keys: [secret]
human:
  response_timeout: 1s
  reasoning_template: waiting
  auto_offline:
    enabled: true
    after_missed_replies: 3
fallback:
  mode: template
  template: fallback answer
telegram:
  bot_token: token
  api_base_url: "` + telegramURL + `"
  poll_timeout: 1s
  retry_interval: 1ms
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
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func writeWebhookConfig(t *testing.T, baseURL string) string {
	t.Helper()
	content := `
server:
  base_url: "` + baseURL + `"
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
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write deployment config: %v", err)
	}
	return path
}
