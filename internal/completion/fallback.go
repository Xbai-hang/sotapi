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
