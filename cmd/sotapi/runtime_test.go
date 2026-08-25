package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunReloadsConfigurationAndCancelsPendingRequest(t *testing.T) {
	telegramAPI := newReloadTelegramAPI(t)
	t.Cleanup(telegramAPI.Close)
	address := availableAddress(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeRuntimeConfig(t, configPath, telegramAPI.URL, address, "old-token", "old reasoning")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, configPath) }()
	if token := receiveRuntime(t, telegramAPI.configured); token != "old-token" {
		t.Fatalf("initial configured token = %q", token)
	}

	response := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"human","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			requestError <- err
			return
		}
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		result, err := http.DefaultClient.Do(request)
		if err != nil {
			requestError <- err
			return
		}
		response <- result
	}()
	receiveRuntime(t, telegramAPI.delivered)

	writeRuntimeConfig(t, configPath, telegramAPI.URL, address, "new-token", "new reasoning")
	reloadedResponse := receiveRuntime(t, response)
	defer reloadedResponse.Body.Close()
	if reloadedResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("reload response status = %d", reloadedResponse.StatusCode)
	}
	body := readResponseBody(t, reloadedResponse)
	if !strings.Contains(body, `"code":"service_reloading"`) {
		t.Fatalf("reload response body = %s", body)
	}
	if token := receiveRuntime(t, telegramAPI.configured); token != "new-token" {
		t.Fatalf("reloaded configured token = %q", token)
	}
	waitForRuntimeHealth(t, address)
	select {
	case err := <-requestError:
		t.Fatalf("completion request error = %v", err)
	default:
	}

	cancel()
	if err := receiveRuntime(t, result); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunIgnoresInvalidAndUnchangedConfiguration(t *testing.T) {
	telegramAPI := newReloadTelegramAPI(t)
	t.Cleanup(telegramAPI.Close)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	address := availableAddress(t)
	content := runtimeConfigYAML(telegramAPI.URL, address, "token", "waiting")
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, configPath) }()
	receiveRuntime(t, telegramAPI.configured)

	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("rewrite unchanged config: %v", err)
	}
	assertNoRuntimeValue(t, telegramAPI.configured, 400*time.Millisecond)
	if err := os.WriteFile(configPath, []byte("auth: [invalid\n"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}
	assertNoRuntimeValue(t, telegramAPI.configured, 400*time.Millisecond)

	writeRuntimeConfig(t, configPath, telegramAPI.URL, address, "new-token", "waiting")
	if token := receiveRuntime(t, telegramAPI.configured); token != "new-token" {
		t.Fatalf("configured token = %q", token)
	}
	waitForRuntimeHealth(t, address)
	cancel()
	if err := receiveRuntime(t, result); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRestoresPreviousConfigurationWhenReloadCannotStart(t *testing.T) {
	telegramAPI := newReloadTelegramAPI(t)
	t.Cleanup(telegramAPI.Close)
	oldAddress := availableAddress(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy address: %v", err)
	}
	defer occupied.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeRuntimeConfig(t, configPath, telegramAPI.URL, oldAddress, "old-token", "waiting")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- run(ctx, configPath) }()
	receiveRuntime(t, telegramAPI.configured)

	writeRuntimeConfig(t, configPath, telegramAPI.URL, occupied.Addr().String(), "new-token", "waiting")
	if token := receiveRuntime(t, telegramAPI.configured); token != "old-token" {
		t.Fatalf("rollback configured token = %q", token)
	}
	waitForRuntimeHealth(t, oldAddress)
	select {
	case err := <-result:
		t.Fatalf("run() stopped after successful rollback: %v", err)
	default:
	}

	cancel()
	if err := receiveRuntime(t, result); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

type reloadTelegramAPI struct {
	*httptest.Server
	configured chan string
	delivered  chan struct{}
}

func newReloadTelegramAPI(t *testing.T) *reloadTelegramAPI {
	t.Helper()
	api := &reloadTelegramAPI{
		configured: make(chan string, 8),
		delivered:  make(chan struct{}, 8),
	}
	api.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token := telegramToken(request.URL.Path)
		switch {
		case strings.HasSuffix(request.URL.Path, "/deleteWebhook"):
			api.configured <- token
			writeRuntimeJSON(writer, `{"ok":true,"result":true}`)
		case strings.HasSuffix(request.URL.Path, "/sendMessage"):
			api.delivered <- struct{}{}
			writeRuntimeJSON(writer, `{"ok":true,"result":{"message_id":42,"chat":{"id":123}}}`)
		case strings.HasSuffix(request.URL.Path, "/getUpdates"):
			select {
			case <-request.Context().Done():
				return
			case <-time.After(25 * time.Millisecond):
				writeRuntimeJSON(writer, `{"ok":true,"result":[]}`)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	return api
}

func telegramToken(path string) string {
	value := strings.TrimPrefix(path, "/bot")
	token, _, _ := strings.Cut(value, "/")
	return token
}

func writeRuntimeJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(body))
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}
	return address
}

func waitForRuntimeHealth(t *testing.T, address string) {
	t.Helper()
	if strings.HasSuffix(address, ":0") {
		return
	}
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + healthPath)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime at %s did not become healthy", address)
}

func writeRuntimeConfig(t *testing.T, path, telegramURL, address, token, reasoning string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(runtimeConfigYAML(telegramURL, address, token, reasoning)), 0o600); err != nil {
		t.Fatalf("write runtime config: %v", err)
	}
}

func runtimeConfigYAML(telegramURL, address, token, reasoning string) string {
	return fmt.Sprintf(`
server:
  listen: %q
  base_url: "http://127.0.0.1"
  shutdown_timeout: 1s
auth:
  mode: api_key
  api_keys: [secret]
request_timeout: 5s
reasoning_template: %q
telegram:
  bot_token: %q
  api_base_url: %q
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
`, address, reasoning, token, telegramURL)
}

func receiveRuntime[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime value")
		var zero T
		return zero
	}
}

func assertNoRuntimeValue[T any](t *testing.T, channel <-chan T, duration time.Duration) {
	t.Helper()
	select {
	case value := <-channel:
		t.Fatalf("unexpected runtime value: %#v", value)
	case <-time.After(duration):
	}
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	buffer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(buffer)
}
