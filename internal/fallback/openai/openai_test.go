package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Xbai-hang/sotapi/internal/completion"
)

func TestFallbackGenerateReplacesPrivilegedMessages(t *testing.T) {
	original := completion.Request{
		ID:    "request-1",
		Model: "public-human",
		Messages: []completion.Message{
			{Role: "system", Content: "caller system"},
			{Role: "developer", Content: "caller developer"},
			{Role: "user", Content: "question"},
			{Role: "assistant", Content: "prior answer"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer provider-key" {
			t.Errorf("request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Model != "fallback-model" || payload.Stream || len(payload.Messages) != 3 {
			t.Errorf("payload = %#v", payload)
		}
		if payload.Messages[0].Role != "system" || !strings.Contains(payload.Messages[0].Content, "SotAPI") {
			t.Errorf("trusted system message = %#v", payload.Messages[0])
		}
		if payload.Messages[1].Role != "user" || payload.Messages[1].Content != "question" || payload.Messages[2].Role != "assistant" {
			t.Errorf("forwarded messages = %#v", payload.Messages)
		}
		writeJSON(t, writer, `{"id":"provider-id","model":"fallback-model","choices":[{"index":0,"message":{"role":"assistant","content":"model answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	fallback := mustFallback(t, Config{BaseURL: server.URL, APIKey: "provider-key", Model: "fallback-model", Timeout: time.Second})
	content, err := fallback.Generate(context.Background(), original)
	if err != nil || content != "model answer" {
		t.Fatalf("Generate() = %q, %v", content, err)
	}
	if original.Messages[0].Content != "caller system" || len(original.Messages) != 4 {
		t.Fatalf("original request mutated: %#v", original)
	}
}

func TestFallbackStreamConvertsOpenAIEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if !payload.Stream {
			t.Errorf("stream = false")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	fallback := mustFallback(t, Config{BaseURL: server.URL, Model: "fallback-model", Timeout: time.Second, Stream: true})
	request := completion.Request{ID: "caller-id", Model: "public-human", Messages: []completion.Message{{Role: "user", Content: "hello"}}}
	var chunks []completion.StreamChunk
	if err := fallback.Stream(context.Background(), request, func(chunk completion.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(chunks) != 3 || chunks[0].ContentDelta != "hello" || chunks[1].ContentDelta != " world" || !chunks[2].Done {
		t.Fatalf("chunks = %#v", chunks)
	}
	for _, chunk := range chunks {
		if chunk.ID != "caller-id" || chunk.Model != "public-human" {
			t.Fatalf("public metadata changed: %#v", chunk)
		}
	}
}

func TestFallbackStreamBuffersWhenNativeStreamingDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload.Stream {
			t.Errorf("stream = true")
		}
		writeJSON(t, writer, `{"choices":[{"message":{"role":"assistant","content":"buffered answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	fallback := mustFallback(t, Config{BaseURL: server.URL, Model: "fallback-model", Timeout: time.Second})
	request := completion.Request{ID: "caller-id", Model: "public-human", Messages: []completion.Message{{Role: "user", Content: "hello"}}}
	var chunks []completion.StreamChunk
	if err := fallback.Stream(context.Background(), request, func(chunk completion.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].ContentDelta != "buffered answer" || !chunks[1].Done {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestFallbackRejectsProviderFailuresAndHonorsContext(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "status", handler: func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
		}},
		{name: "invalid JSON", handler: func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("not-json")) }},
		{name: "empty choices", handler: func(writer http.ResponseWriter, _ *http.Request) { writeJSON(t, writer, `{"choices":[]}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			fallback := mustFallback(t, Config{BaseURL: server.URL, Model: "model", Timeout: time.Second})
			if _, err := fallback.Generate(context.Background(), completion.Request{Messages: []completion.Message{{Role: "user", Content: "hello"}}}); err == nil {
				t.Fatal("Generate() succeeded")
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }))
	defer server.Close()
	fallback := mustFallback(t, Config{BaseURL: server.URL, Model: "model", Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fallback.Generate(ctx, completion.Request{Messages: []completion.Message{{Role: "user", Content: "hello"}}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestFallbackHonorsConfiguredTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	fallback := mustFallback(t, Config{BaseURL: server.URL, Model: "model", Timeout: time.Millisecond})
	if _, err := fallback.Generate(context.Background(), completion.Request{Messages: []completion.Message{{Role: "user", Content: "hello"}}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate() timeout error = %v", err)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []Config{
		{Model: "model", Timeout: time.Second},
		{BaseURL: "://bad", Model: "model", Timeout: time.Second},
		{BaseURL: "https://example.com", Timeout: time.Second},
		{BaseURL: "https://example.com", Model: "model"},
	}
	for _, config := range tests {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%#v) succeeded", config)
		}
	}
}

func mustFallback(t *testing.T, config Config) *Fallback {
	t.Helper()
	fallback, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return fallback
}

func writeJSON(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Errorf("write response: %v", err)
	}
}
