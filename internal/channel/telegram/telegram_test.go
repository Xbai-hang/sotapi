package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Xbai-hang/sotapi/internal/completion"
	"github.com/Xbai-hang/sotapi/internal/routing"
)

func TestDeliverSendsTaskAndRegistersCorrelation(t *testing.T) {
	requests := make(chan sendMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bottoken/sendMessage" || request.Method != http.MethodPost {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload sendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests <- payload
		writeTelegramResult(t, writer, telegramMessage{MessageID: 42, Chat: telegramChat{ID: 123}})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, &fakeReplyHandler{})
	delivery, err := client.Deliver(context.Background(), telegramTarget(), completion.Task{
		RequestID: "request-1",
		Model:     "human",
		Messages:  []completion.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	payload := receiveTelegram(t, requests)
	if payload.ChatID != "configured-recipient" || !strings.Contains(payload.Text, "request-1") || !strings.Contains(payload.Text, "[user]\nhello") {
		t.Fatalf("sendMessage payload = %#v", payload)
	}
	if delivery.ID != "123:42" || correlationCount(client.correlations) != 1 {
		t.Fatalf("delivery = %#v, correlations = %d", delivery, correlationCount(client.correlations))
	}
	client.Forget(delivery)
	if count := correlationCount(client.correlations); count != 0 {
		t.Fatalf("correlations after Forget() = %d", count)
	}
}

func TestDeliverSplitsLongUnicodeTaskAndCorrelatesFinalMessage(t *testing.T) {
	var messageID atomic.Int32
	texts := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload sendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		texts <- payload.Text
		id := int(messageID.Add(1))
		writeTelegramResult(t, writer, telegramMessage{MessageID: id, Chat: telegramChat{ID: 456}})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, &fakeReplyHandler{})
	delivery, err := client.Deliver(context.Background(), telegramTarget(), completion.Task{
		RequestID: "long",
		Model:     "human",
		Messages:  []completion.Message{{Role: "user", Content: strings.Repeat("你", maxMessageRunes+100)}},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	close(texts)
	var parts []string
	for text := range texts {
		parts = append(parts, text)
		if utf8.RuneCountInString(text) > maxMessageRunes {
			t.Fatalf("message contains %d runes", utf8.RuneCountInString(text))
		}
		if !strings.Contains(text, "SotAPI-Request-ID: long") {
			t.Fatalf("split message has no request ID: %q", text)
		}
	}
	if len(parts) < 2 || !strings.Contains(parts[len(parts)-1], "请直接回复此消息") {
		t.Fatalf("split messages = %#v", parts)
	}
	if delivery.ID != "456:"+strconv.Itoa(len(parts)) {
		t.Fatalf("delivery ID = %q", delivery.ID)
	}
}

func TestDeliverRejectsUnsupportedChannelAndAPIErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": "chat not found",
		})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, &fakeReplyHandler{})

	target := telegramTarget()
	target.User.Channel = "email"
	if _, err := client.Deliver(context.Background(), target, completion.Task{}); err == nil {
		t.Fatal("Deliver(unsupported channel) succeeded")
	}
	if calls.Load() != 0 {
		t.Fatalf("API calls = %d, want 0", calls.Load())
	}
	target.User.Channel = "telegram"
	if _, err := client.Deliver(context.Background(), target, completion.Task{RequestID: "request"}); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("Deliver(API error) error = %v", err)
	}
	if count := correlationCount(client.correlations); count != 0 {
		t.Fatalf("correlations = %d, want 0", count)
	}
}

func TestTelegramTransportErrorDoesNotLeakBotToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := server.URL
	server.Close()
	client := newTestClient(t, baseURL, &fakeReplyHandler{})
	_, err := client.Deliver(context.Background(), telegramTarget(), completion.Task{RequestID: "request"})
	if err == nil {
		t.Fatal("Deliver() succeeded against a closed server")
	}
	if strings.Contains(err.Error(), "token") {
		t.Fatalf("transport error leaks bot token: %v", err)
	}
}

func TestHandleUpdateConsumesCorrelatedTextReply(t *testing.T) {
	replies := &fakeReplyHandler{submissions: make(chan submittedReply, 1)}
	client := clientWithoutServer(t, replies)
	if _, err := client.correlations.register(100, 20, "request-1"); err != nil {
		t.Fatalf("register() error = %v", err)
	}
	update := telegramUpdate{
		UpdateID: 7,
		Message: &telegramMessage{
			MessageID:      21,
			Chat:           telegramChat{ID: 100},
			Text:           "human answer",
			ReplyToMessage: &telegramMessage{MessageID: 20},
		},
	}
	if err := client.handleUpdate(update); err != nil {
		t.Fatalf("handleUpdate() error = %v", err)
	}
	submission := receiveTelegram(t, replies.submissions)
	if submission.requestID != "request-1" || submission.content != "human answer" {
		t.Fatalf("submission = %#v", submission)
	}
	if count := correlationCount(client.correlations); count != 0 {
		t.Fatalf("correlations = %d, want 0", count)
	}
	// Telegram may redeliver an update; the consumed correlation makes it a no-op.
	if err := client.handleUpdate(update); err != nil {
		t.Fatalf("duplicate handleUpdate() error = %v", err)
	}
	select {
	case duplicate := <-replies.submissions:
		t.Fatalf("duplicate submission = %#v", duplicate)
	default:
	}
}

func TestHandleUpdateRecoversRequestIDFromEarlyReply(t *testing.T) {
	replies := &fakeReplyHandler{submissions: make(chan submittedReply, 1)}
	client := clientWithoutServer(t, replies)
	update := telegramUpdate{Message: &telegramMessage{
		Chat: telegramChat{ID: 100},
		Text: "fast answer",
		ReplyToMessage: &telegramMessage{
			MessageID: 20,
			Text:      "task\nSotAPI-Request-ID: request-early",
		},
	}}
	if err := client.handleUpdate(update); err != nil {
		t.Fatalf("handleUpdate() error = %v", err)
	}
	submission := receiveTelegram(t, replies.submissions)
	if submission.requestID != "request-early" || submission.content != "fast answer" {
		t.Fatalf("submission = %#v", submission)
	}
}

func TestHandleUpdateIgnoresUnrelatedMessages(t *testing.T) {
	replies := &fakeReplyHandler{submissions: make(chan submittedReply, 1)}
	client := clientWithoutServer(t, replies)
	tests := []telegramUpdate{
		{},
		{Message: &telegramMessage{Text: "not a reply", Chat: telegramChat{ID: 1}}},
		{Message: &telegramMessage{Text: " ", Chat: telegramChat{ID: 1}, ReplyToMessage: &telegramMessage{MessageID: 2}}},
		{Message: &telegramMessage{Text: "unknown", Chat: telegramChat{ID: 1}, ReplyToMessage: &telegramMessage{MessageID: 2}}},
	}
	for _, update := range tests {
		if err := client.handleUpdate(update); err != nil {
			t.Fatalf("handleUpdate(%#v) error = %v", update, err)
		}
	}
	select {
	case submission := <-replies.submissions:
		t.Fatalf("unexpected submission = %#v", submission)
	default:
	}
}

func TestHandleUpdateOnlineCommandRestoresConfiguredRecipient(t *testing.T) {
	messages := make(chan sendMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload sendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode sendMessage: %v", err)
		}
		messages <- payload
		writeTelegramResult(t, writer, telegramMessage{MessageID: 8, Chat: telegramChat{ID: 100}})
	}))
	defer server.Close()

	replies := &fakeReplyHandler{online: make(chan onlineCommand, 1)}
	client := newTestClient(t, server.URL, replies)
	if err := client.handleUpdate(telegramUpdate{Message: &telegramMessage{Chat: telegramChat{ID: 100}, Text: " /online "}}); err != nil {
		t.Fatalf("handleUpdate() error = %v", err)
	}
	command := receiveTelegram(t, replies.online)
	if command.channel != "telegram" || command.recipient != "100" {
		t.Fatalf("online command = %#v", command)
	}
	message := receiveTelegram(t, messages)
	if message.ChatID != "100" || !strings.Contains(message.Text, "恢复在线") {
		t.Fatalf("confirmation message = %#v", message)
	}
}

func TestNotifyFormatsAutoOfflineMessage(t *testing.T) {
	messages := make(chan sendMessageRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload sendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode sendMessage: %v", err)
		}
		messages <- payload
		writeTelegramResult(t, writer, telegramMessage{MessageID: 9, Chat: telegramChat{ID: 100}})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, &fakeReplyHandler{})

	client.Notify(context.Background(), telegramTarget(), completion.Notification{
		Kind:          completion.NotificationAutoOffline,
		MissedReplies: 3,
	})
	message := receiveTelegram(t, messages)
	if message.ChatID != "configured-recipient" || !strings.Contains(message.Text, "3") || !strings.Contains(message.Text, "/online") {
		t.Fatalf("offline notification = %#v", message)
	}
}

func TestHandleUpdateReturnsReplyHandlerError(t *testing.T) {
	replyError := errors.New("request expired")
	replies := &fakeReplyHandler{err: replyError}
	client := clientWithoutServer(t, replies)
	if _, err := client.correlations.register(1, 2, "request"); err != nil {
		t.Fatalf("register() error = %v", err)
	}
	err := client.handleUpdate(telegramUpdate{Message: &telegramMessage{
		Chat:           telegramChat{ID: 1},
		Text:           "answer",
		ReplyToMessage: &telegramMessage{MessageID: 2},
	}})
	if !errors.Is(err, replyError) {
		t.Fatalf("handleUpdate() error = %v", err)
	}
	if count := correlationCount(client.correlations); count != 0 {
		t.Fatalf("correlations = %d, want consumed", count)
	}
}

func TestGetUpdatesSendsOffsetAndDecodesMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bottoken/getUpdates" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var payload getUpdatesRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Offset != 11 || payload.Timeout != 1 || len(payload.AllowedUpdates) != 1 || payload.AllowedUpdates[0] != "message" {
			t.Fatalf("getUpdates payload = %#v", payload)
		}
		writeTelegramResult(t, writer, []telegramUpdate{{UpdateID: 11, Message: &telegramMessage{Text: "answer"}}})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, &fakeReplyHandler{})

	updates, err := client.getUpdates(context.Background(), 11)
	if err != nil {
		t.Fatalf("getUpdates() error = %v", err)
	}
	if len(updates) != 1 || updates[0].UpdateID != 11 || updates[0].Message.Text != "answer" {
		t.Fatalf("updates = %#v", updates)
	}
}

func TestRunRetriesPollingAndStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pollingCalls atomic.Int32
	replies := &cancelingReplyHandler{cancel: cancel}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/bottoken/deleteWebhook" {
			writeTelegramResult(t, writer, true)
			return
		}
		if request.URL.Path != "/bottoken/getUpdates" {
			http.NotFound(writer, request)
			return
		}
		call := pollingCalls.Add(1)
		if call == 1 {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 500, "description": "temporary"})
			return
		}
		writeTelegramResult(t, writer, []telegramUpdate{{
			UpdateID: 5,
			Message: &telegramMessage{
				Chat:           telegramChat{ID: 9},
				Text:           "answer",
				ReplyToMessage: &telegramMessage{MessageID: 8},
			},
		}})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, replies)
	client.config.RetryInterval = time.Millisecond
	if _, err := client.correlations.register(9, 8, "request"); err != nil {
		t.Fatalf("register() error = %v", err)
	}

	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if pollingCalls.Load() < 2 || replies.requestID != "request" || replies.content != "answer" {
		t.Fatalf("polling calls=%d reply=%s/%s", pollingCalls.Load(), replies.requestID, replies.content)
	}
}

func TestNewClientValidation(t *testing.T) {
	valid := Config{BotToken: "token", APIBaseURL: "https://api.telegram.org", PollTimeout: time.Second, RetryInterval: time.Second}
	replies := &fakeReplyHandler{}
	tests := []struct {
		name    string
		config  Config
		replies ReplyHandler
	}{
		{name: "token", config: Config{APIBaseURL: valid.APIBaseURL, PollTimeout: time.Second, RetryInterval: time.Second}, replies: replies},
		{name: "URL", config: Config{BotToken: "token", APIBaseURL: "://bad", PollTimeout: time.Second, RetryInterval: time.Second}, replies: replies},
		{name: "poll timeout", config: Config{BotToken: "token", APIBaseURL: valid.APIBaseURL, RetryInterval: time.Second}, replies: replies},
		{name: "retry interval", config: Config{BotToken: "token", APIBaseURL: valid.APIBaseURL, PollTimeout: time.Second}, replies: replies},
		{name: "reply handler", config: valid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewClient(test.config, nil, test.replies, nil); err == nil {
				t.Fatal("NewClient() succeeded")
			}
		})
	}
	valid.APIBaseURL = ""
	client, err := NewClient(valid, nil, replies, nil)
	if err != nil || client.config.APIBaseURL != defaultAPIBaseURL {
		t.Fatalf("NewClient(default URL) = %#v, %v", client, err)
	}
}

func TestCorrelationStoreConsumesExactlyOnce(t *testing.T) {
	store := newCorrelationStore()
	key, err := store.register(1, 2, "request")
	if err != nil || key != "1:2" {
		t.Fatalf("register() = %q, %v", key, err)
	}
	if _, err := store.register(1, 2, "other"); err == nil {
		t.Fatal("duplicate register() succeeded")
	}
	if requestID, exists := store.consume(1, 2); !exists || requestID != "request" {
		t.Fatalf("consume() = %q, %v", requestID, exists)
	}
	if _, exists := store.consume(1, 2); exists {
		t.Fatal("second consume() succeeded")
	}
	store.forget("missing")
}

func TestFormatTaskPartsRejectsInvalidRequestID(t *testing.T) {
	if _, err := formatTaskParts(telegramTarget(), completion.Task{}); err == nil {
		t.Fatal("formatTaskParts(empty request ID) succeeded")
	}
	if _, err := formatTaskParts(telegramTarget(), completion.Task{RequestID: strings.Repeat("x", maxMessageRunes)}); err == nil {
		t.Fatal("formatTaskParts(oversized request ID) succeeded")
	}
}

type submittedReply struct {
	requestID string
	content   string
}

type fakeReplyHandler struct {
	submissions chan submittedReply
	online      chan onlineCommand
	err         error
}

type onlineCommand struct {
	channel   string
	recipient string
}

func (h *fakeReplyHandler) SubmitReply(requestID, content string) error {
	if h.submissions != nil {
		h.submissions <- submittedReply{requestID: requestID, content: content}
	}
	return h.err
}

func (h *fakeReplyHandler) SetOnline(channel, recipient string) error {
	if h.online != nil {
		h.online <- onlineCommand{channel: channel, recipient: recipient}
	}
	return h.err
}

type cancelingReplyHandler struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	requestID string
	content   string
}

func (h *cancelingReplyHandler) SubmitReply(requestID, content string) error {
	h.mu.Lock()
	h.requestID = requestID
	h.content = content
	h.mu.Unlock()
	h.cancel()
	return nil
}

func (*cancelingReplyHandler) SetOnline(string, string) error { return nil }

func newTestClient(t *testing.T, baseURL string, replies ReplyHandler) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BotToken:      "token",
		APIBaseURL:    baseURL,
		PollTimeout:   time.Second,
		RetryInterval: time.Millisecond,
	}, nil, replies, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func clientWithoutServer(t *testing.T, replies ReplyHandler) *Client {
	t.Helper()
	return newTestClient(t, "https://api.telegram.org", replies)
}

func telegramTarget() routing.Target {
	return routing.Target{
		User: routing.User{
			ID:        "alice",
			Channel:   "telegram",
			Recipient: "configured-recipient",
		},
	}
}

func writeTelegramResult(t *testing.T, writer http.ResponseWriter, result any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func receiveTelegram[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func correlationCount(store *correlationStore) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.requests)
}
