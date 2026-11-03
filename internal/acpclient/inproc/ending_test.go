package inproc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/postmortem"
)

// fakeEndService is the runtime's end surface as the tests drive it: it reports
// a set membership and decision, records what it was given, and returns set
// errors.
type fakeEndService struct {
	mu          sync.Mutex
	work        acpwire.EndWork
	decision    acpwire.EndDecision
	waitErr     error
	analysisErr error
	params      []acpwire.EndAcceptParams
	goals       []string
	waits       int
}

func (f *fakeEndService) ActiveWork() acpwire.EndWork {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.work
}

func (f *fakeEndService) AcceptEnd(params acpwire.EndAcceptParams) (acpwire.EndDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.params = append(f.params, params)
	return f.decision, nil
}

func (f *fakeEndService) WaitForEnd(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	return f.waitErr
}

func (f *fakeEndService) RunExitAnalysis(ctx context.Context, goal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goals = append(f.goals, goal)
	return f.analysisErr
}

// accepts returns the acceptance parameters the service was given, in order.
func (f *fakeEndService) accepts() []acpwire.EndAcceptParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acpwire.EndAcceptParams(nil), f.params...)
}

// analysisGoals returns the goals RunExitAnalysis was given, in order.
func (f *fakeEndService) analysisGoals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.goals...)
}

// waitCount returns the number of WaitForEnd calls.
func (f *fakeEndService) waitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waits
}

// TestEndCallsForwardToTheEndService pins that the four forward calls reach the
// runtime unchanged: the work, the acceptance parameters, the wait and the exit
// analysis's goal.
func TestEndCallsForwardToTheEndService(t *testing.T) {
	ctx := context.Background()
	work := acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1", Label: "agent turn"}}}
	wantDecision := acpwire.EndDecision{Expanded: []string{"cmd-3"}, Work: work}
	fake := &fakeEndService{work: work, decision: wantDecision, waitErr: errors.New("join failed")}

	_, sess := New(Deps{State: newState(t, "s1"), End: fake})

	if got := sess.ActiveWork(); !reflect.DeepEqual(got, work) {
		t.Errorf("ActiveWork() = %+v, want the service's %+v", got, work)
	}

	params := acpwire.EndAcceptParams{
		Intent:           acpwire.EndIntentExit,
		DisplayedIDs:     []string{"a"},
		RequireDisplayed: true,
	}
	decision, err := sess.AcceptEnd(ctx, params)
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !reflect.DeepEqual(decision, wantDecision) {
		t.Errorf("AcceptEnd = %+v, want the service's %+v", decision, wantDecision)
	}
	if got := fake.accepts(); !reflect.DeepEqual(got, []acpwire.EndAcceptParams{params}) {
		t.Errorf("AcceptEnd params = %+v, want them unchanged", got)
	}

	if err := sess.WaitForEnd(ctx); err == nil || err.Error() != "join failed" {
		t.Errorf("WaitForEnd = %v, want the service's join failed", err)
	}
	if got := fake.waitCount(); got != 1 {
		t.Errorf("WaitForEnd calls = %d, want 1", got)
	}

	if err := sess.RunExitAnalysis(ctx, "goal"); err != nil {
		t.Fatalf("RunExitAnalysis: %v", err)
	}
	if got := fake.analysisGoals(); !reflect.DeepEqual(got, []string{"goal"}) {
		t.Errorf("RunExitAnalysis goals = %v, want [goal]", got)
	}
}

// TestASessionWithoutAnEndServiceEndsAtOnce pins the no-runtime fallback: no
// work to warn about, an acceptance, a settlement with nothing to join, and a
// refusal of the exit analysis.
func TestASessionWithoutAnEndServiceEndsAtOnce(t *testing.T) {
	ctx := context.Background()
	_, sess := New(Deps{State: newState(t, "s1")})

	if got := sess.ActiveWork(); len(got.Items) != 0 {
		t.Errorf("ActiveWork() = %+v, want no items without an end service", got)
	}

	decision, err := sess.AcceptEnd(ctx, acpwire.EndAcceptParams{Intent: acpwire.EndIntentExit})
	if err != nil {
		t.Fatalf("AcceptEnd without an end service: %v", err)
	}
	if !decision.Accepted {
		t.Error("AcceptEnd = not accepted, want accepted without an end service")
	}
	if len(decision.Work.Items) != 0 {
		t.Errorf("AcceptEnd work = %+v, want no items", decision.Work)
	}

	if err := sess.WaitForEnd(ctx); err != nil {
		t.Errorf("WaitForEnd without an end service = %v, want nil", err)
	}
	if err := sess.RunExitAnalysis(ctx, "goal"); closedCode(err) != acpwire.CodeNoRunner {
		t.Errorf("RunExitAnalysis without an end service = %v, want code %q", err, acpwire.CodeNoRunner)
	}
}

// TestPostmortemWritesTheReport pins that the call writes the report where the
// TUI's exit path does, and that the file decodes as a report.
func TestPostmortemWritesTheReport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	path, err := sess.Postmortem(context.Background())
	if err != nil {
		t.Fatalf("Postmortem: %v", err)
	}
	wantDir := postmortem.Dir(home, postmortem.ProjectSlug(state.WorkingDir))
	if got := filepath.Dir(path); got != wantDir {
		t.Errorf("report dir = %q, want %q", got, wantDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	var report postmortem.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode the report: %v", err)
	}
	if report.SchemaVersion != postmortem.SchemaVersion {
		t.Errorf("report schema version = %d, want %d", report.SchemaVersion, postmortem.SchemaVersion)
	}
	if report.Session.ID != "s1" {
		t.Errorf("report session ID = %q, want s1", report.Session.ID)
	}
}

// TestCloseShutsTheSessionDown pins what an end does: it denies the open
// approval, shuts the state down, closes the change signal, keeps answering
// reads with their final values, and is idempotent.
func TestCloseShutsTheSessionDown(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	responses := approvalChan()
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "rm -rf build",
		ResponseChan: responses,
	})

	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case d := <-responses:
		if d.Approved {
			t.Error("the pending approval was approved, want a denial")
		}
	default:
		t.Error("the pending approval was not settled by Close")
	}

	select {
	case <-state.Done():
	default:
		t.Error("the state is not shut down after Close")
	}

	select {
	case <-sess.Changed():
	default:
		t.Error("Changed is not closed after Close")
	}

	if got := sess.ID(); got != "s1" {
		t.Errorf("ID() = %q after Close, want its final value s1", got)
	}
	if err := sess.Stop(context.Background(), "stop"); closedCode(err) != acpwire.CodeSessionClosed {
		t.Errorf("Stop after Close = %v, want code %q", err, acpwire.CodeSessionClosed)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Errorf("the second Close = %v, want nil", err)
	}
}

// TestCloseCancelsTheRunInFlight pins that an end stops the turn: the run's
// context is cancelled, and the turn it was in reports cancelled, not failed.
func TestCloseCancelsTheRunInFlight(t *testing.T) {
	state := newState(t, "s1")
	fake := newFakeRunner()
	fake.setBody(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	_, sess := New(Deps{State: state, Runner: func() Runner { return fake }})

	type outcome struct {
		res acpwire.PromptResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := sess.Prompt(context.Background(), acpwire.PromptParams{Text: "a long turn"})
		done <- outcome{res: res, err: err}
	}()
	fake.waitStart(t)

	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("Prompt after Close = %v, want no error", got.err)
	}
	if got.res.StopReason != acpwire.StopReasonCancelled {
		t.Errorf("StopReason = %q, want %q", got.res.StopReason, acpwire.StopReasonCancelled)
	}
}

// TestNewSessionReplacesTheCurrentSession pins that the host asks the runtime
// for the session, hands it the name, and closes the one it replaces.
func TestNewSessionReplacesTheCurrentSession(t *testing.T) {
	var mu sync.Mutex
	var names []string
	host, old := New(Deps{
		State: newState(t, "s1"),
		NewSession: func(name string) (*session.State, error) {
			mu.Lock()
			names = append(names, name)
			mu.Unlock()
			return newState(t, "s2"), nil
		},
	})

	sess, err := host.NewSession(context.Background(), "fix-auth")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := sess.ID(); got != "s2" {
		t.Errorf("the new session ID() = %q, want s2", got)
	}
	mu.Lock()
	gotNames := append([]string(nil), names...)
	mu.Unlock()
	if !reflect.DeepEqual(gotNames, []string{"fix-auth"}) {
		t.Errorf("NewSession names = %v, want [fix-auth]", gotNames)
	}
	if err := old.Stop(context.Background(), "stop"); closedCode(err) != acpwire.CodeSessionClosed {
		t.Errorf("the replaced session's Stop = %v, want code %q", err, acpwire.CodeSessionClosed)
	}
}

// TestAFailedNewSessionKeepsTheCurrentOne pins that a failure leaves the
// current session as it was, and that a runtime with no factory refuses.
func TestAFailedNewSessionKeepsTheCurrentOne(t *testing.T) {
	host, old := New(Deps{
		State: newState(t, "s1"),
		NewSession: func(string) (*session.State, error) {
			return nil, errors.New("boom")
		},
	})

	sess, err := host.NewSession(context.Background(), "fix-auth")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("NewSession = (%v, %v), want the runtime's boom", sess, err)
	}
	if sess != nil {
		t.Errorf("NewSession returned a session with an error: %v", sess)
	}
	if err := old.Stop(context.Background(), "stop"); err != nil {
		t.Errorf("the current session's Stop = %v after a failed NewSession, want nil", err)
	}

	bare, _ := New(Deps{State: newState(t, "s2")})
	if _, err := bare.NewSession(context.Background(), "fix-auth"); !errors.Is(err, errNoNewSession) {
		t.Errorf("NewSession without a factory = %v, want errNoNewSession", err)
	}
}

// TestAClosedSessionEndsNothing pins the closed-session refusal for the six end
// calls: a session a swap replaced answers each with its final values, and the
// runtime's end service is never asked.
func TestAClosedSessionEndsNothing(t *testing.T) {
	ctx := context.Background()
	fake := &fakeEndService{
		work:     acpwire.EndWork{Items: []acpwire.EndWorkItem{{ID: "op-1"}}},
		decision: acpwire.EndDecision{Accepted: true},
	}
	host, old := New(Deps{State: newState(t, "s1"), End: fake})
	host.Swap(newState(t, "s2"))

	refused := func(name string, err error) {
		t.Helper()
		if code := closedCode(err); code != acpwire.CodeSessionClosed {
			t.Errorf("%s on a closed session = %v, want code %q", name, err, acpwire.CodeSessionClosed)
		}
	}

	_, err := old.AcceptEnd(ctx, acpwire.EndAcceptParams{Intent: acpwire.EndIntentExit})
	refused("AcceptEnd", err)
	refused("WaitForEnd", old.WaitForEnd(ctx))
	refused("RunExitAnalysis", old.RunExitAnalysis(ctx, "goal"))
	_, err = old.Postmortem(ctx)
	refused("Postmortem", err)

	if got := old.ActiveWork(); len(got.Items) != 0 {
		t.Errorf("ActiveWork on a closed session = %+v, want empty", got)
	}
	if got := fake.accepts(); len(got) != 0 {
		t.Errorf("the end service was asked to accept %+v on a closed session", got)
	}
	if got := fake.waitCount(); got != 0 {
		t.Errorf("the end service was asked to wait %d times on a closed session", got)
	}
	if got := fake.analysisGoals(); len(got) != 0 {
		t.Errorf("the end service was asked to analyse %v on a closed session", got)
	}
}
