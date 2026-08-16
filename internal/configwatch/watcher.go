// Package configwatch reloads configuration after on-disk content changes.
package configwatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadFunc reloads the configuration at path.
type ReloadFunc func(path string) error

// Watcher observes a configuration file and invokes reload after a change.
//
// The parent directory is watched instead of the file path. Kubernetes
// projected ConfigMaps update their files by swapping the ..data symlink, which
// replaces the target beneath a stable per-key symlink such as config.yaml.
type Watcher struct {
	path     string
	fileName string
	dir      string
	debounce time.Duration
	reload   ReloadFunc
	logger   *slog.Logger
	watcher  *fsnotify.Watcher
}

// Start creates a content-aware watcher and starts it in the background.
// The initial contents are not reloaded: callers are expected to load the
// configuration before starting the watcher.
func Start(ctx context.Context, path string, debounce time.Duration, reload ReloadFunc, logger *slog.Logger) (*Watcher, error) {
	if path == "" {
		return nil, errors.New("config watch path is empty")
	}
	if debounce <= 0 {
		return nil, errors.New("config watch debounce must be positive")
	}
	if reload == nil {
		return nil, errors.New("config watch reload function is nil")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create config watcher: %w", err)
	}
	configPath, err := absolutePath(path)
	if err != nil {
		_ = watcher.Close()
		return nil, err
	}
	digest, err := fileDigest(configPath.path)
	if err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("read initial config watch file: %w", err)
	}
	if err := watcher.Add(configPath.dir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch config directory %q: %w", configPath.dir, err)
	}

	w := &Watcher{
		path:     configPath.path,
		fileName: configPath.fileName,
		dir:      configPath.dir,
		debounce: debounce,
		reload:   reload,
		logger:   logger,
		watcher:  watcher,
	}
	go w.run(ctx, digest)
	if logger != nil {
		logger.Info("config_watch_started", "config", configPath.path, "debounce", debounce)
	}
	return w, nil
}

type resolvedPath struct {
	path     string
	dir      string
	fileName string
}

func absolutePath(path string) (resolvedPath, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("resolve config watch path: %w", err)
	}
	return resolvedPath{path: abs, dir: filepath.Dir(abs), fileName: filepath.Base(abs)}, nil
}

func (w *Watcher) run(ctx context.Context, digest [sha256.Size]byte) {
	defer func() {
		_ = w.watcher.Close()
		if w.logger != nil {
			w.logger.Info("config_watch_stopped", "config", w.path)
		}
	}()

	var timer *time.Timer
	var timerCh <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if !w.isConfigEvent(event) {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(w.debounce)
				timerCh = timer.C
				continue
			}
			resetTimer(timer, w.debounce)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if w.logger != nil {
				w.logger.Warn("config_watch_error", "config", w.path, "error", err)
			}
		case <-timerCh:
			timer = nil
			timerCh = nil
			updated, err := fileDigest(w.path)
			if err != nil {
				if w.logger != nil {
					w.logger.Warn("config_watch_read_failed", "config", w.path, "error", err)
				}
				continue
			}
			if updated == digest {
				continue
			}
			digest = updated
			if err := w.reload(w.path); err != nil {
				if w.logger != nil {
					w.logger.Warn("config_watch_reload_failed", "config", w.path, "error", err)
				}
				continue
			}
			if w.logger != nil {
				w.logger.Info("config_watch_reloaded", "config", w.path)
			}
		}
	}
}

func (w *Watcher) isConfigEvent(event fsnotify.Event) bool {
	if filepath.Dir(event.Name) != w.dir {
		return false
	}
	name := filepath.Base(event.Name)
	return name == w.fileName || name == "..data" || name == "..data_tmp"
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func stopTimer(timer *time.Timer) {
	if timer == nil || !timer.Stop() {
		return
	}
}
