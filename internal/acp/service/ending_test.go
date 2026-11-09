package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
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
	host.swap(newState(t, "s2"))

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
