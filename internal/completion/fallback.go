package completion

import (
	"context"
	"errors"
	"strings"
)

// Fallback generates a protocol-neutral response when no human answer is
// available. Implementations may use the complete original request.
type Fallback interface {
	Generate(ctx context.Context, request Request) (string, error)
	Stream(ctx context.Context, request Request, emit StreamEmitter) error
}

type templateFallback struct {
	content string
}

// NewTemplateFallback creates the phase-one static fallback implementation.
func NewTemplateFallback(content string) (Fallback, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("completion: fallback template is required")
	}
	return templateFallback{content: content}, nil
}

func (f templateFallback) Generate(ctx context.Context, _ Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return f.content, nil
}

func (f templateFallback) Stream(ctx context.Context, request Request, emit StreamEmitter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if emit == nil {
		return errors.New("completion: fallback stream emitter is required")
	}
	if err := emit(StreamChunk{ID: request.ID, Model: request.Model, ContentDelta: f.content}); err != nil {
		return err
	}
	return emit(StreamChunk{ID: request.ID, Model: request.Model, Done: true})
}

type fallbackChain struct {
	primary  Fallback
	terminal Fallback
}

// NewFallbackChain tries primary first and uses terminal when primary fails
// before producing a response.
func NewFallbackChain(primary, terminal Fallback) (Fallback, error) {
	if primary == nil || terminal == nil {
		return nil, errors.New("completion: primary and terminal fallbacks are required")
	}
	return fallbackChain{primary: primary, terminal: terminal}, nil
}

func (f fallbackChain) Generate(ctx context.Context, request Request) (string, error) {
	content, err := f.primary.Generate(ctx, request)
	if err == nil {
		return content, nil
	}
	if ctx.Err() != nil {
		return "", err
	}
	return f.terminal.Generate(ctx, request)
}

func (f fallbackChain) Stream(ctx context.Context, request Request, emit StreamEmitter) error {
	emitted := false
	err := f.primary.Stream(ctx, request, func(chunk StreamChunk) error {
		emitted = true
		return emit(chunk)
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || emitted {
		return err
	}
	return f.terminal.Stream(ctx, request, emit)
}
