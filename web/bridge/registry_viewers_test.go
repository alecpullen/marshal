package bridge

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// viewersRegistry wires a Registry onto an in-memory fake agent, so a test
// can assert both the frames the registry sent and the state it kept.
func viewersRegistry(t *testing.T) (*Registry, *fakeAgent) {
	t.Helper()
	fa := newFakeAgent()
	c := &Child{Transport: fa}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Stop)
	reg := NewRegistry(c)
	t.Cleanup(func() {
		reg.mu.Lock()
		for _, info := range reg.sessions {
			if info.cancel != nil {
				info.cancel()
			}
		}
		reg.mu.Unlock()
	})
	return reg, fa
}

// paramsOf decodes one recorded frame's params.
func paramsOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode params %q: %v", raw, err)
	}
	return out
}

func TestResumeTracksTheSession(t *testing.T) {
	reg, fa := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Resume(ctx, AgentPath("/home/u/repo"), "stored-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	calls := fa.calls("session/resume")
	if len(calls) != 1 {
		t.Fatalf("session/resume sent %d times, want 1: %v", len(calls), calls)
	}
	params := paramsOf(t, calls[0])
	if got := params["sessionId"]; got != "stored-1" {
		t.Errorf("sessionId = %v, want stored-1", got)
	}
	if got := params["cwd"]; got != "/home/u/repo" {
		t.Errorf("cwd = %v, want /home/u/repo", got)
	}
	info, ok := reg.lookup("stored-1")
	if !ok {
		t.Fatal("Resume did not track the session")
	}
	if info.Cwd != AgentPath("/home/u/repo") {
		t.Errorf("tracked cwd = %q", info.Cwd)
	}

	// A resume the agent refuses tracks nothing.
	failing := newFakeAgent()
	failing.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
		if method == "session/resume" {
			return nil, &rpcError{Code: -32000, Message: "no such session"}, true
		}
		return nil, nil, false
	}
	c := &Child{Transport: failing}
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(c.Stop)
	refusing := NewRegistry(c)
	if err := refusing.Resume(ctx, AgentPath("/home/u/repo"), "missing"); err == nil {
		t.Fatal("Resume of a refused session: expected an error, got nil")
	}
	if _, ok := refusing.lookup("missing"); ok {
		t.Fatal("a failed Resume tracked the session")
	}
}

func TestCloseClosesTheRuntimeAndUntracks(t *testing.T) {
	reg, fa := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Resume(ctx, AgentPath("/home/u/repo"), "s-close"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Park a permission request on the session: Close must drain it.
	waiter := make(chan Decision, 1)
	reg.permMu.Lock()
	reg.permissions["tc1"] = waiter
	reg.permSession["tc1"] = "s-close"
	reg.permMu.Unlock()

	if err := reg.Close(ctx, "s-close"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case d := <-waiter:
		if d.Approved {
			t.Error("drained permission was approved, want a denial")
		}
	default:
		t.Error("Close left the pending permission waiter blocked")
	}
	if _, ok := reg.lookup("s-close"); ok {
		t.Error("Close left the session tracked")
	}
	calls := fa.calls("session/close")
	if len(calls) != 1 {
		t.Fatalf("session/close sent %d times, want 1: %v", len(calls), calls)
	}
	if got := paramsOf(t, calls[0])["sessionId"]; got != "s-close" {
		t.Errorf("session/close sessionId = %v, want s-close", got)
	}
	if err := reg.Close(ctx, "s-close"); !errors.Is(err, ErrUnknownSession) {
		t.Errorf("Close of an untracked session = %v, want ErrUnknownSession", err)
	}
}

func TestRenameKeepsTheSessionsState(t *testing.T) {
	reg, _ := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Resume(ctx, AgentPath("/home/u/one"), "old"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	reg.attachViewer("old")
	reg.attachViewer("old")

	if !reg.rename("old", "new") {
		t.Fatal("rename of a tracked session reported false")
	}
	info, ok := reg.lookup("new")
	if !ok {
		t.Fatal("rename left no session under the new ID")
	}
	if info.Cwd != AgentPath("/home/u/one") {
		t.Errorf("renamed session cwd = %q, want /home/u/one", info.Cwd)
	}
	if info.viewers != 2 {
		t.Errorf("renamed session viewers = %d, want 2", info.viewers)
	}
	if _, ok := reg.lookup("old"); ok {
		t.Error("rename left the old ID tracked")
	}

	if reg.rename("missing", "x") {
		t.Error("rename of an unknown ID reported true")
	}
	if err := reg.Resume(ctx, AgentPath("/home/u/two"), "taken"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if reg.rename("new", "taken") {
		t.Error("rename onto a tracked ID reported true")
	}
	if _, ok := reg.lookup("new"); !ok {
		t.Error("a refused rename moved the session anyway")
	}
}

func TestSummariesAreMostRecentFirst(t *testing.T) {
	reg, _ := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	for _, id := range []string{"s1", "s2", "s3"} {
		if err := reg.Resume(ctx, AgentPath("/home/u/repo"), id); err != nil {
			t.Fatalf("Resume %s: %v", id, err)
		}
	}
	reg.attachViewer("s2")
	reg.attachViewer("s2")

	// Stamp the activity explicitly: three touches in the same millisecond
	// would only prove the ID tie-break, not the ordering. The stamps sit in
	// the past so the later touch below is unambiguously the most recent.
	base := time.Now().Add(-time.Hour)
	setActive := func(id string, at time.Time) {
		reg.mu.Lock()
		reg.sessions[id].lastActive = at
		reg.mu.Unlock()
	}
	setActive("s1", base)
	setActive("s3", base.Add(2*time.Second))
	setActive("s2", base.Add(time.Second)) // middle activity, two viewers

	got := reg.summaries()
	if len(got) != 3 {
		t.Fatalf("summaries listed %d sessions, want 3", len(got))
	}
	wantOrder := []string{"s3", "s2", "s1"}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("summary %d = %q, want %q (order %v)", i, got[i].ID, want, summaryIDs(got))
		}
	}
	if got[1].Viewers != 2 {
		t.Errorf("s2 viewers = %d, want 2", got[1].Viewers)
	}
	if want := base.Add(2 * time.Second).UnixMilli(); got[0].LastActive != want {
		t.Errorf("s3 lastActive = %d, want %d", got[0].LastActive, want)
	}
	if n := reg.count(); n != 3 {
		t.Errorf("count = %d, want 3", n)
	}

	// touch moves a session to the front.
	reg.touch("s1")
	if got := reg.summaries(); got[0].ID != "s1" {
		t.Errorf("after touch, first summary = %q, want s1", got[0].ID)
	}
}

func summaryIDs(list []DaemonSession) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

// Two attaches resolving one stored id can both reach track; the second must
// not replace the live record, or the first viewer is stranded on a session
// with no viewer and the record's cancel context is leaked.
func TestTrackingATrackedSessionKeepsItsViewers(t *testing.T) {
	reg, _ := viewersRegistry(t)
	ctx, cancel := testContext(t)
	defer cancel()

	if err := reg.Resume(ctx, AgentPath("/home/u/repo"), "s1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	reg.attachViewer("s1")
	if err := reg.Resume(ctx, AgentPath("/home/u/repo"), "s1"); err != nil {
		t.Fatalf("Resume again: %v", err)
	}

	got := reg.summaries()
	if len(got) != 1 {
		t.Fatalf("summaries listed %d sessions, want 1", len(got))
	}
	if got[0].Viewers != 1 {
		t.Errorf("viewers = %d after a second resolve, want 1", got[0].Viewers)
	}
}
