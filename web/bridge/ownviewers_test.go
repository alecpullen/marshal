package bridge

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ownChildFleet builds a fleet with the production agent factory intact and no
// container runtime, so newRuntime takes its host-process fallback, over a state
// directory short enough for a viewer socket path.
func ownChildFleet(t *testing.T) *Fleet {
	t.Helper()
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if _, err := ws.Load(); err != nil {
		t.Fatal(err)
	}
	f := NewFleet(ws, "marshal-test-bin", nil, shortStateDir(t), Limits{}, "", nil, "marshal-state")
	f.probe = func() (path, name string, ok bool) { return "", "", false }
	t.Cleanup(f.Close)
	return f
}

// socketWatchingTransport records whether a file was still at socket when the
// child's transport opened — that is, when the child started.
type socketWatchingTransport struct {
	*fakeTransport
	socket string

	mu       sync.Mutex
	sawStale bool
}

func (s *socketWatchingTransport) Open() (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	s.mu.Lock()
	if _, err := os.Stat(s.socket); err == nil {
		s.sawStale = true
	}
	s.mu.Unlock()
	return s.fakeTransport.Open()
}

func (s *socketWatchingTransport) sawStaleAtStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawStale
}

// TestAnOwnChildStartsWithAViewerSocket: an agent with a host-process child of
// its own starts marshal with --viewers on its own socket under the state
// directory's v/, and the runtime records that path (decision 1).
func TestAnOwnChildStartsWithAViewerSocket(t *testing.T) {
	f := ownChildFleet(t)
	a := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}

	want := agentViewerSocketPath(f.stateDir, a.ID)
	// The binary is not on this machine, so the start itself fails; the
	// arguments and the recorded socket do not depend on it.
	rt, _ := f.startRuntime(ctlContext(t), a)
	if rt == nil || rt.child == nil {
		t.Fatal("startRuntime returned no runtime with a child")
	}
	if got := rt.child.Args; len(got) != 2 || got[0] != "--viewers" || got[1] != "unix://"+want {
		t.Fatalf("Args = %q, want --viewers unix://%s", got, want)
	}
	if rt.viewerSocket != want {
		t.Fatalf("viewerSocket = %q, want %q", rt.viewerSocket, want)
	}
	if dir := filepath.Dir(want); dir != filepath.Join(f.stateDir, "v") {
		t.Fatalf("the viewer socket %q is not under the state directory's v/", want)
	}
	base := filepath.Base(want)
	if !strings.HasPrefix(base, "a-") || !strings.HasSuffix(base, ".sock") {
		t.Fatalf("the viewer socket %q is not an a-<digest>.sock name", want)
	}
	di, err := os.Stat(filepath.Dir(want))
	if err != nil {
		t.Fatalf("stat the socket's directory: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("the socket's directory mode = %04o, want 0700", got)
	}
}

// TestAContainerChildHasNoViewerSocket: a containerized child's arguments are
// unchanged and the runtime records no viewer socket (decision 1).
func TestAContainerChildHasNoViewerSocket(t *testing.T) {
	f := ownChildFleet(t)
	f.runner = func(string, ...string) ([]byte, error) { return nil, nil }
	f.probe = func() (path, name string, ok bool) {
		return "/usr/local/bin/docker", "docker", true
	}
	a := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}

	child, err := f.newRuntime(a)
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if !child.Containerized {
		t.Fatal("the agent's child is not containerized")
	}
	if len(child.Args) != 0 {
		t.Fatalf("Args = %q, want none for a container's child", child.Args)
	}

	// The transport is a fake so the test never starts a container; the
	// containerized child is what the runtime records.
	tr := newFakeTransport()
	f.newRuntime = func(Agent) (*Child, error) {
		return &Child{Transport: tr, Containerized: true}, nil
	}
	rt, err := f.startRuntime(ctlContext(t), a)
	if err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	if rt.viewerSocket != "" {
		t.Fatalf("viewerSocket = %q, want none for a container's child", rt.viewerSocket)
	}
	if _, err := os.Stat(filepath.Dir(agentViewerSocketPath(f.stateDir, a.ID))); !os.IsNotExist(err) {
		t.Fatalf("a viewer socket directory was made for a container agent: %v", err)
	}
}

// TestAStaleViewerSocketIsRemoved: a file left at the agent's socket path is
// gone before the child it belongs to starts (decision 1).
func TestAStaleViewerSocketIsRemoved(t *testing.T) {
	f := ownChildFleet(t)
	a := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}
	socket := agentViewerSocketPath(f.stateDir, a.ID)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tr := &socketWatchingTransport{fakeTransport: newFakeTransport(), socket: socket}
	f.newRuntime = func(Agent) (*Child, error) { return &Child{Transport: tr}, nil }

	if _, err := f.startRuntime(ctlContext(t), a); err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	if tr.sawStaleAtStart() {
		t.Fatal("the stale viewer socket was still there when the child started")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("the stale viewer socket survived: %v", err)
	}
}

// TestTheViewerSocketGoesWithTheChild: releasing an agent destroys its child's
// viewer socket with it, while a child that is only detached keeps its own
// (decision 1).
func TestTheViewerSocketGoesWithTheChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		destroy bool
		gone    bool
	}{
		{name: "destroyed", destroy: true, gone: true},
		{name: "detached", destroy: false, gone: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ownChildFleet(t)
			a := Agent{ID: "a1", Project: t.TempDir(), SourceKind: "local", Profile: DefaultRuntimeProfile()}
			tr := newFakeTransport()
			f.newRuntime = func(Agent) (*Child, error) { return &Child{Transport: tr}, nil }
			if _, err := f.startRuntime(ctlContext(t), a); err != nil {
				t.Fatalf("startRuntime: %v", err)
			}
			socket := agentViewerSocketPath(f.stateDir, a.ID)
			if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(socket, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			f.releaseAgent(a.ID, tc.destroy)

			_, err := os.Stat(socket)
			if tc.gone && !os.IsNotExist(err) {
				t.Fatalf("the viewer socket outlived its child: %v", err)
			}
			if !tc.gone && err != nil {
				t.Fatalf("a detached child lost its viewer socket: %v", err)
			}
		})
	}
}

// TestAgentViewerSocketPathFitsTheLimit: the name is a digest of the agent id,
// not the id itself, so a long id under a long state directory stays inside the
// 104 bytes macOS allows for a unix socket path (decision 1).
func TestAgentViewerSocketPathFitsTheLimit(t *testing.T) {
	state := filepath.Join(string(filepath.Separator), "state")
	longID := "agent-" + strings.Repeat("x", 200)
	got := agentViewerSocketPath(state, longID)

	if dir := filepath.Dir(got); dir != filepath.Join(state, "v") {
		t.Fatalf("agentViewerSocketPath(%q) = %q, not under the state directory's v/", longID, got)
	}
	base := filepath.Base(got)
	if !strings.HasPrefix(base, "a-") || !strings.HasSuffix(base, ".sock") {
		t.Fatalf("agentViewerSocketPath(%q) = %q, want an a-<digest>.sock name", longID, got)
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(base, "a-"), ".sock")
	if len(digest) != 16 {
		t.Fatalf("the digest %q is %d hex digits, want 16", digest, len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		t.Fatalf("the digest %q is not hex: %v", digest, err)
	}
	if len(got) > 104 {
		t.Fatalf("agentViewerSocketPath(%q) = %q is %d bytes, too long for a unix socket", longID, got, len(got))
	}
	if agentViewerSocketPath(state, "a1") == agentViewerSocketPath(state, "a2") {
		t.Fatal("two agents share one viewer socket path")
	}
	if agentViewerSocketPath(state, longID) != agentViewerSocketPath(state, longID) {
		t.Fatal("one agent yielded two viewer socket paths")
	}
	// The a- keeps an agent's socket apart from a project child's name.
	if agentViewerSocketPath(state, "a1") == viewerSocketPath(state, "a1") {
		t.Fatal("an agent's socket has the same name as a project's")
	}
}
