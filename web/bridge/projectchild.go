package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// projectLinger is how long a project child with no tracked session lives on
// (P4 D2). Tests shorten it.
var projectLinger = 30 * time.Second

// viewerSocketDir is the state subdirectory the project children's viewer
// sockets live in (P4 D2).
const viewerSocketDir = "v"

// projectChild is one project root's `marshal acp --viewers` child (P4 D2).
type projectChild struct {
	root    string
	socket  string
	child   *Child
	reg     *Registry
	version string
	// mu guards ready and linger.
	mu     sync.Mutex
	ready  bool
	linger func() // stops the armed linger timer; nil when none

	// stopMu guards stopping, the marker a resolver waits on so no
	// replacement child is started while the previous process still holds the
	// viewer socket path. It is separate from mu because a stop blocks on the
	// old child exiting, and a restart's handshake takes mu on the child's own
	// supervision goroutine: holding one across the other would deadlock.
	stopMu   sync.Mutex
	stopping chan struct{}
}

// viewerSocketPath is <stateDir>/v/<first 16 hex digits of sha256(root)>.sock.
//
// The name is a digest rather than the root's own path because a unix socket
// path is capped at 104 bytes on macOS (sun_path) and a project root is easily
// longer. Sixteen hex digits stay well inside the cap and out of collision
// range.
func viewerSocketPath(stateDir, root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(stateDir, viewerSocketDir, hex.EncodeToString(sum[:8])+".sock")
}

// buildProjectChild returns root's Child, unstarted: newProjectChild's when
// set, else a local process running marshalBin in root with --viewers.
//
// It is to project children what buildControlChild is to the control agent. The
// socket's directory is created before anything can listen on it, so a caller
// that only builds the child still finds it (decision 3).
func (f *Fleet) buildProjectChild(root, socket string) (*Child, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, fmt.Errorf("bridge: create viewer socket dir: %w", err)
	}
	if f.newProjectChild != nil {
		return f.newProjectChild(root, socket)
	}
	return &Child{
		MarshalBin: f.marshalBin,
		Args:       []string{"--viewers", "unix://" + socket},
		Dir:        root,
	}, nil
}

// projectChildFor returns root's project child, starting it on first use.
//
// The lookup and the insert both happen under projMu, so concurrent first
// callers share one entry; the start itself runs under the child's own mutex, so
// one project's slow handshake never blocks another's (decision 1).
func (f *Fleet) projectChildFor(ctx context.Context, root string) (*projectChild, error) {
	select {
	case <-f.done:
		return nil, fmt.Errorf("bridge: fleet is closing")
	default:
	}
	root = filepath.Clean(root)

	f.projMu.Lock()
	if f.projects == nil {
		f.projects = make(map[string]*projectChild)
	}
	pc, ok := f.projects[root]
	if !ok {
		pc = &projectChild{root: root, socket: viewerSocketPath(f.stateDir, root)}
		f.projects[root] = pc
	}
	f.projMu.Unlock()

	// Resolving a project child is an attach, so an armed linger is called off
	// (decision 4). Only a start arms one, which is why a second resolve of a
	// live child leaves nothing armed.
	pc.cancelLinger()

	if err := f.startProjectChild(ctx, pc); err != nil {
		// A child that could not come up must not be found again by the next
		// caller: drop this exact entry and let the next call build a fresh
		// one.
		f.forgetProjectChild(pc)
		return nil, err
	}
	return pc, nil
}

// forgetProjectChild removes pc from the map, if it is still the entry there.
func (f *Fleet) forgetProjectChild(pc *projectChild) {
	f.projMu.Lock()
	if f.projects[pc.root] == pc {
		delete(f.projects, pc.root)
	}
	f.projMu.Unlock()
}

// startProjectChild builds, starts and handshakes pc's child, once. Concurrent
// callers share one start through pc.mu.
func (f *Fleet) startProjectChild(ctx context.Context, pc *projectChild) error {
	// A stop in flight keeps the viewer socket until its process is gone, so
	// wait it out rather than start a replacement that cannot bind.
	pc.waitStop()

	child, err := f.startProjectChildLocked(ctx, pc)
	if child != nil {
		// A child whose handshake failed is unusable. It is stopped here, off
		// the lock, before the next caller tries the same viewer socket path:
		// a process that has not let go of its socket yet would make the
		// replacement fail to bind.
		pc.stopChild(child)
	}
	if err != nil {
		return err
	}
	if err := f.projectChildUsable(pc); err != nil {
		// The fleet closed, or this entry was reaped, while the start was in
		// flight: nothing would ever stop this child, so end it here rather
		// than leave a marshal acp process with no owner.
		pc.stop()
		return err
	}
	return nil
}

// projectChildUsable reports whether pc is still the fleet's live entry for
// its root. A nil projects map means the fleet is closing.
func (f *Fleet) projectChildUsable(pc *projectChild) error {
	f.projMu.Lock()
	defer f.projMu.Unlock()
	if f.projects == nil {
		return fmt.Errorf("bridge: fleet is closing")
	}
	if f.projects[pc.root] != pc {
		return fmt.Errorf("bridge: project child for %s was reaped", pc.root)
	}
	return nil
}

// startProjectChildLocked is startProjectChild's body under pc.mu. It returns a
// non-nil child only when that child must be stopped: it started, but its
// handshake failed.
func (f *Fleet) startProjectChildLocked(ctx context.Context, pc *projectChild) (*Child, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.ready {
		return nil, nil
	}
	child := pc.child
	if child == nil {
		built, err := f.buildProjectChild(pc.root, pc.socket)
		if err != nil {
			return nil, err
		}
		// A restarted child has forgotten every session. Re-run initialize on
		// the new generation before NewRegistry chains resumeAll after this
		// handler, so each session/resume is ordered after the re-initialize
		// (decision 4).
		built.OnRestart = func() { f.reinitProjectChild(pc) }
		child = built
		pc.child = child
		pc.reg = NewRegistry(child)
		pc.ready, pc.version = false, ""
		if err := child.Start(); err != nil {
			pc.child = nil
			return nil, fmt.Errorf("bridge: start project child for %s: %w (stderr: %s)",
				pc.root, err, child.StderrLog())
		}
		// A child nothing has attached to yet lingers rather than living on
		// forever (decision 4).
		f.armLinger(pc)
	}

	hctx, cancel := context.WithTimeout(ctx, controlHandshakeTimeout)
	defer cancel()
	if err := f.projectHandshake(hctx, pc); err != nil {
		// A handshake that fails on a live child means it is unusable, and the
		// next call should build a fresh one.
		pc.disarmLinger()
		pc.child, pc.ready = nil, false
		return child, err
	}
	return nil, nil
}

// projectHandshake runs initialize on the current generation and records the
// version it reported (decision 3). The caller holds pc.mu.
func (f *Fleet) projectHandshake(ctx context.Context, pc *projectChild) error {
	child := pc.child
	if child == nil {
		return fmt.Errorf("bridge: project child for %s is not running", pc.root)
	}
	raw, err := child.Request(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		pc.ready = false
		return fmt.Errorf("bridge: project initialize for %s: %w (stderr: %s)",
			pc.root, err, child.StderrLog())
	}
	var res struct {
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		// A child that answers its handshake with something that is not the
		// initialize result is unusable, the same as one that does not answer.
		pc.ready = false
		return fmt.Errorf("bridge: decode project initialize for %s: %w", pc.root, err)
	}
	pc.version = res.AgentInfo.Version
	pc.ready = true
	return nil
}

// reinitProjectChild re-runs the handshake on a restarted generation, so the
// registry's chained resumeAll resumes the tracked sessions against an
// initialized agent (decision 4).
func (f *Fleet) reinitProjectChild(pc *projectChild) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.ready = false
	if pc.child == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlHandshakeTimeout)
	defer cancel()
	if err := f.projectHandshake(ctx, pc); err != nil {
		slog.Default().Warn("webbridge: project child restart handshake failed",
			"root", pc.root, "err", err)
	}
}

// lingerDuration is how long an empty project child lives on: the fleet's own
// duration when a test set one, else projectLinger.
func (f *Fleet) lingerDuration() time.Duration {
	if f.lingerDur > 0 {
		return f.lingerDur
	}
	return projectLinger
}

// armLinger arms the stop timer for a child with no tracked session, through
// the fleet's deadline seam when one is set (decision 4). It is a no-op when a
// timer is already armed. The caller holds pc.mu.
func (f *Fleet) armLinger(pc *projectChild) {
	if pc.linger != nil {
		return
	}
	d := f.lingerDuration()
	fire := func() {
		// The timer has fired, so nothing is armed any more. Read the
		// registry under the same lock the start path uses: a session that
		// arrived while the timer was armed keeps the child alive, because
		// the linger is for a project child with no tracked session
		// (decision 4) and a session only ever leaves through the registry.
		pc.mu.Lock()
		pc.linger = nil
		reg := pc.reg
		pc.mu.Unlock()
		if reg != nil && reg.count() > 0 {
			return
		}
		// Only the child still registered for its root is reaped: a child that
		// a later resolve has already replaced must be left alone. It is
		// stopped before it is forgotten, so a resolver arriving in between
		// waits for the process (waitStop) instead of racing it for the
		// viewer socket path.
		f.projMu.Lock()
		current := f.projects[pc.root] == pc
		f.projMu.Unlock()
		if !current {
			return
		}
		pc.stop()
		f.projMu.Lock()
		if f.projects[pc.root] == pc {
			delete(f.projects, pc.root)
		}
		f.projMu.Unlock()
	}
	if f.afterFunc != nil {
		stop := f.afterFunc(d, fire)
		if stop == nil {
			stop = func() {}
		}
		pc.linger = stop
		return
	}
	timer := time.AfterFunc(d, fire)
	pc.linger = func() { timer.Stop() }
}

// armProjectLinger arms the linger on a project child that tracks no
// session, taking the child's own lock (the relay end path has none).
func (f *Fleet) armProjectLinger(pc *projectChild) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	f.armLinger(pc)
}

// cancelLinger disarms an armed linger timer, if any (decision 4).
func (pc *projectChild) cancelLinger() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.disarmLinger()
}

// disarmLinger stops an armed timer. The caller holds pc.mu.
func (pc *projectChild) disarmLinger() {
	if pc.linger == nil {
		return
	}
	stop := pc.linger
	pc.linger = nil
	stop()
}

// stop ends a project child. It stops it rather than detaching it: a project
// child holds no work worth reattaching to (decision 4).
func (pc *projectChild) stop() {
	pc.cancelLinger()
	pc.mu.Lock()
	child := pc.child
	pc.child, pc.ready = nil, false
	pc.mu.Unlock()
	pc.stopChild(child)
}

// stopChild ends one child and keeps a marker open until its process is gone,
// so a resolver that arrives meanwhile waits for the viewer socket path rather
// than starting a replacement whose process cannot bind it.
func (pc *projectChild) stopChild(child *Child) {
	if child == nil {
		return
	}
	pc.stopMu.Lock()
	if pc.stopping == nil {
		pc.stopping = make(chan struct{})
	}
	marker := pc.stopping
	pc.stopMu.Unlock()
	child.Stop()
	pc.stopMu.Lock()
	if pc.stopping == marker {
		close(marker)
		pc.stopping = nil
	}
	pc.stopMu.Unlock()
}

// waitStop blocks while a stop of this child is in flight. A stop is bounded by
// Child.Stop's own grace period, so this cannot wait forever.
func (pc *projectChild) waitStop() {
	pc.stopMu.Lock()
	marker := pc.stopping
	pc.stopMu.Unlock()
	if marker != nil {
		<-marker
	}
}

// rearmLinger restores the linger an attach cancelled when it resolved this
// child. Resolving cancels the timer (decision 4), so an attach that fails
// afterwards must put it back: otherwise the child is left with no tracked
// session and nothing armed to reap it.
func (f *Fleet) rearmLinger(pc *projectChild) {
	if pc.reg == nil || pc.reg.count() > 0 {
		return
	}
	f.armProjectLinger(pc)
}

// stopProjectChildren stops every project child; Close calls it.
func (f *Fleet) stopProjectChildren() {
	f.projMu.Lock()
	children := make([]*projectChild, 0, len(f.projects))
	for _, pc := range f.projects {
		children = append(children, pc)
	}
	f.projects = nil
	f.projMu.Unlock()

	var wg sync.WaitGroup
	for _, pc := range children {
		wg.Add(1)
		go func(pc *projectChild) {
			defer wg.Done()
			pc.stop()
		}(pc)
	}
	wg.Wait()
}
