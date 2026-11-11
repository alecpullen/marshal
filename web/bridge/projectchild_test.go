package bridge

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortStateDir returns a state directory short enough for the viewer socket
// path: macOS caps a unix socket path at 104 bytes, and t.TempDir() is already
// close to that under the suite's TMPDIR.
func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "p4c")
	if err != nil {
		t.Fatalf("make state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// shortenLinger makes the linger short enough that a test never keeps a timer
// alive for the production 30 seconds. The duration is the fleet's: a relay's
// own goroutine reads it while the test is cleaning up.
func shortenLinger(t *testing.T, f *Fleet) {
	t.Helper()
	f.lingerDur = 50 * time.Millisecond
}

// projectFleet builds a fleet whose project children are fake transports over
// a short state directory, and returns the fake built for a root.
func projectFleet(t *testing.T) (*Fleet, func(root string) *fakeProjectChild) {
	t.Helper()
	return projectFleetWith(t, nil)
}

// projectFleetWith is projectFleet with a hook that configures each fake before
// the bridge can use it.
func projectFleetWith(t *testing.T, configure func(*fakeProjectChild)) (*Fleet, func(root string) *fakeProjectChild) {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "unused", nil, shortStateDir(t), Limits{}, "", nil, "marshal-state")
	t.Cleanup(f.Close)

	var mu sync.Mutex
	byRoot := map[string]*fakeProjectChild{}
	f.newProjectChild = func(root, socket string) (*Child, error) {
		fake := newFakeProjectChild(socket)
		if configure != nil {
			configure(fake)
		}
		mu.Lock()
		byRoot[root] = fake
		mu.Unlock()
		return &Child{Transport: fake, Dir: root}, nil
	}
	return f, func(root string) *fakeProjectChild {
		mu.Lock()
		defer mu.Unlock()
		return byRoot[root]
	}
}

// projectEntry returns root's entry in the fleet's map.
func projectEntry(f *Fleet, root string) (*projectChild, bool) {
	f.projMu.Lock()
	defer f.projMu.Unlock()
	pc, ok := f.projects[root]
	return pc, ok
}

// lingerTimer is one timer armed through the fleet's deadline seam.
type lingerTimer struct {
	d    time.Duration
	fire func()

	mu      sync.Mutex
	stopped bool
}

func (t *lingerTimer) stopFunc() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

func (t *lingerTimer) wasStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

// lingerKit captures the timers a fleet arms, so a test fires one itself
// instead of waiting out the linger.
type lingerKit struct {
	mu    sync.Mutex
	armed []*lingerTimer
}

func captureTimers(f *Fleet) *lingerKit {
	kit := &lingerKit{}
	f.afterFunc = func(d time.Duration, fn func()) func() {
		timer := &lingerTimer{d: d, fire: fn}
		kit.mu.Lock()
		kit.armed = append(kit.armed, timer)
		kit.mu.Unlock()
		return timer.stopFunc
	}
	return kit
}

func (k *lingerKit) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.armed)
}

func (k *lingerKit) at(i int) *lingerTimer {
	k.mu.Lock()
	defer k.mu.Unlock()
	if i >= len(k.armed) {
		return nil
	}
	return k.armed[i]
}

// TestAProjectChildStartsOnFirstUse: the first resolve starts one child and
// handshakes on it, a second resolve of the same root returns the same child
// and starts nothing, and another root gets a child of its own.
func TestAProjectChildStartsOnFirstUse(t *testing.T) {
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("first projectChildFor: %v", err)
	}
	if pc.child == nil || pc.reg == nil {
		t.Fatalf("the project child was not started: %+v", pc)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want 1", gens)
	}
	inits := fake.calls("initialize")
	if len(inits) != 1 {
		t.Fatalf("initialize calls = %v, want exactly one", inits)
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(inits[0]), &params); err != nil {
		t.Fatalf("decode initialize params %q: %v", inits[0], err)
	}
	if len(params) != 1 || params["protocolVersion"] != float64(1) {
		t.Fatalf("initialize params = %s, want only protocolVersion 1", inits[0])
	}

	again, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("second projectChildFor: %v", err)
	}
	if again != pc {
		t.Fatal("the second call for the same root returned a different child")
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("the second call started another child: generations = %d", gens)
	}

	otherRoot := t.TempDir()
	other, err := f.projectChildFor(ctx, otherRoot)
	if err != nil {
		t.Fatalf("projectChildFor another root: %v", err)
	}
	if other == pc {
		t.Fatal("another root shared the first root's child")
	}
	otherFake := fakeFor(other.root)
	if otherFake == nil {
		t.Fatal("no fake was built for the second root")
	}
	if gens := otherFake.generations(); gens != 1 {
		t.Fatalf("the second root's generations = %d, want 1", gens)
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("the first child was started again: generations = %d", gens)
	}
}

// TestTheChildRunsInTheRootWithItsSocket: with no fake, the built child runs
// marshal in the root with --viewers on a socket whose directory already
// exists. The child is inspected, never started.
func TestTheChildRunsInTheRootWithItsSocket(t *testing.T) {
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	state := shortStateDir(t)
	f := NewFleet(ws, "marshal-test-bin", nil, state, Limits{}, "", nil, "marshal-state")
	t.Cleanup(f.Close)
	root := t.TempDir()

	socket := viewerSocketPath(f.stateDir, root)
	child, warning, err := f.buildProjectChild(ctlContext(t), root, socket)
	if err != nil {
		t.Fatalf("buildProjectChild: %v", err)
	}
	if warning != "" {
		t.Fatalf("warning = %q, want none without a resolver", warning)
	}
	if child.Env != nil {
		t.Fatalf("Env = %q, want nil without a resolver", child.Env)
	}
	if child.Dir != root {
		t.Fatalf("Dir = %q, want the project root %q", child.Dir, root)
	}
	if child.MarshalBin != f.marshalBin {
		t.Fatalf("MarshalBin = %q, want %q", child.MarshalBin, f.marshalBin)
	}
	want := []string{"--viewers", "unix://" + socket}
	if len(child.Args) != len(want) || child.Args[0] != want[0] || child.Args[1] != want[1] {
		t.Fatalf("Args = %q, want %q", child.Args, want)
	}
	if !strings.HasPrefix(child.Args[1], "unix://"+filepath.Join(state, "v")+string(filepath.Separator)) {
		t.Fatalf("the viewer socket %q is not under the state directory's v/", child.Args[1])
	}
	di, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatalf("stat the socket's directory: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("the socket's directory mode = %04o, want 0700", got)
	}
}

// TestTheViewerSocketPathIsShort: the name is a digest, not the root, so a root
// of any length yields the same short path under the state directory.
func TestTheViewerSocketPathIsShort(t *testing.T) {
	state := filepath.Join(string(filepath.Separator), "state")
	long := filepath.Join(string(filepath.Separator), strings.Repeat("project/", 40), "root")
	roots := []string{"/tmp/a", long, long + "/nested"}
	for _, root := range roots {
		got := viewerSocketPath(state, root)
		if dir := filepath.Dir(got); dir != filepath.Join(state, "v") {
			t.Fatalf("viewerSocketPath(%q) = %q, not under the state directory's v/", root, got)
		}
		name := filepath.Base(got)
		if !strings.HasSuffix(name, ".sock") {
			t.Fatalf("viewerSocketPath(%q) = %q, want a .sock name", root, got)
		}
		digest := strings.TrimSuffix(name, ".sock")
		if len(digest) != 16 {
			t.Fatalf("viewerSocketPath(%q) digest = %q, want 16 hex digits", root, digest)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			t.Fatalf("viewerSocketPath(%q) digest = %q, want hex: %v", root, digest, err)
		}
		if len(got) > 100 {
			t.Fatalf("viewerSocketPath(%q) = %q is %d bytes, too long for a unix socket", root, got, len(got))
		}
	}
	if viewerSocketPath(state, "/tmp/a") == viewerSocketPath(state, "/tmp/b") {
		t.Fatal("two roots share one viewer socket path")
	}
	if viewerSocketPath(state, long) != viewerSocketPath(state, long) {
		t.Fatal("one root yielded two viewer socket paths")
	}
}

// TestAFailedHandshakeDropsTheChild: a child whose initialize fails leaves
// nothing behind, and the next call starts a fresh one.
func TestAFailedHandshakeDropsTheChild(t *testing.T) {
	var mu sync.Mutex
	failures := 1
	f, fakeFor := projectFleetWith(t, func(fake *fakeProjectChild) {
		mu.Lock()
		fail := failures > 0
		if fail {
			failures--
		}
		mu.Unlock()
		if !fail {
			return
		}
		fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method == "initialize" {
				return nil, &rpcError{Code: -32000, Message: "handshake refused"}, true
			}
			return nil, nil, false
		}
	})
	ctx := ctlContext(t)
	root := t.TempDir()

	_, err := f.projectChildFor(ctx, root)
	if err == nil {
		t.Fatal("a failed handshake was reported as a start")
	}
	if !strings.Contains(err.Error(), "handshake refused") {
		t.Fatalf("error = %v, want the handshake failure", err)
	}
	if pc, ok := projectEntry(f, filepath.Clean(root)); ok {
		t.Fatalf("the unusable child was left in the map: %+v", pc)
	}

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("the next call did not start a fresh child: %v", err)
	}
	if pc.child == nil || !pc.ready {
		t.Fatal("the fresh child is not ready")
	}
	if fake := fakeFor(pc.root); fake == nil || fake.generations() != 1 {
		t.Fatalf("the fresh child did not get its own transport: %+v", fake)
	}
}

// TestAnEmptyChildLingersThenStops: a child with no session arms the linger,
// and when it fires the child stops and the fleet forgets it.
func TestAnEmptyChildLingersThenStops(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built")
	}
	if n := timers.count(); n != 1 {
		t.Fatalf("armed timers = %d, want 1", n)
	}
	timer := timers.at(0)
	if want := f.lingerDuration(); timer.d != want {
		t.Fatalf("linger = %v, want %v", timer.d, want)
	}

	timer.fire()

	if !fake.wasStopped() {
		t.Fatal("the lingered child was not stopped")
	}
	if entry, ok := projectEntry(f, pc.root); ok {
		t.Fatalf("the lingered child is still registered: %+v", entry)
	}

	next, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor after the linger fired: %v", err)
	}
	if next == pc {
		t.Fatal("the forgotten child was handed out again")
	}
	if replacement := fakeFor(next.root); replacement == nil || replacement == fake {
		t.Fatal("the next call did not build a fresh child")
	}
	if n := timers.count(); n != 2 {
		t.Fatalf("armed timers = %d, want a second one for the fresh child", n)
	}
}

// TestARearmArmsOnlyAnEmptyChild: an attach that fails after resolving a
// child must put the linger back, and a child a session keeps alive must not
// get one. Without the re-arm the child has no session and no timer, so
// nothing ever reaps it.
func TestARearmArmsOnlyAnEmptyChild(t *testing.T) {
	f, _ := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	first := timers.at(0)
	if first == nil {
		t.Fatal("starting the child armed no linger")
	}

	// Resolving a child calls its linger off, which is what a failing attach
	// does before it gives up.
	pc.cancelLinger()
	if !first.wasStopped() {
		t.Fatal("cancelLinger left the timer armed")
	}
	f.rearmLinger(pc)
	if n := timers.count(); n != 2 {
		t.Fatalf("armed timers = %d, want the linger re-armed for an empty child", n)
	}

	// A tracked session keeps the child alive on its own; no timer is needed.
	pc.reg.track("s1", AgentPath(root))
	second := timers.at(1)
	pc.cancelLinger()
	f.rearmLinger(pc)
	if !second.wasStopped() {
		t.Fatal("cancelLinger left the second timer armed")
	}
	if n := timers.count(); n != 2 {
		t.Errorf("armed timers = %d, want no re-arm while a session is tracked", n)
	}
}

// TestAnAttachCancelsTheLinger: resolving the child again before the timer
// fires calls the timer off.
func TestAnAttachCancelsTheLinger(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	if n := timers.count(); n != 1 {
		t.Fatalf("armed timers = %d, want 1", n)
	}
	timer := timers.at(0)
	fake := fakeFor(pc.root)

	again, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("second projectChildFor: %v", err)
	}
	if again != pc {
		t.Fatal("the second resolve returned a different child")
	}
	if !timer.wasStopped() {
		t.Fatal("resolving the child again left the linger armed")
	}
	if n := timers.count(); n != 1 {
		t.Fatalf("armed timers = %d, want no re-arm for a live child", n)
	}
	if fake.wasStopped() {
		t.Fatal("the cancelled linger still stopped the child")
	}
	if entry, ok := projectEntry(f, pc.root); !ok || entry != pc {
		t.Fatal("the resolved child is no longer registered")
	}
}

// TestTheFleetsCloseStopsProjectChildren: Close stops every project child, and
// a later resolve refuses.
func TestTheFleetsCloseStopsProjectChildren(t *testing.T) {
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)

	var fakes []*fakeProjectChild
	var roots []string
	for i := 0; i < 2; i++ {
		root := t.TempDir()
		if _, err := f.projectChildFor(ctx, root); err != nil {
			t.Fatalf("projectChildFor %s: %v", root, err)
		}
		roots = append(roots, root)
		fake := fakeFor(filepath.Clean(root))
		if fake == nil {
			t.Fatalf("no fake was built for %s", root)
		}
		fakes = append(fakes, fake)
	}

	f.Close()

	for i, fake := range fakes {
		fake.waitStopped(t, 5*time.Second)
		if !fake.wasStopped() {
			t.Fatalf("project child %d was not stopped", i)
		}
	}
	if entry, ok := projectEntry(f, filepath.Clean(roots[0])); ok {
		t.Fatalf("a closed fleet still holds a project child: %+v", entry)
	}
	if _, err := f.projectChildFor(ctx, roots[0]); err == nil {
		t.Fatal("a closed fleet served a project child")
	} else if !strings.Contains(err.Error(), "closing") {
		t.Fatalf("error = %v, want a refusal naming the close", err)
	}
}

// TestARestartedChildResumesItsSessions: after the child dies, the new
// generation re-initializes and then resumes every tracked session.
func TestARestartedChildResumesItsSessions(t *testing.T) {
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	var sessions []string
	for i := 0; i < 2; i++ {
		sid, err := pc.reg.New(ctx, AgentPath(root), "")
		if err != nil {
			t.Fatalf("session/new: %v", err)
		}
		sessions = append(sessions, sid)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built")
	}

	fake.die()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if resumed := resumedSessions(fake); len(resumed) >= len(sessions) {
			for _, sid := range sessions {
				if !resumed[sid] {
					t.Fatalf("session %s was not resumed; resumed %v", sid, resumed)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no resume after the restart; resumed %v", resumedSessions(fake))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A restart's resumes carry the same mark: the fresh generation must let
	// each session's marshal client answer the trust question (P4 D8).
	resumes := fake.calls("session/resume")
	if len(resumes) == 0 {
		t.Fatal("no session/resume was recorded")
	}
	for _, params := range resumes {
		if got := paramsOf(t, params)["clientTrust"]; got != true {
			t.Errorf("session/resume clientTrust = %v, want true (params %s)", got, params)
		}
	}

	if gens := fake.generations(); gens < 2 {
		t.Fatalf("generations = %d, want the child to have restarted", gens)
	}
	// The resume must follow the restarted generation's own initialize.
	lastInit, firstResume := -1, -1
	for i, rec := range fake.record() {
		switch rec.frame.method {
		case "initialize":
			lastInit = i
		case "session/resume":
			if firstResume < 0 {
				firstResume = i
			}
		}
	}
	if lastInit < 0 || firstResume < 0 {
		t.Fatalf("initialize at %d, first session/resume at %d", lastInit, firstResume)
	}
	if firstResume < lastInit {
		t.Fatal("session/resume ran before the restarted generation was initialized")
	}
}

// TestOtherRegistriesSendNoMark: only a project child's registry marks its
// sessions. A registry built for anything else — the fleet's, the control
// agent's — sends session/new without the field.
func TestOtherRegistriesSendNoMark(t *testing.T) {
	reg, fa := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := reg.New(ctx, AgentPath("/home/u/repo"), ""); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	calls := fa.calls("session/new")
	if len(calls) != 1 {
		t.Fatalf("session/new sent %d times, want 1", len(calls))
	}
	if _, ok := paramsOf(t, calls[0])["clientTrust"]; ok {
		t.Errorf("session/new carried clientTrust, want the field omitted: %s", calls[0])
	}
}

// resumedSessions returns the session ids the fake resumed, by id.
func resumedSessions(fake *fakeProjectChild) map[string]bool {
	out := map[string]bool{}
	for _, params := range fake.calls("session/resume") {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal([]byte(params), &p) == nil && p.SessionID != "" {
			out[p.SessionID] = true
		}
	}
	return out
}

// projectEnvFixture is a resolver whose login shell is a script: it ignores
// its arguments and prints what body says, so a test decides what the shell
// resolves without starting a login shell of its own.
func projectEnvFixture(t *testing.T, body string) (*ProjectEnv, []string) {
	t.Helper()
	dir := p6Dir(t)
	base := []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	return &ProjectEnv{
		PrintEnv: []string{p6Script(t, dir, "printer", p6PrinterScript)},
		Shell:    p6Script(t, dir, "shell", body),
		Base:     base,
		Timeout:  10 * time.Second,
	}, base
}

// TestAProjectChildStartsWithTheProjectsEnvironment: with a resolver, the
// built child runs with the environment it resolved, and the daemon reports
// no fallback for it.
func TestAProjectChildStartsWithTheProjectsEnvironment(t *testing.T) {
	f, _ := projectFleet(t)
	res, _ := projectEnvFixture(t, "#!/bin/sh\nprintf '\\000marshal-env\\000P6=1\\000'\n")
	f.SetProjectEnv(res)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	if v, ok := p6Value(pc.child.Env, "P6"); !ok || v != "1" {
		t.Fatalf("the child's Env = %q, want P6=1 from the project environment", pc.child.Env)
	}

	children := f.projectChildren()
	if len(children) != 1 {
		t.Fatalf("projectChildren() = %+v, want one child", children)
	}
	if children[0].Root != pc.root {
		t.Fatalf("projectChildren() root = %q, want %q", children[0].Root, pc.root)
	}
	if children[0].EnvWarning != "" {
		t.Fatalf("EnvWarning = %q, want none for a resolved environment", children[0].EnvWarning)
	}
}

// TestAFallbackIsReportedOnTheChild: a login shell that fails leaves the child
// with the resolver's base environment, and the daemon reports why.
func TestAFallbackIsReportedOnTheChild(t *testing.T) {
	f, _ := projectFleet(t)
	res, base := projectEnvFixture(t, "#!/bin/sh\nexit 2\n")
	f.SetProjectEnv(res)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	if !slices.Equal(pc.child.Env, base) {
		t.Fatalf("the child's Env = %q, want the resolver's Base %q", pc.child.Env, base)
	}

	children := f.projectChildren()
	if len(children) != 1 {
		t.Fatalf("projectChildren() = %+v, want one child", children)
	}
	if !strings.Contains(children[0].EnvWarning, "2") {
		t.Fatalf("EnvWarning = %q, want the login shell's exit status", children[0].EnvWarning)
	}
}

// TestWithoutAResolverChildrenInherit: a fleet nothing configured with a
// resolver leaves Env nil, which is the child inheriting the bridge's.
func TestWithoutAResolverChildrenInherit(t *testing.T) {
	f, _ := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()

	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	if pc.child.Env != nil {
		t.Fatalf("the child's Env = %q, want nil so it inherits", pc.child.Env)
	}
	if children := f.projectChildren(); len(children) != 1 || children[0].EnvWarning != "" {
		t.Fatalf("projectChildren() = %+v, want one child with no warning", children)
	}
}
