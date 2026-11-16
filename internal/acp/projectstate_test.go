package acp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/watch"
)

// projectHarness is one project child: a host that keeps a record, the bridge's
// own connection to it, and the viewers that attach to it.
type projectHarness struct {
	host   *agentHost
	record *startRecord
	bridge *rpc.Conn
}

// newProjectHarness builds a project child whose record keeps exe and the two
// config files, and serves its bridge connection over a pipe.
func newProjectHarness(t *testing.T, exe, userConfig, projectConfig string) *projectHarness {
	t.Helper()
	record := newStartRecord(exe, userConfig, projectConfig, discardLogger())
	cfg := testRunConfig(t)
	cfg.record = record
	host, err := newAgentHost(cfg)
	if err != nil {
		t.Fatalf("newAgentHost: %v", err)
	}
	p := &projectHarness{host: host, record: record}
	p.bridge = p.serveBridge(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.close(ctx); err != nil {
			t.Errorf("close the host: %v", err)
		}
	})
	return p
}

// serveBridge serves the host's bridge connection, as serveConn does for stdio,
// and returns the client's end.
func (p *projectHarness) serveBridge(t *testing.T) *rpc.Conn {
	t.Helper()
	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(discardLogger()))
	srv.MaxLineBytes = rpc.MaxFrameBytes

	var alive atomic.Bool
	alive.Store(true)
	cleanup := p.host.registerHandlers(srv, &alive, p.host.sink.Notify, false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		cleanup()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the bridge connection did not stop")
		}
	})
	return rpc.NewConn(client, rpc.Handlers{})
}

// viewers is the viewer harness over this host: its methods use nothing but the
// host they serve.
func (p *projectHarness) viewers() *viewerHarness { return &viewerHarness{host: p.host} }

// adopt starts a session in the child, with the record installed on its deps by
// the manager's own wiring, as a project child's session is built.
func (p *projectHarness) adopt(t *testing.T, id string, deps service.Deps) (*app.Runtime, *service.Session) {
	t.Helper()
	state := embeddingState(t, id)
	broker := pubsub.NewBroker[session.Event]()
	state.SetEventBroker(broker)
	rt := &app.Runtime{
		State:       state,
		SessionID:   id,
		EventBroker: broker,
		Config:      config.Default(),
		WorkingDir:  t.TempDir(),
	}
	deps.State = state
	deps.ConfigWritten = p.host.manager.configWritten
	deps.PendingRestart = p.host.manager.pendingRestart
	servedHost, served := service.New(deps)
	if err := p.host.manager.adopt(rt, servedHost, served); err != nil {
		t.Fatalf("adopt %s: %v", id, err)
	}
	return rt, served
}

// gatedRunner is the scripted provider's gate: its run holds until gate closes
// or the run is cancelled.
type gatedRunner struct{ gate <-chan struct{} }

func (r gatedRunner) Run(ctx context.Context, goal string) error {
	select {
	case <-r.gate:
	case <-ctx.Done():
	}
	return nil
}

// notesWith counts the messages in state whose text carries text.
func notesWith(state *session.State, text string) int {
	n := 0
	for _, msg := range state.Messages() {
		if strings.Contains(msg.Content, text) {
			n++
		}
	}
	return n
}

// TestTheConfigManagerReportsItsWrites pins decision 3 for the five config/
// methods: each tells wrote the user config's path once its save succeeded, and
// a call that fails tells nothing.
func TestTheConfigManagerReportsItsWrites(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", home)

	var mu sync.Mutex
	var reported []string
	m := NewConfigManager(home, nil, nil)
	m.wrote = func(path string) {
		mu.Lock()
		reported = append(reported, path)
		mu.Unlock()
	}
	took := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), reported...)
		reported = nil
		return out
	}

	want := config.UserConfigPath(home)
	cases := []struct {
		name string
		call func() error
	}{
		{"config/set_providers", func() error {
			_, err := m.SetProviders(ctx, json.RawMessage(
				`{"providers":{"local":{"type":"openai","baseUrl":"http://127.0.0.1:1/v1","toolCalling":true}}}`))
			return err
		}},
		{"config/set_provider_key", func() error {
			_, err := m.SetProviderKey(ctx, json.RawMessage(`{"name":"local","key":"sk-test"}`))
			return err
		}},
		{"config/set_presets", func() error {
			_, err := m.SetPresets(ctx, json.RawMessage(`{"presets":{"local":{"provider":"local","model":"model"}}}`))
			return err
		}},
		{"config/set_routing", func() error {
			_, err := m.SetRouting(ctx, json.RawMessage(`{"activePreset":"local"}`))
			return err
		}},
		{"config/set_budgets", func() error {
			_, err := m.SetBudgets(ctx, json.RawMessage(
				`{"budgets":{"dailyUsd":1,"perAgentUsd":0.5,"onDailyCap":"warn","onAgentCap":"warn"}}`))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got := took(); len(got) != 1 || got[0] != want {
				t.Fatalf("%s told wrote %v, want exactly [%s]", tc.name, got, want)
			}
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("%s reported a path it did not write: %v", tc.name, err)
			}
		})
	}

	t.Run("a write that fails tells nothing", func(t *testing.T) {
		_, err := m.SetBudgets(ctx, json.RawMessage(
			`{"budgets":{"dailyUsd":-1,"perAgentUsd":0,"onDailyCap":"warn","onAgentCap":"warn"}}`))
		if err == nil {
			t.Fatal("a negative budget cap reported no error")
		}
		if got := took(); len(got) != 0 {
			t.Fatalf("a failed call told wrote %v", got)
		}
	})
}

// TestOnlyAProjectChildServesProjectState pins decision 1: a host with no
// record answers the two project requests as methods that do not exist, as one
// without a hold answers project/hold.
func TestOnlyAProjectChildServesProjectState(t *testing.T) {
	client, _ := startAgentHost(t)
	conn := rpc.NewConn(client, rpc.Handlers{})

	for _, method := range []string{acpwire.MethodProjectState, acpwire.MethodProjectNote} {
		var res json.RawMessage
		err := conn.Call(context.Background(), method, map[string]any{"text": "hello"}, &res)
		if err == nil || !strings.Contains(err.Error(), "method not found") {
			t.Fatalf("%s on a host without a record = %v, want method not found", method, err)
		}
	}
}

// TestProjectStateIsTheBridgesOnly pins decision 6: a viewer's connection is
// refused both project requests with the sentence project/hold's refusal uses.
func TestProjectStateIsTheBridgesOnly(t *testing.T) {
	files := newRecordFiles(t)
	p := newProjectHarness(t, files.executable, files.user, files.project)

	v := p.viewers().openRawViewer(t)
	v.initialize(t, true)

	for _, method := range []string{acpwire.MethodProjectState, acpwire.MethodProjectNote} {
		var res json.RawMessage
		err := v.conn.Call(context.Background(), method, map[string]any{"text": "hello"}, &res)
		if err == nil || !strings.Contains(err.Error(), "a viewer connection cannot start or load a session") {
			t.Fatalf("%s on a viewer = %v, want the viewer's lifecycle refusal", method, err)
		}
	}
}

// TestProjectNoteReachesEverySession pins decision 7: the text becomes one plain
// note in every live session, and the answer is how many it reached.
func TestProjectNoteReachesEverySession(t *testing.T) {
	files := newRecordFiles(t)
	p := newProjectHarness(t, files.executable, files.user, files.project)
	_, first := p.adopt(t, "s1", service.Deps{})
	_, second := p.adopt(t, "s2", service.Deps{})

	const text = "a new marshal build is ready; the child restarts when it is quiet"
	var reached int
	if err := p.bridge.Call(context.Background(), acpwire.MethodProjectNote, acpwire.ProjectNoteParams{Text: text}, &reached); err != nil {
		t.Fatalf("project/note: %v", err)
	}
	if reached != 2 {
		t.Fatalf("project/note reached %d sessions, want 2", reached)
	}
	for _, sess := range []*service.Session{first, second} {
		if got := notesWith(sess.State(), text); got != 1 {
			t.Fatalf("session %s carries the note %d times, want once", sess.ID(), got)
		}
	}
}

// TestProjectStateNamesWhatIsBusy pins decision 5: each session says what holds
// it up, in the fixed order, and the sessions come in creation order.
func TestProjectStateNamesWhatIsBusy(t *testing.T) {
	files := newRecordFiles(t)
	p := newProjectHarness(t, files.executable, files.user, files.project)

	// A session in a run, held by the scripted provider's gate. It has a title,
	// so busy names it by that.
	gate := make(chan struct{})
	var release sync.Once
	releaseGate := func() { release.Do(func() { close(gate) }) }
	t.Cleanup(releaseGate)
	busyRT, busySess := p.adopt(t, "s1", service.Deps{
		Runner: func() service.Runner { return gatedRunner{gate: gate} },
	})
	busyRT.State.SetTitle("planner")
	go func() { _, _ = busySess.Prompt(context.Background(), acpwire.PromptParams{Text: "work"}) }()
	waitFor(t, "the session's run to start", func() bool { return busySess.Status().Run != nil })

	// A session with a watching watch. Its name is its ID.
	watchedRT, _ := p.adopt(t, "s2", service.Deps{})
	target := filepath.Join(t.TempDir(), "watched.txt")
	writeRecordFile(t, target, "x")
	wm := watch.NewManager(context.Background(), watch.Deps{OnEvent: func(watch.Event) {}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wm.Close(ctx)
	})
	if _, _, err := wm.Start(watch.Spec{
		Name:      "build",
		Kind:      watch.KindFile,
		Path:      target,
		Condition: "change",
		Mode:      watch.ModeRepeat,
		Interval:  100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("start the watch: %v", err)
	}
	watchedRT.WatchManager = wm

	var got acpwire.ProjectState
	if err := p.bridge.Call(context.Background(), acpwire.MethodProjectState, nil, &got); err != nil {
		t.Fatalf("project/state: %v", err)
	}
	want := []string{"a run in planner", "watch build in s2"}
	if len(got.Busy) != len(want) {
		t.Fatalf("project/state busy = %v, want %v", got.Busy, want)
	}
	for i := range want {
		if got.Busy[i] != want[i] {
			t.Fatalf("project/state busy = %v, want %v", got.Busy, want)
		}
	}
	if got.Version != AgentVersion {
		t.Fatalf("project/state version = %q, want the child's own %q", got.Version, AgentVersion)
	}
}

// TestTheStatusCarriesAPendingRestart pins decision 4 end to end: after a stale
// project/state answer, a session's status carries the reason and what it waits
// for, and the status forwarder sends it without being asked.
func TestTheStatusCarriesAPendingRestart(t *testing.T) {
	files := newRecordFiles(t)
	p := newProjectHarness(t, files.executable, files.user, files.project)
	p.adopt(t, "s1", service.Deps{})

	// The viewer attaches while the child is current, so its first status has no
	// restart.
	v := p.viewers().openViewer(t)
	sess := attachViewer(t, v, "s1")
	if got := sess.Status().Restart; got != nil {
		t.Fatalf("a current child's status carries restart %+v, want none", got)
	}

	// Someone else changes the user config, and the bridge asks.
	writeRecordFile(t, files.user, "project = { name = \"someone else\" }\n")
	var state acpwire.ProjectState
	if err := p.bridge.Call(context.Background(), acpwire.MethodProjectState, nil, &state); err != nil {
		t.Fatalf("project/state: %v", err)
	}
	if len(state.Stale) != 1 || state.Stale[0] != "config.toml changed" {
		t.Fatalf("project/state stale = %v, want [config.toml changed]", state.Stale)
	}

	// The forwarder sends the new status without a call asking for it.
	waitFor(t, "the status update carrying the restart", func() bool {
		return v.sawText(`"restart"`)
	})

	restart := sess.Status().Restart
	if restart == nil {
		t.Fatal("the status carries no restart after a stale answer")
	}
	if restart.Reason != "config.toml changed" {
		t.Fatalf("the restart's reason = %q, want %q", restart.Reason, "config.toml changed")
	}
	if restart.WaitingFor != "a quiet moment" {
		t.Fatalf("the restart waits for %q, want %q", restart.WaitingFor, "a quiet moment")
	}
}

// TestASettingsWriteLeavesItsChildCurrent pins L4's child half: a settings write
// a session of the child makes never marks the child stale, while the same file
// changed by anyone else does.
func TestASettingsWriteLeavesItsChildCurrent(t *testing.T) {
	files := t.TempDir()
	projectDir := t.TempDir()
	t.Setenv("MARSHAL_CONFIG_DIR", files)

	userConfig := config.UserConfigPath("")
	projectConfig := config.ProjectConfigPath(projectDir)
	writeRecordFile(t, userConfig, "[tools.shell]\nallow_network = true\n")
	writeRecordFile(t, projectConfig, "# the project's config\n")
	exe := filepath.Join(files, "marshal")
	writeRecordFile(t, exe, "the marshal binary")
	record := newStartRecord(exe, userConfig, projectConfig, discardLogger())

	socketPath := filepath.Join(shortTempDir(t), "viewers.sock")
	ln, err := listenUnix(socketPath)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}

	// stdio is the bridge's own connection; the child serves it over a pipe.
	child, bridgeEnd := net.Pipe()
	cfg := testRunConfig(t)
	cfg.record = record
	cfg.startRuntime = func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		state := session.New(config.Default(), projectDir, time.Unix(100, 0), session.Persistence{SessionID: "s1"})
		return &app.Runtime{
			State:      state,
			SessionID:  "s1",
			Config:     config.Default(),
			WorkingDir: projectDir,
			HomeDir:    files,
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithViewersConfig(ctx, child, child, ln, cfg) }()
	t.Cleanup(func() {
		cancel()
		_ = bridgeEnd.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the child did not stop")
		}
	})

	bridge := rpc.NewConn(rpc.NewStream(bridgeEnd, bridgeEnd, rpc.MaxFrameBytes), rpc.Handlers{})
	t.Cleanup(func() { _ = bridge.Close() })

	var created struct {
		SessionID string `json:"sessionId"`
	}
	if err := bridge.Call(ctx, "session/new", map[string]any{"cwd": projectDir, "mcpServers": []any{}}, &created); err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if created.SessionID == "" {
		t.Fatal("session/new answered no session ID")
	}

	// A marshal viewer writes the user config through the settings path.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial the viewer socket: %v", err)
	}
	viewer, err := rpc.Dial(ctx, rpc.NewStream(conn, conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("dial the viewer: %v", err)
	}
	t.Cleanup(func() {
		_ = viewer.Close()
		_ = conn.Close()
	})
	if _, err := viewer.Attach(ctx, created.SessionID); err != nil {
		t.Fatalf("attach the viewer: %v", err)
	}
	if err := viewer.SaveUserValue(ctx, "tools.shell.allow_network", false); err != nil {
		t.Fatalf("the viewer's SaveUserValue: %v", err)
	}

	var state acpwire.ProjectState
	if err := bridge.Call(ctx, acpwire.MethodProjectState, nil, &state); err != nil {
		t.Fatalf("project/state: %v", err)
	}
	if len(state.Stale) != 0 {
		t.Fatalf("after the child's own settings write, stale = %v, want none", state.Stale)
	}

	// The same file changed by anyone else is a reason.
	writeRecordFile(t, userConfig, "[tools.shell]\nallow_network = true\n")
	if err := bridge.Call(ctx, acpwire.MethodProjectState, nil, &state); err != nil {
		t.Fatalf("second project/state: %v", err)
	}
	if len(state.Stale) != 1 || state.Stale[0] != "config.toml changed" {
		t.Fatalf("after a direct change, stale = %v, want [config.toml changed]", state.Stale)
	}
}
