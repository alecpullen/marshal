package settings

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
)

// studioSession is the client seam as the Studio section uses it: the three
// access calls are the only ones the section makes, so the rest of the
// interface is embedded unpublished rather than faked.
type studioSession struct {
	acpclient.Session
	view    acpwire.MarshalAccessView
	err     error
	saves   []acpwire.SaveMarshalAccessParams
	selects []acpwire.SelectMarshalInstanceParams
}

func (s *studioSession) MarshalAccess(context.Context) (acpwire.MarshalAccessView, error) {
	return s.view, s.err
}

func (s *studioSession) SaveMarshalAccess(_ context.Context, p acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error) {
	s.saves = append(s.saves, p)
	return s.view, s.err
}

func (s *studioSession) SelectMarshalInstance(_ context.Context, p acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error) {
	s.selects = append(s.selects, p)
	return s.view, s.err
}

// allow names one access field's value in a fixture.
func allow(v bool) *bool { return &v }

func authority(a string) *string { return &a }

// studioRows indexes one frame's rows by id.
func studioRows(t *testing.T, f *frame) map[string]*field {
	t.Helper()
	ps := newPaneStack(f)
	ps.SetSize(100, 40)
	rows := make(map[string]*field)
	for _, row := range ps.Top().List.Rows() {
		rows[row.ID] = row
	}
	return rows
}

// loadedStudioState is a state that has read one access view: the starting
// point of every frame test below.
//
// The view is reported editable, which is what the server says for a tracked
// session. The one view it reports read-only — an untracked session — has its
// own test, so the rows here are the editable ones.
func loadedStudioState(view acpwire.MarshalAccessView) *state {
	s := newState(config.Default())
	view.Editable = true
	s.access = view
	s.accessLoaded = true
	return s
}

// TestMarshalStudioAccessFrameShowsInheritanceAndSources proves the section
// reports what each level stores and what the resolution reaches: an unset
// field reads as inheritance, an explicit denial reads as a denial, and the
// badge beside it is the effective value.
func TestMarshalStudioAccessFrameShowsInheritanceAndSources(t *testing.T) {
	s := loadedStudioState(acpwire.MarshalAccessView{
		State:      acpwire.MarshalAccessConnected,
		InstanceID: "/run/user/501/marshal/daemon.sock",
		Generation: "4242:1700000000000",
		ProjectID:  "project-1",
		SessionID:  "s1",
		Default: acpwire.MarshalAccessLayer{
			Access:  acpwire.MarshalAccess{Read: allow(true), CreateDraft: allow(true)},
			Version: "default-1",
		},
		Override: &acpwire.MarshalAccessLayer{
			Access:  acpwire.MarshalAccess{EditDraft: allow(false)},
			Version: "session-1",
		},
		OverrideVersion: "session-1",
		Effective: acpwire.MarshalAccess{
			Read: allow(true), CreateDraft: allow(true), EditDraft: allow(false),
			ApprovalAuthority: authority("none"),
		},
		Source: map[string]string{"project": "override", "session": "override"},
	})

	root := studioRows(t, studioFrame(s))
	if got := root["studio.connection"].GetStr(); got != acpwire.MarshalAccessConnected {
		t.Fatalf("connection row = %q", got)
	}
	if got := root["studio.instance"].GetStr(); !strings.Contains(got, "4242:1700000000000") {
		t.Fatalf("instance row = %q", got)
	}
	if got := root["studio.project"].GetStr(); got != "project-1" {
		t.Fatalf("project row = %q", got)
	}
	if got := root["studio.defaults"].Summary(); got != "read, create drafts" {
		t.Fatalf("defaults summary = %q", got)
	}
	if got := root["studio.override"].Summary(); got != "nothing granted" {
		t.Fatalf("override summary = %q", got)
	}

	// The project level stores create-draft and says so; the session level
	// leaves it alone, so the row reads inheritance while its badge shows the
	// effective allow it inherits.
	project := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))
	if got := project["studio.project.create_draft"].GetStr(); got != "allow" {
		t.Fatalf("project create_draft = %q", got)
	}
	if value, badge := project["studio.project.edit_draft"].Display(); value != studioInherit || badge != "→ deny" {
		t.Fatalf("project edit_draft = %q %q", value, badge)
	}
	session := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeSession))
	if got := session["studio.session.edit_draft"].GetStr(); got != "deny" {
		t.Fatalf("session edit_draft = %q", got)
	}
	if value, badge := session["studio.session.read"].Display(); value != studioInherit || badge != "→ allow" {
		t.Fatalf("session read = %q %q", value, badge)
	}
	if got := session["studio.session.approval_authority"].GetStr(); got != studioInherit {
		t.Fatalf("session authority = %q", got)
	}
}

// TestMarshalStudioAccessFrameWritesTheWholeLayer proves a row writes the level
// it edits — every field the level stores, not only the one that changed — and
// names the version that level was read at.
func TestMarshalStudioAccessFrameWritesTheWholeLayer(t *testing.T) {
	s := loadedStudioState(acpwire.MarshalAccessView{
		State:           acpwire.MarshalAccessConnected,
		ProjectID:       "project-1",
		SessionID:       "s1",
		Default:         acpwire.MarshalAccessLayer{Access: acpwire.MarshalAccess{Read: allow(true), CreateDraft: allow(true)}, Version: "default-1"},
		OverrideVersion: "session-1",
	})
	sess := &studioSession{view: s.access}
	s.sess = sess

	project := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))
	if err := project["studio.project.create_draft"].SetStr("deny"); err != nil {
		t.Fatalf("set create_draft: %v", err)
	}
	cmd := s.takePendingCmd()
	if cmd == nil {
		t.Fatal("a project write queued no command")
	}
	cmd()
	if len(sess.saves) != 1 {
		t.Fatalf("saves = %d, want 1", len(sess.saves))
	}
	saved := sess.saves[0]
	if saved.Scope != acpwire.MarshalAccessScopeProject || saved.Version != "default-1" {
		t.Fatalf("saved = %+v", saved)
	}
	if saved.Access.CreateDraft == nil || *saved.Access.CreateDraft {
		t.Fatalf("the write did not carry the new denial: %+v", saved.Access)
	}
	if saved.Access.Read == nil || !*saved.Access.Read {
		t.Fatalf("the write dropped a field the level stores: %+v", saved.Access)
	}

	// A reset writes the field as unset, which is what restores inheritance.
	session := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeSession))
	if err := session["studio.session.edit_draft"].SetStr(studioInherit); err != nil {
		t.Fatalf("set edit_draft to inherit: %v", err)
	}
	if cmd := s.takePendingCmd(); cmd == nil {
		t.Fatal("a session write queued no command")
	} else {
		cmd()
	}
	if len(sess.saves) != 2 {
		t.Fatalf("saves = %d, want 2", len(sess.saves))
	}
	if got := sess.saves[1]; got.Scope != acpwire.MarshalAccessScopeSession || got.Version != "session-1" || got.Access.EditDraft != nil {
		t.Fatalf("the reset write = %+v", got)
	}
}

// TestMarshalStudioAccessFrameRefusesWriteBeforeRead proves a row cannot write
// before the surface has read the level: a write must name the version it was
// edited from, and guessing one would overwrite a value nobody saw.
func TestMarshalStudioAccessFrameRefusesWriteBeforeRead(t *testing.T) {
	s := newState(config.Default())
	s.sess = &studioSession{}

	root := studioRows(t, studioFrame(s))
	// Before the first read the section claims no connection state: it says the
	// connection has not been read, and names the row that reads it.
	if got := root["studio.connection"].GetStr(); !strings.Contains(strings.ToLower(got), "read") ||
		got == acpwire.MarshalAccessConnected || got == acpwire.MarshalAccessUnavailable {
		t.Fatalf("connection before the first read = %q", got)
	}
	project := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))
	if err := project["studio.project.read"].SetStr("allow"); err == nil {
		t.Fatal("a write before the first read was accepted")
	}
	if cmd := s.takePendingCmd(); cmd != nil {
		t.Fatal("a refused write queued a command")
	}
	// Reading is how the surface recovers: the section always offers it.
	if _, ok := root["studio.refresh"]; !ok {
		t.Fatal("the section offers no way to read the connection")
	}
}

// TestMarshalStudioAccessFrameOfflineStatesStillEdit proves a state that is not
// a connection is shown as one and blocks nothing: the policy rows stay
// writable, because the policy is a stored setting and not the connection.
func TestMarshalStudioAccessFrameOfflineStatesStillEdit(t *testing.T) {
	for _, state := range []string{
		acpwire.MarshalAccessAmbiguous,
		acpwire.MarshalAccessUnavailable,
		acpwire.MarshalAccessUnsupported,
		acpwire.MarshalAccessUnauthorized,
	} {
		s := loadedStudioState(acpwire.MarshalAccessView{
			State:           state,
			Reason:          "the server said so",
			Editable:        true,
			Default:         acpwire.MarshalAccessLayer{Version: "default-1"},
			OverrideVersion: "session-1",
		})
		sess := &studioSession{view: s.access}
		s.sess = sess

		root := studioRows(t, studioFrame(s))
		if got := root["studio.connection"].GetStr(); !strings.Contains(got, state) || !strings.Contains(got, "the server said so") {
			t.Fatalf("%s: connection row = %q", state, got)
		}
		project := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))
		if err := project["studio.project.read"].SetStr("allow"); err != nil {
			t.Fatalf("%s: an offline state refused a policy write: %v", state, err)
		}
		cmd := s.takePendingCmd()
		if cmd == nil {
			t.Fatalf("%s: no write was queued", state)
		}
		cmd()
		if len(sess.saves) != 1 {
			t.Fatalf("%s: saves = %d", state, len(sess.saves))
		}
	}
}

// TestMarshalStudioIntegrationAccessAmbiguousInstanceSelection proves the
// section offers the candidates an ambiguous session reported and records the
// choice against the selection version it read.
func TestMarshalStudioIntegrationAccessAmbiguousInstanceSelection(t *testing.T) {
	s := loadedStudioState(acpwire.MarshalAccessView{
		State:            acpwire.MarshalAccessAmbiguous,
		SessionID:        "s1",
		Candidates:       []string{"project-1", "project-2"},
		SelectionVersion: "selection-1",
	})
	sess := &studioSession{view: s.access}
	s.sess = sess

	root := studioRows(t, studioFrame(s))
	if got := root["studio.candidates"].GetStr(); got != "project-1, project-2" {
		t.Fatalf("candidates row = %q", got)
	}
	options := root["studio.instance.select"].Options()
	if strings.Join(options, ",") != ",project-1,project-2" {
		t.Fatalf("options = %v", options)
	}
	if err := root["studio.instance.select"].SetStr("project-2"); err != nil {
		t.Fatalf("selecting a candidate: %v", err)
	}
	cmd := s.takePendingCmd()
	if cmd == nil {
		t.Fatal("the selection queued no command")
	}
	cmd()
	if len(sess.selects) != 1 {
		t.Fatalf("selections = %d, want 1", len(sess.selects))
	}
	if got := sess.selects[0]; got.ProjectID != "project-2" || got.Version != "selection-1" {
		t.Fatalf("selection = %+v", got)
	}
}

// TestMarshalStudioAccessFrameStaleSaveShowsTheWinner proves a refused stale
// write is answered, not failed: the row says the value changed elsewhere and
// the section shows the value that won.
func TestMarshalStudioAccessFrameStaleSaveShowsTheWinner(t *testing.T) {
	b := &BrowserPanel{reg: BuildRegistry(config.Default())}
	winner := acpwire.MarshalAccessView{
		State:   acpwire.MarshalAccessConnected,
		Default: acpwire.MarshalAccessLayer{Access: acpwire.MarshalAccess{CreateDraft: allow(true)}, Version: "default-2"},
		Stale:   true,
	}
	b.handleMarshalAccess(marshalAccessMsg{view: winner, fieldID: "studio.project.create_draft"})

	if !b.reg.st.accessLoaded || b.reg.st.access.Default.Version != "default-2" {
		t.Fatalf("the winner was not stored: %+v", b.reg.st.access)
	}
	if got := b.reg.st.actionState["studio.project.create_draft"].label; !strings.Contains(got, "changed elsewhere") {
		t.Fatalf("the row's receipt = %q", got)
	}
	// The row still shows the value the store holds, so a second edit is made
	// against the version that won rather than the one that was lost.
	if got := b.reg.st.studioLayerVersion(acpwire.MarshalAccessScopeProject); got != "default-2" {
		t.Fatalf("the next write would name %q", got)
	}

	// A failed read is a reason, not a silent gap.
	b.handleMarshalAccess(marshalAccessMsg{err: errAccessNotRead})
	if !strings.Contains(b.reg.st.studioConnection(), errAccessNotRead.Error()) {
		t.Fatalf("a failed read left the connection row as %q", b.reg.st.studioConnection())
	}
}

// TestMarshalStudioAccessFrameOwnsItsMessages proves the browser claims the
// Studio answers so the dock delivers them, and claims nothing else.
func TestMarshalStudioAccessFrameOwnsItsMessages(t *testing.T) {
	b := &BrowserPanel{reg: BuildRegistry(config.Default())}
	if !b.OwnsMsg(marshalAccessMsg{}) {
		t.Fatal("the browser does not claim its own Studio answers")
	}
	if b.OwnsMsg(tea.KeyPressMsg{}) {
		t.Fatal("the browser claimed a keypress; the dock routes those already")
	}
	if b.OwnsMsg(struct{}{}) {
		t.Fatal("the browser claimed an unrelated message")
	}
}

// TestMarshalStudioAccessSectionIsRegistered proves the section is reachable in
// the settings browser at all.
func TestMarshalStudioAccessSectionIsRegistered(t *testing.T) {
	for _, spec := range sectionList() {
		if spec.ID != "studio" {
			continue
		}
		if spec.Title != "Studio" {
			t.Fatalf("title = %q", spec.Title)
		}
		return
	}
	t.Fatal("no Studio section in the settings browser")
}

// TestMarshalStudioAccessFrameReadOnlyRefusesWrites proves a view the server
// reports as read-only keeps its rows visible and refuses every write locally.
// The server says read-only for exactly one session — one it does not track, so
// no write could name it — and a row that offered the edit anyway would let the
// person type a change the agent side must then refuse.
func TestMarshalStudioAccessFrameReadOnlyRefusesWrites(t *testing.T) {
	s := loadedStudioState(acpwire.MarshalAccessView{
		State:           acpwire.MarshalAccessUnavailable,
		ProjectID:       "project-1",
		SessionID:       "s1",
		Default:         acpwire.MarshalAccessLayer{Version: "default-1"},
		OverrideVersion: "session-1",
	})
	s.access.Editable = false
	sess := &studioSession{view: s.access}
	s.sess = sess

	project := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))
	// The value is still reported: read-only is a refusal to write, not a
	// refusal to say what is stored.
	if got := project["studio.project.read"].GetStr(); got != studioInherit {
		t.Fatalf("a read-only layer lost its value: %q", got)
	}
	if err := project["studio.project.read"].SetStr("allow"); !errors.Is(err, errStudioNotEditable) {
		t.Fatalf("a read-only capability row accepted a write: %v", err)
	}
	if err := project["studio.project.approval_authority"].SetStr("standing"); !errors.Is(err, errStudioNotEditable) {
		t.Fatalf("a read-only authority row accepted a write: %v", err)
	}

	// The reset asks twice, as every destructive row does: the first press arms
	// it and the second is the one that would write.
	reset := studioRows(t, studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject))[studioAccessRowID(acpwire.MarshalAccessScopeProject)]
	reset.Act()
	reset.Act()
	if s.takePendingCmd() != nil {
		t.Fatal("a read-only layer queued a reset write")
	}
	if len(sess.saves) != 0 {
		t.Fatalf("a read-only layer wrote %d times", len(sess.saves))
	}
}
