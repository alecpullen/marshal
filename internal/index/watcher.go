package index

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	root     string
	debounce time.Duration
	run      func(ctx context.Context) error
	log      *slog.Logger
	wg       sync.WaitGroup
}

func NewWatcher(root string, debounce time.Duration, run func(ctx context.Context) error, log *slog.Logger) *Watcher {
	if debounce <= 0 {
		debounce = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{root: root, debounce: debounce, run: run, log: log}
}

func (w *Watcher) Name() string { return "index-watcher" }

func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()
	w.addRecursive(fsw, w.root)

	timer := time.NewTimer(w.debounce)
	timer.Stop()
	dirty := false
	running := false
	rerun := false
	finished := make(chan struct{}, 1)

	fire := func() {
		running = true
		dirty = false
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			if err := w.run(ctx); err != nil {
				w.log.Warn("index watcher pass failed", "err", err)
			}
			finished <- struct{}{}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			w.wg.Wait()
			return nil
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if w.ignored(ev.Name) {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					w.addRecursive(fsw, ev.Name)
				}
			}
			dirty = true
			timer.Reset(w.debounce)
		case <-timer.C:
			if !dirty {
				continue
			}
			if running {
				rerun = true
				continue
			}
			fire()
		case <-finished:
			running = false
			if rerun || dirty {
				rerun = false
				fire()
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("index watcher fsnotify error", "err", err)
		}
	}
}

func (w *Watcher) addRecursive(fsw *fsnotify.Watcher, dir string) {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if w.ignored(path) {
			return filepath.SkipDir
		}
		if err := fsw.Add(path); err != nil {
			w.log.Debug("index watcher could not watch dir", "dir", path, "err", err)
		}
		return nil
	})
}

func (w *Watcher) ignored(path string) bool {
	base := filepath.Base(path)
	return base == ".git" || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) ||
		base == "node_modules" || base == ".marshal"
}
