package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/availability"
	"github.com/Xbai-hang/sotapi/internal/channel/telegram"
	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/routing"
	"github.com/Xbai-hang/sotapi/internal/stats"
)

func TestEndToEndFallbackStartsWhenTimeoutTakesHumanOffline(t *testing.T) {
	users := []routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}}
	router, err := routing.NewRouter(
		[]routing.Model{{ID: "human", PoolID: "pool"}},
		[]routing.Pool{{ID: "pool", UserIDs: []string{"alice"}}},
		users,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := availability.NewStore(users, availability.Config{Enabled: true, AfterMissedReplies: 2})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := completion.NewTemplateFallback("fallback answer")
	if err != nil {
		t.Fatal(err)
	}
	deliverer := &timeoutIntegrationDeliverer{}
	service, err := completion.NewService(router, deliverer, nil, state, fallback, completion.ServiceConfig{RequestTimeout: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	handler := mustHandler(t, service, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})

	for requestNumber := 1; requestNumber <= 3; requestNumber++ {
		response := performRequest(handler, http.MethodPost, validRequestBody(false), "Bearer secret", "application/json")
		if requestNumber == 1 && (response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "request_timeout")) {
			t.Fatalf("request %d response = %d %s", requestNumber, response.Code, response.Body.String())
		}
		if requestNumber > 1 && (response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "fallback answer")) {
			t.Fatalf("request %d response = %d %s", requestNumber, response.Code, response.Body.String())
		}
	}
	if deliverer.deliveries.Load() != 2 {
		t.Fatalf("human deliveries = %d, want 2", deliverer.deliveries.Load())
	}
}

func TestEndToEndHTTPDeliveryAndTelegramReply(t *testing.T) {
	sentText := make(chan string)
	updateServed := make(chan struct{})
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/deleteWebhook"):
			writeIntegrationJSON(t, writer, map[string]any{"ok": true, "result": true})
		case strings.HasSuffix(request.URL.Path, "/sendMessage"):
			// Make the update available before sendMessage returns. This exercises
			// the delivery/reply correlation barrier rather than a friendly order.
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode sendMessage: %v", err)
				return
			}
			sentText <- payload.Text
			select {
			case <-updateServed:
			case <-time.After(time.Second):
				t.Error("getUpdates did not observe the sent message")
			}
			writeIntegrationJSON(t, writer, map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 42,
					"chat":       map[string]any{"id": 123},
				},
			})
		case strings.HasSuffix(request.URL.Path, "/getUpdates"):
			var payload struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode getUpdates: %v", err)
				return
			}
			if payload.Offset > 0 {
				<-request.Context().Done()
				return
			}
			var taskText string
			select {
			case taskText = <-sentText:
			case <-request.Context().Done():
				return
			}
			writeIntegrationJSON(t, writer, map[string]any{
				"ok": true,
				"result": []any{map[string]any{
					"update_id": 1,
					"message": map[string]any{
						"message_id": 43,
						"chat":       map[string]any{"id": 123},
						"text":       "integration answer",
						"reply_to_message": map[string]any{
							"message_id": 42,
							"chat":       map[string]any{"id": 123},
							"text":       taskText,
						},
					},
				}},
			})
			close(updateServed)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer telegramServer.Close()

	router, err := routing.NewRouter(
		[]routing.Model{{ID: "human", PoolID: "pool"}},
		[]routing.Pool{{ID: "pool", UserIDs: []string{"alice"}}},
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
	)
	if err != nil {
		t.Fatalf("routing.NewRouter() error = %v", err)
	}
	statistics, err := stats.NewStore(3)
	if err != nil {
		t.Fatalf("stats.NewStore() error = %v", err)
	}
	forwarder := &integrationReplyForwarder{}
	telegramClient, err := telegram.NewClient(telegram.Config{
		BotToken:      "token",
		APIBaseURL:    telegramServer.URL,
		PollTimeout:   time.Second,
		RetryInterval: time.Millisecond,
	}, telegramServer.Client(), forwarder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("telegram.NewClient() error = %v", err)
	}
	state, err := availability.NewStore(
		[]routing.User{{ID: "alice", Channel: "telegram", Recipient: "123"}},
		availability.Config{Enabled: true, AfterMissedReplies: 3},
	)
	if err != nil {
		t.Fatalf("availability.NewStore() error = %v", err)
	}
	fallback, err := completion.NewTemplateFallback("fallback answer")
	if err != nil {
		t.Fatalf("completion.NewTemplateFallback() error = %v", err)
	}
	service, err := completion.NewService(router, telegramClient, statistics, state, fallback, completion.ServiceConfig{
		RequestTimeout:    time.Second,
		ReasoningTemplate: "human thinking",
	})
	if err != nil {
		t.Fatalf("completion.NewService() error = %v", err)
	}
	forwarder.service = service
	handler := mustHandler(t, service, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})

	pollContext, cancelPolling := context.WithCancel(context.Background())
	pollResult := make(chan error, 1)
	go func() { pollResult <- telegramClient.Run(pollContext) }()

	responseChannel := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseChannel <- performRequest(handler, http.MethodPost, validRequestBody(false), "Bearer secret", "application/json")
	}()
	response := receiveIntegration(t, responseChannel)
	cancelPolling()
	if err := receiveIntegration(t, pollResult); err != nil {
		t.Fatalf("telegram Run() error = %v", err)
	}

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload completionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Choices[0].Message.Content != "integration answer" || payload.Choices[0].Message.ReasoningContent != "human thinking" {
		t.Fatalf("completion response = %#v", payload)
	}
	snapshot, exists := statistics.All()["alice"]
	if !exists || snapshot.Responded != 1 || snapshot.Unanswered != 0 {
		t.Fatalf("statistics = %#v, exists=%v", snapshot, exists)
	}
}

type integrationReplyForwarder struct {
	service *completion.Service
}

type timeoutIntegrationDeliverer struct {
	deliveries atomic.Int32
}

func (d *timeoutIntegrationDeliverer) Deliver(_ context.Context, _ routing.Target, task completion.Task) (completion.Delivery, error) {
	d.deliveries.Add(1)
	return completion.Delivery{ID: task.RequestID}, nil
}

func (*timeoutIntegrationDeliverer) Forget(completion.Delivery) {}

func (*timeoutIntegrationDeliverer) Notify(context.Context, routing.Target, completion.Notification) {
}

func (f *integrationReplyForwarder) SubmitReply(requestID, content string) error {
	if f.service == nil {
		return errors.New("service is not ready")
	}
	return f.service.SubmitReply(requestID, content)
}

func (f *integrationReplyForwarder) SetOnline(channel, recipient string) error {
	if f.service == nil {
		return errors.New("service is not ready")
	}
	_, err := f.service.SetOnline(channel, recipient)
	return err
}

func writeIntegrationJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode Telegram response: %v", err)
	}
}

func receiveIntegration[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for integration result")
		var zero T
		return zero
	}
}
