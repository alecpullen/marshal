package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func TestAHeldHostWatchesWhateverTheConfigSays(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	h := mustAcquire(t, hs, hostRoot(t))
	cfg := config.Default() // watch unset
	if !h.supervisor(cfg, true).watch {
		t.Fatal("a held host does not watch with indexing.watch unset")
	}
	off := false
	cfg.Indexing.Watch = &off
	if !h.supervisor(cfg, true).watch {
		t.Fatal("a held host does not watch with indexing.watch = false")
	}
	if h.supervisor(cfg, false).watch {
		t.Fatal("an unheld host watches with indexing.watch = false")
	}
}

func TestAHoldStartsTheIndexWithNoSession(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)

	release, err := hs.Hold(root, config.Default())
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	defer release()
	waitForRuns(t, &runs, 1)
}

func TestAHoldRestartsTheIndexOnlyWhenItChangesTheWatcher(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default() // watch off
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	waitForRuns(t, &runs, 1)

	first, err := hs.Hold(root, cfg)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	waitForRuns(t, &runs, 2)

	second, err := hs.Hold(root, cfg)
	if err != nil {
		t.Fatalf("second Hold: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("index work ran %d times after a second hold; want 2", got)
	}

	first()
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("index work ran %d times after releasing one of two holds; want 2", got)
	}

	second()
	waitForRuns(t, &runs, 3)
}

func TestAHoldKeepsTheSessionsConfig(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	sessionCfg := config.Default()
	sessionCfg.Project.Name = "session-project"
	h.attach(session.New(sessionCfg, root, time.Now(), session.Persistence{}), sessionCfg, 7)

	holdCfg := config.Default()
	holdCfg.Project.Name = "hold-project"
	release, err := hs.Hold(root, holdCfg)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	defer release()

	h.mu.Lock()
	gotCfg, gotID := h.cfg, h.projectID
	h.mu.Unlock()
	if gotCfg.Project.Name != "session-project" || gotID != 7 {
		t.Fatalf("host config = %q, project %d; want the session's", gotCfg.Project.Name, gotID)
	}
}

func TestAHeldHostOutlivesItsLastSession(t *testing.T) {
	hs := testHosts(t, false, 100*time.Millisecond)
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	release, err := hs.Hold(root, config.Default())
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	h.release() // the session's release
	time.Sleep(300 * time.Millisecond)
	if hostClosed(h) {
		t.Fatal("the host closed while it was held")
	}
	release()
	waitForHostClosed(t, h)
	release() // a second call changes nothing
}

func TestAHoldIndexesTheProject(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	root := hostRoot(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, hs, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := h.reports.Subscribe(ctx)

	cfg := config.Default()
	release, err := hs.Hold(root, cfg)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	defer release()

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted || ev.Payload.Files == 0 {
			t.Fatalf("report = %q with %d files; want a completed pass over the file", ev.Type, ev.Payload.Files)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the hold published no report")
	}
	projectID, err := h.db.GetOrCreateProject(root, cfg.Project.Name)
	if err != nil {
		t.Fatal(err)
	}
	files, err := h.db.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		t.Fatalf("file index after the hold's scan: %d files, err %v", len(files), err)
	}
}
