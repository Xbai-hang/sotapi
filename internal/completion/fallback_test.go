package completion

import (
	"context"
	"errors"
	"testing"
)

func TestTemplateFallbackReturnsConfiguredContent(t *testing.T) {
	fallback, err := NewTemplateFallback("friendly fallback")
	if err != nil {
		t.Fatalf("NewTemplateFallback() error = %v", err)
	}
	content, err := fallback.Generate(context.Background(), validRequest("fallback"))
	if err != nil || content != "friendly fallback" {
		t.Fatalf("Generate() = %q, %v", content, err)
	}
}

func TestTemplateFallbackRejectsBlankTemplate(t *testing.T) {
	if _, err := NewTemplateFallback("   "); err == nil {
		t.Fatal("NewTemplateFallback(blank) succeeded")
	}
}

func TestTemplateFallbackHonorsCanceledContext(t *testing.T) {
	fallback, err := NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fallback.Generate(ctx, validRequest("canceled")); err == nil {
		t.Fatal("Generate() with canceled context succeeded")
	}
}

func TestTemplateFallbackStreamsConfiguredContent(t *testing.T) {
	fallback, err := NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	var chunks []StreamChunk
	if err := fallback.Stream(context.Background(), validRequest("stream"), func(chunk StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(chunks) != 2 || chunks[0].ContentDelta != "fallback" || !chunks[1].Done {
		t.Fatalf("Stream() chunks = %#v", chunks)
	}
}

func TestTemplateFallbackStreamValidatesContextAndEmitter(t *testing.T) {
	fallback, err := NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fallback.Stream(ctx, validRequest("canceled-stream"), func(StreamChunk) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() canceled error = %v", err)
	}
	if err := fallback.Stream(context.Background(), validRequest("nil-emitter"), nil); err == nil {
		t.Fatal("Stream() with nil emitter succeeded")
	}
}

func TestFallbackChainRequiresBothFallbacks(t *testing.T) {
	template, err := NewTemplateFallback("fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFallbackChain(nil, template); err == nil {
		t.Fatal("NewFallbackChain(nil, template) succeeded")
	}
	if _, err := NewFallbackChain(template, nil); err == nil {
		t.Fatal("NewFallbackChain(template, nil) succeeded")
	}
}

func TestFallbackChainUsesTerminalFallbackAfterPrimaryFailure(t *testing.T) {
	primaryError := errors.New("provider unavailable")
	primary := &fakeFallback{generateErr: primaryError, streamErr: primaryError}
	terminal, err := NewTemplateFallback("template answer")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := NewFallbackChain(primary, terminal)
	if err != nil {
		t.Fatal(err)
	}

	content, err := chain.Generate(context.Background(), validRequest("complete"))
	if err != nil || content != "template answer" {
		t.Fatalf("Generate() = %q, %v", content, err)
	}
	var chunks []StreamChunk
	if err := chain.Stream(context.Background(), validRequest("stream"), func(chunk StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(chunks) != 2 || chunks[0].ContentDelta != "template answer" || !chunks[1].Done {
		t.Fatalf("Stream() chunks = %#v", chunks)
	}
}

func TestFallbackChainDoesNotAppendTemplateAfterPartialStream(t *testing.T) {
	primaryError := errors.New("stream interrupted")
	primary := &fakeFallback{stream: func(request Request, emit StreamEmitter) error {
		if err := emit(StreamChunk{ID: request.ID, Model: request.Model, ContentDelta: "partial"}); err != nil {
			return err
		}
		return primaryError
	}}
	terminal, err := NewTemplateFallback("template answer")
	if err != nil {
		t.Fatal(err)
	}
	chain, err := NewFallbackChain(primary, terminal)
	if err != nil {
		t.Fatal(err)
	}

	var chunks []StreamChunk
	err = chain.Stream(context.Background(), validRequest("stream"), func(chunk StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if !errors.Is(err, primaryError) || len(chunks) != 1 || chunks[0].ContentDelta != "partial" {
		t.Fatalf("Stream() chunks = %#v, error = %v", chunks, err)
	}
}

func TestFallbackChainHonorsCancellationWithoutTerminalFallback(t *testing.T) {
	terminal := &fakeFallback{}
	chain, err := NewFallbackChain(&fakeFallback{generateErr: context.Canceled}, terminal)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := chain.Generate(ctx, validRequest("canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v", err)
	}
	if terminal.generateCalls != 0 {
		t.Fatalf("terminal Generate() calls = %d", terminal.generateCalls)
	}
}

type fakeFallback struct {
	generate      func(Request) (string, error)
	stream        func(Request, StreamEmitter) error
	generateErr   error
	streamErr     error
	generateCalls int
}

func (f *fakeFallback) Generate(_ context.Context, request Request) (string, error) {
	f.generateCalls++
	if f.generate != nil {
		return f.generate(request)
	}
	return "", f.generateErr
}

func (f *fakeFallback) Stream(_ context.Context, request Request, emit StreamEmitter) error {
	if f.stream != nil {
		return f.stream(request, emit)
	}
	return f.streamErr
}
