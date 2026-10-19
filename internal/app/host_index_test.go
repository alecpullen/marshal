package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/logging"
	"marshal/internal/index"
)

// fakeLease stands in for index.TryLease: it reports the lease held by
// another process for the first busy tries, then free.
type fakeLease struct {
	mu       sync.Mutex
	busy     int
	err      error
	tries    int
	releases int
}

func (f *fakeLease) try(string) (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if f.err != nil {
		return nil, false, f.err
	}
	if f.tries <= f.busy {
		return nil, false, nil
	}
	return func() {
		f.mu.Lock()
		f.releases++
		f.mu.Unlock()
	}, true, nil
}

func (f *fakeLease) counts() (tries, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries, f.releases
}

// countingSupervisor is a supervisor over l whose scan and watcher only count
// their calls. The watcher returns as soon as it starts.
func countingSupervisor(l *fakeLease, watch bool, scanErr error, scans, watches *int32) indexSupervisor {
	return indexSupervisor{
		root:     "/repo",
		watch:    watch,
		retry:    time.Millisecond,
		log:      logging.New(io.Discard, slog.LevelInfo, false),
		tryLease: l.try,
		scan: func(context.Context) error {
			atomic.AddInt32(scans, 1)
			return scanErr
		},
		watcher: func(context.Context) error {
			atomic.AddInt32(watches, 1)
			return nil
		},
	}
}

func TestIndexSupervisorWithoutWatcherSkipsAHeldLease(t *testing.T) {
	l := &fakeLease{busy: 1}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if tries, _ := l.counts(); scans != 0 || watches != 0 || tries != 1 {
		t.Fatalf("scans %d, watches %d, tries %d; want no scan and one try", scans, watches, tries)
	}
}

func TestIndexSupervisorWithoutWatcherScansOnceAndReleases(t *testing.T) {
	l := &fakeLease{}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if _, releases := l.counts(); scans != 1 || watches != 0 || releases != 1 {
		t.Fatalf("scans %d, watches %d, releases %d; want one scan, no watcher, one release", scans, watches, releases)
	}
}

func TestIndexSupervisorWithWatcherWaitsForTheLeaseThenScansAndWatches(t *testing.T) {
	l := &fakeLease{busy: 2}
	var scans, watches int32
	countingSupervisor(l, true, nil, &scans, &watches).run(context.Background())
	tries, releases := l.counts()
	if tries != 3 || scans != 1 || watches != 1 || releases != 1 {
		t.Fatalf("tries %d, scans %d, watches %d, releases %d; want 3 tries, then one scan, one watcher, one release", tries, scans, watches, releases)
	}
}

func TestIndexSupervisorWithWatcherStopsWaitingWhenCancelled(t *testing.T) {
	l := &fakeLease{busy: 1 << 30}
	var scans, watches int32
	s := countingSupervisor(l, true, nil, &scans, &watches)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor kept waiting for the lease after cancellation")
	}
	if scans != 0 {
		t.Fatalf("scans %d; want none without the lease", scans)
	}
}

func TestIndexSupervisorFailedScanReleasesTheLeaseAndStops(t *testing.T) {
	l := &fakeLease{}
	var scans, watches int32
	countingSupervisor(l, true, errors.New("embedding probe failed"), &scans, &watches).run(context.Background())
	if tries, releases := l.counts(); tries != 1 || scans != 1 || watches != 0 || releases != 1 {
		t.Fatalf("tries %d, scans %d, watches %d, releases %d; want one failed scan, released, no watcher", tries, scans, watches, releases)
	}
}

func TestIndexSupervisorIndexesWithoutAnUnusableLease(t *testing.T) {
	l := &fakeLease{err: errors.New("open index lease: permission denied")}
	var scans, watches int32
	countingSupervisor(l, false, nil, &scans, &watches).run(context.Background())
	if scans != 1 {
		t.Fatalf("scans %d; want the scan to run without the lease", scans)
	}
}

// Two supervisors on one root with the real lease stand in for two processes:
// while the first watches, the second skips its scan.
func TestIndexSupervisorsOnOneRootRunOneIndexer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := logging.New(io.Discard, slog.LevelInfo, false)
	watching := make(chan struct{})
	var firstScans, secondScans int32
	first := indexSupervisor{root: root, watch: true, retry: time.Hour, log: log, tryLease: index.TryLease,
		scan: func(context.Context) error { atomic.AddInt32(&firstScans, 1); return nil },
		watcher: func(ctx context.Context) error {
			close(watching)
			<-ctx.Done()
			return nil
		},
	}
	second := indexSupervisor{root: root, watch: false, retry: time.Hour, log: log, tryLease: index.TryLease,
		scan: func(context.Context) error { atomic.AddInt32(&secondScans, 1); return nil },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		first.run(ctx)
		close(done)
	}()
	<-watching
	skipped := make(chan struct{})
	go func() {
		second.run(context.Background())
		close(skipped)
	}()
	select {
	case <-skipped:
	case <-time.After(5 * time.Second):
		t.Fatal("the second supervisor waited for the held lease instead of skipping its scan")
	}
	cancel()
	<-done

	if firstScans != 1 || secondScans != 0 {
		t.Fatalf("first scanned %d times, second %d; want only the lease holder to scan", firstScans, secondScans)
	}
	release, held, err := index.TryLease(root)
	if err != nil || !held {
		t.Fatalf("lease after the holder stopped: held %v, err %v; want it free", held, err)
	}
	release()
}
