package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/index"
)

// hostRoot is a project root with the .marshal directory startRuntime would
// already have created.
func hostRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func testHosts(t *testing.T, indexing bool, linger time.Duration) *Hosts {
	t.Helper()
	hs := newHosts(indexing, linger)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Close(ctx)
	})
	return hs
}

func mustAcquire(t *testing.T, hs *Hosts, root string) *ProjectHost {
	t.Helper()
	h, err := hs.acquire(root, time.Now())
	if err != nil {
		t.Fatalf("acquire(%s): %v", root, err)
	}
	return h
}

func hostClosed(h *ProjectHost) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func waitForHostClosed(t *testing.T, h *ProjectHost) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !hostClosed(h) {
		if time.Now().After(deadline) {
			t.Fatal("host still open after the linger")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostsShareOneHostPerRoot(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root, other := hostRoot(t), hostRoot(t)

	a := mustAcquire(t, hs, root)
	b := mustAcquire(t, hs, root)
	if a != b {
		t.Fatal("two acquires on one root opened two hosts")
	}
	if c := mustAcquire(t, hs, other); c == a {
		t.Fatal("a different root shared the host")
	}
	if a.Root() != root {
		t.Fatalf("Root() = %q, want %q", a.Root(), root)
	}
}

// openGeneration starts session id in the host's project with one generation
// left open, the state a live session has.
func openGeneration(t *testing.T, database *db.DB, root, id string) {
	t.Helper()
	projectID, err := database.GetOrCreateProject(root, "hosts-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(id, projectID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.BeginGeneration(db.Generation{ID: id + "-gen", SessionID: id, Seq: 1, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func generationOpen(t *testing.T, database *db.DB, id string) bool {
	t.Helper()
	gens, err := database.GenerationsForSession(id)
	if err != nil || len(gens) != 1 {
		t.Fatalf("generations for %s = %v, %v", id, gens, err)
	}
	return gens[0].EndedAt == nil
}

func TestHostReconcilesOpenGenerationsOncePerHost(t *testing.T) {
	hs := testHosts(t, false, 0)
	root := hostRoot(t)

	first := mustAcquire(t, hs, root)
	openGeneration(t, first.db, root, "sess_live")
	second := mustAcquire(t, hs, root)
	if !generationOpen(t, second.db, "sess_live") {
		t.Fatal("a second session on an open host ended the first session's live generation")
	}

	first.release()
	second.release()
	if !hostClosed(first) {
		t.Fatal("without a linger, the last release did not close the host")
	}
	reopened := mustAcquire(t, hs, root)
	if generationOpen(t, reopened.db, "sess_live") {
		t.Fatal("a fresh host did not reconcile a generation left open")
	}
}

func TestHostLingersAfterItsLastRelease(t *testing.T) {
	hs := testHosts(t, false, 100*time.Millisecond)
	root := hostRoot(t)

	h := mustAcquire(t, hs, root)
	h.release()
	if again := mustAcquire(t, hs, root); again != h {
		t.Fatal("an acquire inside the linger opened a new host")
	}
	h.release()
	waitForHostClosed(t, h)
	if fresh := mustAcquire(t, hs, root); fresh == h {
		t.Fatal("an acquire after the linger reused the closed host")
	}
}

func TestHostsCloseClosesAttachedHostsAtOnce(t *testing.T) {
	hs := newHosts(false, time.Minute)
	h := mustAcquire(t, hs, hostRoot(t))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !hostClosed(h) {
		t.Fatal("Close left an attached host open")
	}
	h.release() // a release after Close is harmless
	if _, err := hs.acquire(hostRoot(t), time.Now()); !errors.Is(err, errHostsClosed) {
		t.Fatalf("acquire after Close: err = %v, want errHostsClosed", err)
	}
}

func TestHostOpenWaitsForItsRootsPreviousClose(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostRoot(t)
	closing := make(chan struct{})
	hs.mu.Lock()
	hs.closing[root] = closing
	hs.mu.Unlock()

	got := make(chan *ProjectHost, 1)
	go func() {
		h, _ := hs.acquire(root, time.Now())
		got <- h
	}()
	select {
	case <-got:
		t.Fatal("acquire opened a host while the root's previous host was closing")
	case <-time.After(50 * time.Millisecond):
	}
	hs.mu.Lock()
	delete(hs.closing, root)
	hs.mu.Unlock()
	close(closing)
	select {
	case h := <-got:
		if h == nil {
			t.Fatal("acquire returned no host")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquire did not open the host after the previous close finished")
	}
}

func TestHostOpenFailureIsNotKept(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := t.TempDir()
	// A file where the .marshal directory belongs makes the database
	// unopenable.
	if err := os.WriteFile(filepath.Join(root, ".marshal"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.acquire(root, time.Now()); err == nil || !strings.Contains(err.Error(), "open database") {
		t.Fatalf("acquire on an unopenable database: err = %v, want an open database error", err)
	}
	if err := os.Remove(filepath.Join(root, ".marshal")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.acquire(root, time.Now()); err != nil {
		t.Fatalf("acquire after fixing the root: %v", err)
	}
}

func TestHostProgressReachesEveryAttachedSession(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	one := session.New(cfg, root, time.Now(), session.Persistence{})
	two := session.New(cfg, root, time.Now(), session.Persistence{})
	h.attach(one, cfg, 1)
	h.attach(two, cfg, 1)

	h.progress("indexing 3 files")
	if one.Activity().Label != "indexing 3 files" || two.Activity().Label != "indexing 3 files" {
		t.Fatalf("activities = %q, %q; want the progress message on both", one.Activity().Label, two.Activity().Label)
	}
	h.detach(two)
	h.progress("embedding 1 file")
	if one.Activity().Label != "embedding 1 file" || two.Activity().Label != "indexing 3 files" {
		t.Fatalf("after detach, activities = %q, %q; want only the attached session updated", one.Activity().Label, two.Activity().Label)
	}
}

func TestHostIndexPassPublishesAReportAndFillsTheFileIndex(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	root := hostRoot(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := mustAcquire(t, hs, root)
	projectID, err := h.db.GetOrCreateProject(root, "hosts-index-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := h.reports.Subscribe(ctx)

	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, projectID)

	select {
	case ev := <-reports:
		if ev.Type != indexEventCompleted || ev.Payload.Files == 0 {
			t.Fatalf("report = %q with %d files; want a completed pass over the file", ev.Type, ev.Payload.Files)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the startup scan published no report")
	}
	files, err := h.db.GetFileIndex(projectID, 0)
	if err != nil || len(files) == 0 {
		t.Fatalf("file index after the startup scan: %d files, err %v", len(files), err)
	}
}

func TestHostStartsTheWatcherOnlyWhenConfigured(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	h := mustAcquire(t, hs, hostRoot(t))
	cfg := config.Default() // watch unset: the watcher stays off
	if h.supervisor(cfg).watch {
		t.Fatal("the watcher runs without indexing.watch")
	}
	on := true
	cfg.Indexing.Watch = &on
	if !h.supervisor(cfg).watch {
		t.Fatal("the watcher does not run with indexing.watch = true")
	}
}

// countingWorker counts its runs and blocks until cancelled, standing in for
// the index work.
type countingWorker struct{ runs *int32 }

func (countingWorker) Name() string { return "counting-index" }
func (w countingWorker) Run(ctx context.Context) error {
	atomic.AddInt32(w.runs, 1)
	<-ctx.Done()
	return nil
}

func TestHostRestartsIndexWorkOnlyWhenTheWatcherSettingChanges(t *testing.T) {
	hs := testHosts(t, true, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	waitForRuns(t, &runs, 1)

	ignored := cfg
	ignored.Indexing.Ignore = append([]string{"tmp/**"}, cfg.Indexing.Ignore...)
	h.noteConfig(ignored)
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("index work ran %d times after changes that keep the watcher setting; want 1", got)
	}

	on := true
	watching := cfg
	watching.Indexing.Watch = &on
	h.noteConfig(watching)
	waitForRuns(t, &runs, 2)
}

func waitForRuns(t *testing.T, runs *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(runs) != want {
		if time.Now().After(deadline) {
			t.Fatalf("index work ran %d times, want %d", atomic.LoadInt32(runs), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHostWithoutIndexingStartsNoIndexWork(t *testing.T) {
	hs := testHosts(t, false, time.Minute)
	var runs int32
	hs.workers = append(hs.workers, countingWorker{runs: &runs})
	root := hostRoot(t)
	h := mustAcquire(t, hs, root)
	cfg := config.Default()
	h.attach(session.New(cfg, root, time.Now(), session.Persistence{}), cfg, 1)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&runs); got != 0 {
		t.Fatalf("a host without indexing ran index work %d times", got)
	}
	release, held, err := index.TryLease(root)
	if err != nil || !held {
		t.Fatalf("lease on a host without indexing: held %v, err %v; want it free", held, err)
	}
	release()
}
