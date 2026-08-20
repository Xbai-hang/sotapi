package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
	openaiAuth "github.com/Xbai-hang/sotapi/internal/protocol/openai/auth"
	"github.com/Xbai-hang/sotapi/internal/routing"
)

func TestHandlerNonStreamingResponse(t *testing.T) {
	completer := &fakeCompleter{
		complete: func(_ context.Context, request completion.Request) (completion.Response, error) {
			if request.Model != "human" || len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Content != "hello" {
				t.Fatalf("completion request = %#v", request)
			}
			return completion.Response{
				ID:        "chatcmpl-test",
				Model:     request.Model,
				Reasoning: "waiting for a human",
				Content:   "human answer",
			}, nil
		},
	}
	handler := mustHandler(t, completer, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
	body := `{"model":"human","messages":[{"role":"system","content":"be useful"},{"role":"user","content":"hello"}],"temperature":0.2}`
	response := performRequest(handler, http.MethodPost, body, "Bearer secret", "application/json; charset=utf-8")

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var payload completionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != "chatcmpl-test" || payload.Object != "chat.completion" || payload.Model != "human" || payload.Created <= 0 {
		t.Fatalf("response metadata = %#v", payload)
	}
	choice := payload.Choices[0]
	if choice.Message.Role != "assistant" || choice.Message.ReasoningContent != "waiting for a human" || choice.Message.Content != "human answer" || choice.FinishReason != "stop" {
		t.Fatalf("choice = %#v", choice)
	}
}

func TestHandlerAuthenticationAndHTTPValidation(t *testing.T) {
	completer := &fakeCompleter{complete: func(context.Context, completion.Request) (completion.Response, error) {
		t.Fatal("completer must not be called")
		return completion.Response{}, nil
	}}
	handler := mustHandler(t, completer, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 64})
	tests := []struct {
		name        string
		method      string
		body        string
		auth        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "method", method: http.MethodGet, auth: "Bearer secret", contentType: "application/json", wantStatus: http.StatusMethodNotAllowed, wantCode: "method_not_allowed"},
		{name: "missing auth", method: http.MethodPost, body: `{}`, contentType: "application/json", wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key"},
		{name: "wrong scheme", method: http.MethodPost, body: `{}`, auth: "Basic secret", contentType: "application/json", wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key"},
		{name: "wrong token", method: http.MethodPost, body: `{}`, auth: "Bearer no", contentType: "application/json", wantStatus: http.StatusUnauthorized, wantCode: "invalid_api_key"},
		{name: "content type", method: http.MethodPost, body: `{}`, auth: "Bearer secret", contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType, wantCode: "invalid_content_type"},
		{name: "invalid JSON", method: http.MethodPost, body: `{`, auth: "Bearer secret", contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "multiple values", method: http.MethodPost, body: `{} {}`, auth: "Bearer secret", contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "body limit", method: http.MethodPost, body: strings.Repeat("x", 65), auth: "Bearer secret", contentType: "application/json", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(handler, test.method, test.body, test.auth, test.contentType)
			assertAPIError(t, response, test.wantStatus, test.wantCode)
			if test.wantStatus == http.StatusMethodNotAllowed && response.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow header = %q", response.Header().Get("Allow"))
			}
		})
	}
}

func TestConvertRequestAcceptsPhaseOneTextShape(t *testing.T) {
	one := 1
	request, err := convertRequest(createRequest{
		Model:          "human",
		N:              &one,
		Tools:          json.RawMessage(`[]`),
		ToolChoice:     json.RawMessage(`"none"`),
		Functions:      json.RawMessage(`null`),
		FunctionCall:   json.RawMessage(`"none"`),
		ResponseFormat: json.RawMessage(`{"type":"text"}`),
		Messages: []requestMessage{
			{Role: "developer", Content: json.RawMessage(`"instructions"`)},
			{Role: "assistant", Content: json.RawMessage(`"prior answer"`)},
			{Role: "user", Content: json.RawMessage(`"question"`)},
		},
	})
	if err != nil {
		t.Fatalf("convertRequest() error = %v", err)
	}
	if request.Model != "human" || len(request.Messages) != 3 || request.Messages[2].Content != "question" {
		t.Fatalf("request = %#v", request)
	}
}

func TestConvertRequestRejectsUnsupportedShapes(t *testing.T) {
	two := 2
	valid := func() createRequest {
		return createRequest{Model: "human", Messages: []requestMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}}
	}
	tests := []struct {
		name   string
		mutate func(*createRequest)
	}{
		{name: "model", mutate: func(r *createRequest) { r.Model = "" }},
		{name: "messages", mutate: func(r *createRequest) { r.Messages = nil }},
		{name: "multiple choices", mutate: func(r *createRequest) { r.N = &two }},
		{name: "tools", mutate: func(r *createRequest) { r.Tools = json.RawMessage(`[{"type":"function"}]`) }},
		{name: "functions", mutate: func(r *createRequest) { r.Functions = json.RawMessage(`[{"name":"f"}]`) }},
		{name: "tool choice", mutate: func(r *createRequest) { r.ToolChoice = json.RawMessage(`"auto"`) }},
		{name: "function call", mutate: func(r *createRequest) { r.FunctionCall = json.RawMessage(`{"name":"f"}`) }},
		{name: "structured output", mutate: func(r *createRequest) { r.ResponseFormat = json.RawMessage(`{"type":"json_schema"}`) }},
		{name: "bad response format", mutate: func(r *createRequest) { r.ResponseFormat = json.RawMessage(`{`) }},
		{name: "role", mutate: func(r *createRequest) { r.Messages[0].Role = "tool" }},
		{name: "content parts", mutate: func(r *createRequest) { r.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"hi"}]`) }},
		{name: "message tools", mutate: func(r *createRequest) { r.Messages[0].ToolCalls = json.RawMessage(`[{}]`) }},
		{name: "message function", mutate: func(r *createRequest) { r.Messages[0].FunctionCall = json.RawMessage(`{}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			test.mutate(&request)
			if _, err := convertRequest(request); err == nil {
				t.Fatalf("convertRequest(%#v) succeeded", request)
			}
		})
	}
}

func TestHandlerMapsCompletionErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "model", err: fmt.Errorf("%w: missing", routing.ErrModelNotFound), wantStatus: http.StatusNotFound, wantCode: "model_not_found"},
		{name: "invalid", err: fmt.Errorf("%w: bad", completion.ErrInvalidRequest), wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "timeout", err: completion.ErrRequestTimeout, wantStatus: http.StatusGatewayTimeout, wantCode: "request_timeout"},
		{name: "canceled", err: completion.ErrRequestCanceled, wantStatus: http.StatusRequestTimeout, wantCode: "request_canceled"},
		{name: "delivery", err: completion.ErrDeliveryFailed, wantStatus: http.StatusBadGateway, wantCode: "channel_delivery_failed"},
		{name: "internal", err: errors.New("unexpected"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, &fakeCompleter{complete: func(context.Context, completion.Request) (completion.Response, error) {
				return completion.Response{}, test.err
			}}, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
			response := performRequest(handler, http.MethodPost, validRequestBody(false), "Bearer secret", "application/json")
			assertAPIError(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestHandlerStreamingResponse(t *testing.T) {
	completer := &fakeCompleter{stream: func(_ context.Context, request completion.Request, emit completion.StreamEmitter) error {
		chunks := []completion.StreamChunk{
			{ID: "chatcmpl-stream", Model: request.Model, ReasoningDelta: "thinking"},
			{ID: "chatcmpl-stream", Model: request.Model, ContentDelta: "answer"},
			{ID: "chatcmpl-stream", Model: request.Model, Done: true},
		}
		for _, chunk := range chunks {
			if err := emit(chunk); err != nil {
				return err
			}
		}
		return nil
	}}
	handler := mustHandler(t, completer, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
	response := performRequest(handler, http.MethodPost, validRequestBody(true), "bearer secret", "application/json")

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	events := parseSSE(t, response.Body.String())
	if len(events) != 4 || events[3] != "[DONE]" {
		t.Fatalf("SSE events = %#v", events)
	}
	var reasoning, content, done streamResponse
	for index, target := range []*streamResponse{&reasoning, &content, &done} {
		if err := json.Unmarshal([]byte(events[index]), target); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
	}
	if reasoning.Choices[0].Delta.Role != "assistant" || reasoning.Choices[0].Delta.ReasoningContent != "thinking" {
		t.Fatalf("reasoning event = %#v", reasoning)
	}
	if content.Choices[0].Delta.Role != "" || content.Choices[0].Delta.Content != "answer" {
		t.Fatalf("content event = %#v", content)
	}
	if done.Choices[0].FinishReason == nil || *done.Choices[0].FinishReason != "stop" || done.Choices[0].Delta != (delta{}) {
		t.Fatalf("done event = %#v", done)
	}
}

func TestHandlerStreamErrorBeforeAndAfterStart(t *testing.T) {
	t.Run("before start", func(t *testing.T) {
		handler := mustHandler(t, &fakeCompleter{stream: func(context.Context, completion.Request, completion.StreamEmitter) error {
			return completion.ErrDeliveryFailed
		}}, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
		response := performRequest(handler, http.MethodPost, validRequestBody(true), "Bearer secret", "application/json")
		assertAPIError(t, response, http.StatusBadGateway, "channel_delivery_failed")
	})

	t.Run("after start", func(t *testing.T) {
		handler := mustHandler(t, &fakeCompleter{stream: func(_ context.Context, _ completion.Request, emit completion.StreamEmitter) error {
			if err := emit(completion.StreamChunk{ID: "id", Model: "human", ReasoningDelta: "thinking"}); err != nil {
				return err
			}
			return completion.ErrRequestTimeout
		}}, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
		response := performRequest(handler, http.MethodPost, validRequestBody(true), "Bearer secret", "application/json")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
		}
		events := parseSSE(t, response.Body.String())
		if len(events) != 3 || !strings.Contains(events[1], `"code":"request_timeout"`) || events[2] != "[DONE]" {
			t.Fatalf("SSE events = %#v", events)
		}
	})
}

func TestHandlerStreamSendsKeepAliveWhileWaiting(t *testing.T) {
	handler := mustHandler(t, &fakeCompleter{stream: func(_ context.Context, _ completion.Request, emit completion.StreamEmitter) error {
		if err := emit(completion.StreamChunk{ID: "id", Model: "human", ReasoningDelta: "thinking"}); err != nil {
			return err
		}
		time.Sleep(8 * time.Millisecond)
		return emit(completion.StreamChunk{ID: "id", Model: "human", Done: true})
	}}, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20, KeepAliveInterval: 2 * time.Millisecond})
	response := performRequest(handler, http.MethodPost, validRequestBody(true), "Bearer secret", "application/json")
	if !strings.Contains(response.Body.String(), ": keep-alive\n\n") {
		t.Fatalf("stream has no keep-alive comment: %s", response.Body.String())
	}
}

func TestHandlerRejectsStreamingWithoutFlusher(t *testing.T) {
	var streamCalls atomic.Int32
	handler := mustHandler(t, &fakeCompleter{stream: func(context.Context, completion.Request, completion.StreamEmitter) error {
		streamCalls.Add(1)
		return nil
	}}, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1 << 20})
	request := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(validRequestBody(true)))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	writer := &nonFlushingWriter{header: make(http.Header)}
	handler.ServeHTTP(writer, request)
	if writer.status != http.StatusInternalServerError || streamCalls.Load() != 0 {
		t.Fatalf("status=%d stream calls=%d body=%s", writer.status, streamCalls.Load(), writer.body.String())
	}
}

func TestNewHandlerValidation(t *testing.T) {
	completer := &fakeCompleter{}
	if _, err := NewHandler(nil, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1}); err == nil {
		t.Fatal("NewHandler(nil completer) succeeded")
	}
	if _, err := NewHandler(completer, HandlerConfig{MaxBodyBytes: 1}); err == nil {
		t.Fatal("NewHandler(nil authenticator) succeeded")
	}
	if _, err := NewHandler(completer, HandlerConfig{Authenticator: mustAuthenticator(t)}); err == nil {
		t.Fatal("NewHandler(zero max body) succeeded")
	}
	handler := mustHandler(t, completer, HandlerConfig{Authenticator: mustAuthenticator(t), MaxBodyBytes: 1})
	if handler.keepAliveInterval != defaultKeepAliveInterval {
		t.Fatalf("keep-alive interval = %v", handler.keepAliveInterval)
	}
}

type fakeCompleter struct {
	complete func(context.Context, completion.Request) (completion.Response, error)
	stream   func(context.Context, completion.Request, completion.StreamEmitter) error
}

func (f *fakeCompleter) Complete(ctx context.Context, request completion.Request) (completion.Response, error) {
	if f.complete == nil {
		return completion.Response{}, errors.New("unexpected Complete call")
	}
	return f.complete(ctx, request)
}

func (f *fakeCompleter) Stream(ctx context.Context, request completion.Request, emit completion.StreamEmitter) error {
	if f.stream == nil {
		return errors.New("unexpected Stream call")
	}
	return f.stream(ctx, request, emit)
}

type nonFlushingWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *nonFlushingWriter) Header() http.Header { return w.header }

func (w *nonFlushingWriter) Write(data []byte) (int, error) { return w.body.Write(data) }

func (w *nonFlushingWriter) WriteHeader(status int) { w.status = status }

func mustHandler(t *testing.T, completer Completer, cfg HandlerConfig) *Handler {
	t.Helper()
	handler, err := NewHandler(completer, cfg)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func mustAuthenticator(t *testing.T) *openaiAuth.Authenticator {
	t.Helper()
	authenticator, err := openaiAuth.New(openaiAuth.ModeAPIKey, []string{"secret"})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	return authenticator
}

func performRequest(handler http.Handler, method, body, authorization, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, Path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var payload errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code == nil || *payload.Error.Code != code || payload.Error.Message == "" {
		t.Fatalf("error payload = %#v, want code %q", payload, code)
	}
}

func validRequestBody(stream bool) string {
	return fmt.Sprintf(`{"model":"human","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
}

func parseSSE(t *testing.T, body string) []string {
	t.Helper()
	var events []string
	for _, block := range strings.Split(body, "\n\n") {
		if strings.HasPrefix(block, "data: ") {
			events = append(events, strings.TrimPrefix(block, "data: "))
		}
	}
	return events
}
