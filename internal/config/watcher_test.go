package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchReportsAtomicConfigReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("value: old\n"), 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events, err := Watch(ctx, path)
	if err != nil {
		t.Fatalf("Watch() error = %v", err)
	}

	replacement := filepath.Join(directory, ".config.yaml.tmp")
	if err := os.WriteFile(replacement, []byte("value: new\n"), 0o600); err != nil {
		t.Fatalf("write replacement config: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace config: %v", err)
	}

	select {
	case event := <-events:
		if event.Err != nil {
			t.Fatalf("watch event error = %v", event.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for config change")
	}

	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("watch event channel remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("watch event channel did not close after cancellation")
	}
}

func TestWatchRejectsMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "config.yaml")
	if _, err := Watch(context.Background(), path); err == nil {
		t.Fatal("Watch() missing directory succeeded")
	}
}
