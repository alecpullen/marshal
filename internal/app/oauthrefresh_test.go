package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/worker"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// fakeRefreshLease is the refresh lease a test controls: it grants the lease
// while granted is true, reports another holder when it is false, and returns
// err when that is set.
type fakeRefreshLease struct {
	mu       sync.Mutex
	granted  bool
	err      error
	grants   int
	releases int
}

func (f *fakeRefreshLease) try() (func(), bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, false, f.err
	}
	if !f.granted {
		return nil, false, nil
	}
	f.grants++
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			f.releases++
			f.mu.Unlock()
		})
	}, true, nil
}

func (f *fakeRefreshLease) grant(granted bool) {
	f.mu.Lock()
	f.granted = granted
	f.mu.Unlock()
}

func (f *fakeRefreshLease) counts() (grants, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grants, f.releases
}

// fakeWorker records that it started and that it returned.
type fakeWorker struct {
	name    string
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func newFakeWorker(name string) *fakeWorker {
	return &fakeWorker{name: name, started: make(chan struct{}), stopped: make(chan struct{})}
}

func (w *fakeWorker) Name() string { return w.name }

func (w *fakeWorker) Run(ctx context.Context) error {
	close(w.started)
	<-ctx.Done()
	w.once.Do(func() { close(w.stopped) })
	return nil
}

// recordingBuilder builds one fake worker per OAuth provider, and remembers
// what each build was asked for.
type recordingBuilder struct {
	mu       sync.Mutex
	requests [][]string
	workers  []*fakeWorker
}

// oauthProviderNames is buildOAuthWorkers' filter without the engines: these
// tests drive config parsing and the lease, not the keychain.
func oauthProviderNames(cfg config.Config) []string {
	names := []string{}
	for name, pc := range cfg.Providers {
		if pc.Auth == "oauth" && pc.Type == "openai_codex" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (b *recordingBuilder) build(cfg config.Config) []worker.Worker {
	names := oauthProviderNames(cfg)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests = append(b.requests, names)
	out := make([]worker.Worker, 0, len(names))
	for _, name := range names {
		w := newFakeWorker(name)
		b.workers = append(b.workers, w)
		out = append(out, w)
	}
	return out
}

func (b *recordingBuilder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.workers)
}

func (b *recordingBuilder) lastRequest() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.requests) == 0 {
		return nil
	}
	return append([]string(nil), b.requests[len(b.requests)-1]...)
}

// workerAt waits for the build that produces the index'th worker.
func (b *recordingBuilder) workerAt(t *testing.T, i int) *fakeWorker {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		if i < len(b.workers) {
			w := b.workers[i]
			b.mu.Unlock()
			return w
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("only %d workers were built, want at least %d", b.count(), i+1)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitNotClosed(t *testing.T, ch chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s happened, want it not to", what)
	case <-time.After(d):
	}
}

// waitFor polls cond until it holds, failing after two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

const oneProviderConfig = `[providers.codex]
type = "openai_codex"
base_url = "https://example.invalid/backend-api"
auth = "oauth"
`

const twoProvidersConfig = `[providers.codex]
type = "openai_codex"
base_url = "https://example.invalid/backend-api"
auth = "oauth"

[providers.second]
type = "openai_codex"
base_url = "https://example.invalid/backend-api"
auth = "oauth"
`

const noProvidersConfig = `# no OAuth provider yet
`

// writeUserConfig writes the user config RunOAuthRefresh reads, moving its
// stamp stampOffset into the future so a rewrite is always a visible change:
// the helper compares nanosecond modification time and size, and two writes of
// one test can otherwise land in the same nanosecond.
func writeUserConfig(t *testing.T, home string, stampOffset time.Duration, body string) {
	t.Helper()
	path := config.UserConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create the user config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write the user config: %v", err)
	}
	if stampOffset != 0 {
		stamp := time.Now().Add(stampOffset)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("move the user config's stamp: %v", err)
		}
	}
}

// startHelper runs RunOAuthRefresh on opts and returns the channel its result
// arrives on.
func startHelper(t *testing.T, opts OAuthRefreshOptions) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunOAuthRefresh(ctx, opts) }()
	return done, cancel
}

// ── oauthLease.hold ──────────────────────────────────────────────────────

func TestHoldRunsTheWorkWhileTheLeaseIsHeld(t *testing.T) {
	lease := &fakeRefreshLease{granted: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := make(chan struct{})
	workDone := make(chan struct{})
	holdDone := make(chan struct{})
	go func() {
		defer close(holdDone)
		oauthLease{tryLease: lease.try, retry: time.Millisecond}.hold(ctx, func(workCtx context.Context) {
			close(ran)
			<-workCtx.Done()
			close(workDone)
		})
	}()

	waitClosed(t, ran, "the work to run")
	if grants, releases := lease.counts(); grants != 1 || releases != 0 {
		t.Fatalf("while the work runs: grants %d, releases %d; want 1 grant and no release", grants, releases)
	}

	cancel()
	waitClosed(t, holdDone, "hold to return")
	waitClosed(t, workDone, "the work to return")
	if grants, releases := lease.counts(); grants != 1 || releases != 1 {
		t.Fatalf("after hold: grants %d, releases %d; want one of each", grants, releases)
	}
}

func TestHoldWaitsForAnotherHolder(t *testing.T) {
	lease := &fakeRefreshLease{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := make(chan struct{})
	holdDone := make(chan struct{})
	go func() {
		defer close(holdDone)
		oauthLease{tryLease: lease.try, retry: 5 * time.Millisecond}.hold(ctx, func(workCtx context.Context) {
			close(ran)
			<-workCtx.Done()
		})
	}()

	waitNotClosed(t, ran, 40*time.Millisecond, "the work running while another process holds the lease")
	if grants, _ := lease.counts(); grants != 0 {
		t.Fatalf("the work took the lease %d times while another process held it", grants)
	}

	lease.grant(true)
	waitClosed(t, ran, "the work to run once the lease is free")
	cancel()
	waitClosed(t, holdDone, "hold to return")
}

func TestHoldRunsWithoutALeaseItCannotTake(t *testing.T) {
	lease := &fakeRefreshLease{err: errors.New("advisory file locking is not supported on this platform")}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	ran := false
	oauthLease{tryLease: lease.try, retry: time.Millisecond, log: log}.hold(
		context.Background(), func(context.Context) { ran = true })

	if !ran {
		t.Fatal("the work did not run without the lease")
	}
	if n := strings.Count(buf.String(), "refreshing without it"); n != 1 {
		t.Fatalf("warned %d times, want exactly 1:\n%s", n, buf.String())
	}
}

// ── RunOAuthRefresh ──────────────────────────────────────────────────────

func TestTheHelperRunsTheConfigsProviders(t *testing.T) {
	home := t.TempDir()
	writeUserConfig(t, home, 0, oneProviderConfig)
	lease := &fakeRefreshLease{granted: true}
	builder := &recordingBuilder{}
	auth, _ := memoryAuthResolver(t)

	done, cancel := startHelper(t, OAuthRefreshOptions{
		HomeDir:  home,
		Auth:     auth,
		Logger:   discardLogger(),
		tryLease: lease.try,
		workers:  builder.build,
		retry:    time.Millisecond,
		poll:     5 * time.Millisecond,
	})

	w := builder.workerAt(t, 0)
	waitClosed(t, w.started, "the config's worker to start")
	if w.Name() != "codex" {
		t.Fatalf("worker name = %q, want the config's provider", w.Name())
	}
	if grants, _ := lease.counts(); grants != 1 {
		t.Fatalf("the lease was taken %d times, want 1", grants)
	}

	cancel()
	waitClosed(t, w.stopped, "the worker to stop")
	if err := <-done; err != nil {
		t.Fatalf("RunOAuthRefresh: %v", err)
	}
	if _, releases := lease.counts(); releases != 1 {
		t.Fatalf("the lease was released %d times, want 1", releases)
	}
}

func TestTheHelperRebuildsOnAChange(t *testing.T) {
	home := t.TempDir()
	writeUserConfig(t, home, 0, oneProviderConfig)
	lease := &fakeRefreshLease{granted: true}
	builder := &recordingBuilder{}
	auth, _ := memoryAuthResolver(t)

	done, cancel := startHelper(t, OAuthRefreshOptions{
		HomeDir:  home,
		Auth:     auth,
		Logger:   discardLogger(),
		tryLease: lease.try,
		workers:  builder.build,
		retry:    time.Millisecond,
		poll:     5 * time.Millisecond,
	})
	defer cancel()

	first := builder.workerAt(t, 0)
	waitClosed(t, first.started, "the first worker to start")

	writeUserConfig(t, home, 5*time.Second, twoProvidersConfig)

	// The rebuild stops the first set, then starts two workers for the new
	// config.
	second := builder.workerAt(t, 1)
	third := builder.workerAt(t, 2)
	waitClosed(t, second.started, "the second config's first worker to start")
	waitClosed(t, third.started, "the second config's second worker to start")
	waitClosed(t, first.stopped, "the first config's worker to stop")

	if grants, releases := lease.counts(); grants != 1 || releases != 0 {
		t.Fatalf("rebuilding took the lease %d times and released it %d times; want one grant, no release", grants, releases)
	}
	if got, want := builder.lastRequest(), []string{"codex", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the last build asked for %v, want %v", got, want)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunOAuthRefresh: %v", err)
	}
}

func TestTheHelperReleasesWhenNoProviderIsLeft(t *testing.T) {
	home := t.TempDir()
	writeUserConfig(t, home, 0, oneProviderConfig)
	lease := &fakeRefreshLease{granted: true}
	builder := &recordingBuilder{}
	auth, _ := memoryAuthResolver(t)

	_, cancel := startHelper(t, OAuthRefreshOptions{
		HomeDir:  home,
		Auth:     auth,
		Logger:   discardLogger(),
		tryLease: lease.try,
		workers:  builder.build,
		retry:    time.Millisecond,
		poll:     5 * time.Millisecond,
	})
	defer cancel()

	first := builder.workerAt(t, 0)
	waitClosed(t, first.started, "the worker to start")

	writeUserConfig(t, home, 5*time.Second, noProvidersConfig)
	waitClosed(t, first.stopped, "the worker to stop when its provider is gone")
	waitFor(t, "the lease to be released", func() bool {
		_, releases := lease.counts()
		return releases == 1
	})

	// Naming the provider again takes the lease a second time.
	writeUserConfig(t, home, 10*time.Second, oneProviderConfig)
	again := builder.workerAt(t, 1)
	waitClosed(t, again.started, "the worker to start again")
	waitFor(t, "the lease to be taken again", func() bool {
		grants, _ := lease.counts()
		return grants == 2
	})
}

func TestTheHelperRefusesWithoutAHomeOrAnAuth(t *testing.T) {
	auth, _ := memoryAuthResolver(t)

	if err := RunOAuthRefresh(context.Background(), OAuthRefreshOptions{Auth: auth}); err == nil {
		t.Fatal("RunOAuthRefresh with no home directory = nil error, want a refusal")
	}
	if err := RunOAuthRefresh(context.Background(), OAuthRefreshOptions{HomeDir: t.TempDir()}); !errors.Is(err, ErrAuthResolverRequired) {
		t.Fatalf("RunOAuthRefresh with no auth resolver = %v, want ErrAuthResolverRequired", err)
	}
}
