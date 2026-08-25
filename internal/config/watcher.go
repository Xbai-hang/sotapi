package config

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounce = 200 * time.Millisecond

// WatchEvent reports either a debounced configuration change or a watcher
// error. A zero Err indicates that the configured file may have changed.
type WatchEvent struct {
	Err error
}

// Watch observes the directory containing path so atomic file replacements
// remain visible. The returned channel closes when ctx is canceled.
func Watch(ctx context.Context, path string) (<-chan WatchEvent, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve watched file %s: %w", path, err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: create file watcher: %w", err)
	}
	if err := watcher.Add(filepath.Dir(absolutePath)); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("config: watch directory for %s: %w", path, err)
	}

	events := make(chan WatchEvent, 1)
	go watchFile(ctx, watcher, filepath.Clean(absolutePath), events)
	return events, nil
}

func watchFile(ctx context.Context, watcher *fsnotify.Watcher, path string, output chan<- WatchEvent) {
	defer close(output)
	defer watcher.Close()

	var timer *time.Timer
	var timerChannel <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != path || !(event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove)) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(watchDebounce)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(watchDebounce)
			}
			timerChannel = timer.C
		case <-timerChannel:
			timerChannel = nil
			timer = nil
			notifyWatchEvent(ctx, output, WatchEvent{})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			notifyWatchEvent(ctx, output, WatchEvent{Err: fmt.Errorf("config: file watcher: %w", err)})
		}
	}
}

func notifyWatchEvent(ctx context.Context, output chan<- WatchEvent, event WatchEvent) {
	select {
	case output <- event:
	case <-ctx.Done():
	}
}
