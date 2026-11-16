package service

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// marshalAccessStores is the Studio policy as the agent side sees it through
// the seam: an in-test stand-in for the bridge, so the tests exercise the
// adapter's behaviour rather than the bridge's own store.
type marshalAccessStores struct {
	view     acpwire.MarshalAccessView
	selected string
}

func newMarshalAccessStores() *marshalAccessStores {
	return &marshalAccessStores{}
}

// accessDeps builds the three hooks over the store, the way a runtime would.
// The save hook reports every write as stale, which is what a write from an
// out-of-date editor gets; the selection hook records what it was told.
func (m *marshalAccessStores) accessDeps() (func() (acpwire.MarshalAccessView, error),
	func(acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error),
	func(acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error)) {
	read := func() (acpwire.MarshalAccessView, error) { return m.view, nil }
	save := func(acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error) {
		view := m.view
		view.Stale = true
		return view, nil
	}
	selectInstance := func(p acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error) {
		m.selected = p.ProjectID
		view := m.view
		view.SelectedProjectID = p.ProjectID
		return view, nil
	}
	return read, save, selectInstance
}

// TestMarshalStudioAccessViewIsTheSessionsOwn proves the adapter reports the
// view its host serves under the session's own identity, so a surface never has
// to reconcile the session it is talking to with the one a binding named.
func TestMarshalStudioAccessViewIsTheSessionsOwn(t *testing.T) {
	st := newMarshalAccessStores()
	st.view = acpwire.MarshalAccessView{
		State:      acpwire.MarshalAccessConnected,
		InstanceID: "instance-1",
		ProjectID:  "project-1",
		SessionID:  "another-session",
		Effective:  acpwire.MarshalAccess{Read: accessPtr(true), CreateDraft: accessPtr(false)},
		Source:     map[string]string{"project": "override"},
	}
	read, save, selectInstance := st.accessDeps()
	_, sess := New(Deps{State: newState(t, "s1"), MarshalAccess: read, SaveMarshalAccess: save, SelectMarshalInstance: selectInstance})

	view, err := sess.MarshalAccess(context.Background())
	if err != nil {
		t.Fatalf("MarshalAccess: %v", err)
	}
	if view.SessionID != "s1" {
		t.Fatalf("SessionID = %q, want the session's own", view.SessionID)
	}
	if view.State != acpwire.MarshalAccessConnected || view.InstanceID != "instance-1" || view.ProjectID != "project-1" {
		t.Fatalf("view = %+v", view)
	}
	if view.Effective.Read == nil || !*view.Effective.Read || view.Effective.CreateDraft == nil || *view.Effective.CreateDraft {
		t.Fatalf("effective = %+v", view.Effective)
	}
	if view.Source["project"] != "override" {
		t.Fatalf("source = %+v", view.Source)
	}
}

// TestMarshalStudioAccessLocalViewWithoutAHost proves the surface still answers
// when the session has no bridge: it reports the association its own enrollment
// recorded, or none, and never invents a policy.
func TestMarshalStudioAccessLocalViewWithoutAHost(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	view, err := sess.MarshalAccess(context.Background())
	if err != nil {
		t.Fatalf("MarshalAccess without a host: %v", err)
	}
	if view.State != acpwire.MarshalAccessUnavailable {
		t.Fatalf("state = %q, want unavailable", view.State)
	}
	if view.Reason == "" {
		t.Fatal("an unavailable state gave no reason")
	}
	// Nothing about the policy is reported, because this side cannot read it.
	if view.Effective.Read != nil || view.Default.Version != "" || view.Override != nil || view.Editable {
		t.Fatalf("a session with no bridge reported a policy: %+v", view)
	}

	// With a verified association the same surface reports the connection
	// identity, which is what the enrollment recorded.
	state.RecordMarshalSession(session.MarshalSession{InstanceID: "instance-1", Generation: "gen-1", ProjectID: "project-1"})
	view, err = sess.MarshalAccess(context.Background())
	if err != nil {
		t.Fatalf("MarshalAccess after an association: %v", err)
	}
	if view.State != acpwire.MarshalAccessConnected {
		t.Fatalf("state = %q, want connected", view.State)
	}
	if view.InstanceID != "instance-1" || view.Generation != "gen-1" || view.ProjectID != "project-1" {
		t.Fatalf("identity = %+v", view)
	}
	if view.Effective.Read != nil {
		t.Fatalf("the local view fabricated an access value: %+v", view.Effective)
	}
}

// TestMarshalStudioAccessWriteWithoutAHostFails proves a write is the one thing
// that fails without a host: a user asked for it, and saying nothing happened is
// the only honest answer.
func TestMarshalStudioAccessWriteWithoutAHostFails(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	_, err := sess.SaveMarshalAccess(context.Background(), acpwire.SaveMarshalAccessParams{
		Scope: acpwire.MarshalAccessScopeProject, Version: "v1",
	})
	var wireErr *acpwire.Error
	if !errors.As(err, &wireErr) || wireErr.Code != acpwire.CodeMarshalAccessUnavailable {
		t.Fatalf("SaveMarshalAccess err = %v, want %s", err, acpwire.CodeMarshalAccessUnavailable)
	}
	_, err = sess.SelectMarshalInstance(context.Background(), acpwire.SelectMarshalInstanceParams{Version: "v1"})
	if !errors.As(err, &wireErr) || wireErr.Code != acpwire.CodeMarshalAccessUnavailable {
		t.Fatalf("SelectMarshalInstance err = %v, want %s", err, acpwire.CodeMarshalAccessUnavailable)
	}
}

// TestMarshalStudioAccessSaveValidatesScope proves the level is checked before
// anything is written: a write that names no scope, or one this surface does not
// define, is refused rather than guessed.
func TestMarshalStudioAccessSaveValidatesScope(t *testing.T) {
	st := newMarshalAccessStores()
	read, save, selectInstance := st.accessDeps()
	_, sess := New(Deps{State: newState(t, "s1"), MarshalAccess: read, SaveMarshalAccess: save, SelectMarshalInstance: selectInstance})

	for _, scope := range []string{"", "workspace", "Project"} {
		if _, err := sess.SaveMarshalAccess(context.Background(), acpwire.SaveMarshalAccessParams{
			Scope: scope, Version: "v1",
		}); err == nil {
			t.Fatalf("scope %q was accepted", scope)
		}
	}
	// The session the write names is the caller's own, whatever a transport
	// payload carried.
	view, err := sess.SaveMarshalAccess(context.Background(), acpwire.SaveMarshalAccessParams{
		SessionID: "elsewhere", Scope: acpwire.MarshalAccessScopeProject, Version: "v1",
	})
	if err != nil {
		t.Fatalf("SaveMarshalAccess: %v", err)
	}
	if view.SessionID != "s1" || !view.Stale {
		t.Fatalf("view = %+v, want the session's own and the stale answer its host gave", view)
	}
}

// TestMarshalStudioIntegrationAccessInstanceSelection proves the selection call
// reaches the host with the session it belongs to, so an ambiguous session can
// be resolved and cleared from this surface.
func TestMarshalStudioIntegrationAccessInstanceSelection(t *testing.T) {
	st := newMarshalAccessStores()
	st.view = acpwire.MarshalAccessView{State: acpwire.MarshalAccessAmbiguous, Candidates: []string{"p1", "p2"}}
	read, save, selectInstance := st.accessDeps()
	_, sess := New(Deps{State: newState(t, "s1"), MarshalAccess: read, SaveMarshalAccess: save, SelectMarshalInstance: selectInstance})

	view, err := sess.SelectMarshalInstance(context.Background(), acpwire.SelectMarshalInstanceParams{
		SessionID: "elsewhere", ProjectID: "p2", Version: "sel-1",
	})
	if err != nil {
		t.Fatalf("SelectMarshalInstance: %v", err)
	}
	if st.selected != "p2" || view.SelectedProjectID != "p2" {
		t.Fatalf("selection = %q, view = %+v", st.selected, view)
	}
	if view.SessionID != "s1" {
		t.Fatalf("the selection answered for %q, want the caller's session", view.SessionID)
	}

	// Clearing the choice is the reset: the host is told no project, and the
	// surface reports the state the host gives back.
	cleared, err := sess.SelectMarshalInstance(context.Background(), acpwire.SelectMarshalInstanceParams{
		Version: "sel-2",
	})
	if err != nil {
		t.Fatalf("clearing the selection: %v", err)
	}
	if st.selected != "" || cleared.SelectedProjectID != "" {
		t.Fatalf("the choice was not cleared: %q %+v", st.selected, cleared)
	}
}

// TestMarshalStudioAccessClosedSessionRefuses proves a replaced session answers
// nothing: its policy could belong to a state that no longer exists.
func TestMarshalStudioAccessClosedSessionRefuses(t *testing.T) {
	st := newMarshalAccessStores()
	read, save, selectInstance := st.accessDeps()
	_, sess := New(Deps{State: newState(t, "s1"), MarshalAccess: read, SaveMarshalAccess: save, SelectMarshalInstance: selectInstance})
	sess.close()

	if _, err := sess.MarshalAccess(context.Background()); !errors.Is(err, errSessionClosed) {
		t.Fatalf("MarshalAccess on a closed session: %v", err)
	}
	if _, err := sess.SaveMarshalAccess(context.Background(), acpwire.SaveMarshalAccessParams{
		Scope: acpwire.MarshalAccessScopeSession, Version: "v1",
	}); !errors.Is(err, errSessionClosed) {
		t.Fatalf("SaveMarshalAccess on a closed session: %v", err)
	}
	if _, err := sess.SelectMarshalInstance(context.Background(), acpwire.SelectMarshalInstanceParams{
		Version: "v1",
	}); !errors.Is(err, errSessionClosed) {
		t.Fatalf("SelectMarshalInstance on a closed session: %v", err)
	}
}

// accessPtr names one access value in a fixture.
func accessPtr(v bool) *bool { return &v }
