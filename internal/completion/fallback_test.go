package completion

import (
	"context"
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
