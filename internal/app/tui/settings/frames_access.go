package settings

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
)

// The Studio section: the connection this session's enrollment reached, the
// identity it holds, and the built-in access it inherits or overrides.
//
// Every value comes through the client seam (state.sess): the TUI reads no
// bridge state, no policy store and no session runtime, so this section is a
// settings surface like the rest. The agent side owns both the policy and its
// versions, which is why each write names the version it was read from and a
// refusal comes back as the value that won.
//
// States are shown, not thrown. A session that is offline, ambiguous or
// unauthorized keeps the whole settings UI usable and its own agent work
// running: the section says what it knows, and the rows that cannot act say so
// as plainly as the rows that can.

// studioInherit is the value a row shows and writes when a field takes the
// layer beneath it. Writing it is a field reset, which restores inheritance
// instead of storing a denial.
const studioInherit = "inherit"

// studioAccessRowID is the id of the row a Studio action reports its receipt
// under.
func studioAccessRowID(scope string) string { return "studio." + scope + ".reset" }

// studioFrame is the section's root frame.
func studioFrame(s *state) *frame {
	return newFrame("Studio", func() []*field {
		return []*field{
			{
				ID:     "studio.connection",
				Title:  "Connection",
				Kind:   kindScalar,
				Desc:   "the Studio instance this session enrolled on; r re-reads it",
				GetStr: func() string { return s.studioConnection() },
			},
			{
				ID: "studio.instance", Title: "Instance", Kind: kindScalar,
				Desc:   "the bridge instance and generation the enrollment bound",
				GetStr: func() string { return s.studioInstance() },
			},
			{
				ID: "studio.project", Title: "Project", Kind: kindScalar,
				Desc:   "the project this session acts on, as the server resolved it",
				GetStr: func() string { return studioOrDash(s.access.ProjectID) },
			},
			{
				ID: "studio.session", Title: "Session", Kind: kindScalar,
				Desc: "this session, and the worker it acts as when it is one",
				GetStr: func() string {
					out := studioOrDash(s.access.SessionID)
					if s.access.WorkerID != "" {
						out += " · worker " + s.access.WorkerID
					}
					return out
				},
			},
			{
				ID: "studio.candidates", Title: "Candidates", Kind: kindScalar,
				Desc:   "the projects the server tracks this session for; more than one is ambiguous",
				GetStr: func() string { return studioCandidates(s.access.Candidates) },
			},
			{
				ID: "studio.instance.select", Title: "Ambiguous instance", Kind: kindEnum,
				Desc:   "pick the project this session acts on when the server reported more than one",
				GetStr: func() string { return s.access.SelectedProjectID },
				Options: func() []string {
					options := append([]string{""}, s.access.Candidates...)
					return options
				},
				SetStr: func(v string) error {
					return s.studioSelectInstance(v)
				},
			},
			{
				ID: "studio.defaults", Title: "Project defaults", Kind: kindDrill,
				Desc:    "the access every session of this project inherits",
				Summary: func() string { return studioGrantSummary(s.access.Default.Access) },
				Build:   func() *frame { return studioAccessLayerFrame(s, acpwire.MarshalAccessScopeProject) },
			},
			{
				ID: "studio.override", Title: "This session", Kind: kindDrill,
				Desc: "this session's own override; anything it leaves alone is inherited",
				Summary: func() string {
					if s.access.Override == nil {
						return studioInherit
					}
					return studioGrantSummary(s.access.Override.Access)
				},
				Build: func() *frame { return studioAccessLayerFrame(s, acpwire.MarshalAccessScopeSession) },
			},
			{
				ID: "studio.refresh", Title: "Re-read connection", Kind: kindAction,
				Desc: "ask the agent side for the connection and the stored access again",
				ActLabel: func() string {
					if s.accessErr != "" {
						return "retry"
					}
					return "re-read"
				},
				Act: func() tea.Cmd { return s.studioReadCmd() },
			},
		}
	})
}

// studioAccessLayerFrame is one level's editor: a row per capability, the
// approval authority, and the reset that restores inheritance.
func studioAccessLayerFrame(s *state, scope string) *frame {
	title := "Studio · project defaults"
	if scope == acpwire.MarshalAccessScopeSession {
		title = "Studio · this session"
	}
	return newFrame(title, func() []*field {
		rows := make([]*field, 0, len(studioCapabilities)+2)
		for _, capability := range studioCapabilities {
			rows = append(rows, studioCapabilityField(s, scope, capability))
		}
		rows = append(rows, studioAuthorityField(s, scope))
		rows = append(rows, studioResetField(s, scope))
		return rows
	})
}

// studioCapability is one capability row: how to read it from a layer and how
// to write it back.
type studioCapability struct {
	id    string
	title string
	desc  string
	get   func(acpwire.MarshalAccess) *bool
	set   func(*acpwire.MarshalAccess, *bool)
}

// studioCapabilities is every capability an access layer carries. Write and
// approval are separate rows on purpose: reading the project's factory and
// changing it are different authorities, and approving an operation is a third.
var studioCapabilities = []studioCapability{
	{
		id: "read", title: "Read", desc: "read the project's factory and its items",
		get: func(a acpwire.MarshalAccess) *bool { return a.Read },
		set: func(a *acpwire.MarshalAccess, v *bool) { a.Read = v },
	},
	{
		id: "create_draft", title: "Create drafts", desc: "propose a draft for the project",
		get: func(a acpwire.MarshalAccess) *bool { return a.CreateDraft },
		set: func(a *acpwire.MarshalAccess, v *bool) { a.CreateDraft = v },
	},
	{
		id: "edit_draft", title: "Edit drafts", desc: "change a draft that already exists",
		get: func(a acpwire.MarshalAccess) *bool { return a.EditDraft },
		set: func(a *acpwire.MarshalAccess, v *bool) { a.EditDraft = v },
	},
	{
		id: "answer_question", title: "Answer questions", desc: "answer a question a draft waits on",
		get: func(a acpwire.MarshalAccess) *bool { return a.AnswerQuestion },
		set: func(a *acpwire.MarshalAccess, v *bool) { a.AnswerQuestion = v },
	},
	{
		id: "approve_and_queue", title: "Approve and queue", desc: "approve a draft and put it on the queue",
		get: func(a acpwire.MarshalAccess) *bool { return a.ApproveAndQueue },
		set: func(a *acpwire.MarshalAccess, v *bool) { a.ApproveAndQueue = v },
	},
}

// studioCapabilityField is one capability's row at one level. The value cell is
// what the level stores; the badge is the effective value the resolution
// reaches, so a row never hides what a session actually holds.
func studioCapabilityField(s *state, scope string, capability studioCapability) *field {
	id := "studio." + scope + "." + capability.id
	return &field{
		ID:      id,
		Title:   capability.title,
		Kind:    kindEnum,
		Desc:    capability.desc,
		Options: func() []string { return []string{studioInherit, "allow", "deny"} },
		GetStr:  func() string { return studioTriState(capability.get(s.studioLayer(scope))) },
		Display: func() (string, string) {
			return studioTriState(capability.get(s.studioLayer(scope))),
				"→ " + studioTriState(capability.get(s.access.Effective))
		},
		SetStr: func(v string) error {
			next, ok := s.studioLayerFor(scope)
			if !ok {
				return fmt.Errorf("read the connection first")
			}
			switch v {
			case studioInherit:
				capability.set(&next, nil)
			case "allow", "deny":
				allow := v == "allow"
				capability.set(&next, &allow)
			default:
				return fmt.Errorf("unknown value %q", v)
			}
			s.queueCmd(s.studioSaveCmd(scope, next, id))
			return nil
		},
	}
}

// studioAuthorityField is the approval authority row: the choice of who may
// approve, which is what makes an approval row meaningful.
func studioAuthorityField(s *state, scope string) *field {
	id := "studio." + scope + ".approval_authority"
	return &field{
		ID:      id,
		Title:   "Approval authority",
		Kind:    kindEnum,
		Desc:    "none, user_directed (each approval named by a person) or standing (a policy set in advance)",
		Options: func() []string { return []string{studioInherit, "none", "user_directed", "standing"} },
		GetStr: func() string {
			if a := s.studioLayer(scope).ApprovalAuthority; a != nil {
				return *a
			}
			return studioInherit
		},
		Display: func() (string, string) {
			value := studioInherit
			if a := s.studioLayer(scope).ApprovalAuthority; a != nil {
				value = *a
			}
			effective := studioInherit
			if a := s.access.Effective.ApprovalAuthority; a != nil {
				effective = *a
			}
			return value, "→ " + effective
		},
		SetStr: func(v string) error {
			next, ok := s.studioLayerFor(scope)
			if !ok {
				return fmt.Errorf("read the connection first")
			}
			switch v {
			case studioInherit:
				next.ApprovalAuthority = nil
			case "none", "user_directed", "standing":
				authority := v
				next.ApprovalAuthority = &authority
			default:
				return fmt.Errorf("unknown authority %q", v)
			}
			s.queueCmd(s.studioSaveCmd(scope, next, id))
			return nil
		},
	}
}

// studioResetField is the level's reset: the project's defaults return to the
// read-only built-in, and a session's override stops denying anything. Two
// presses, as every destructive settings row asks.
func studioResetField(s *state, scope string) *field {
	id := studioAccessRowID(scope)
	title := "Reset project defaults to inherit"
	if scope == acpwire.MarshalAccessScopeSession {
		title = "Revoke this session's override"
	}
	return &field{
		ID:    id,
		Title: title,
		Kind:  kindAction,
		Desc:  "write a level with every field unset, which restores inheritance (applies immediately)",
		ActLabel: func() string {
			if as, ok := s.actionState[id]; ok && as.label != "" {
				return as.label
			}
			return "reset"
		},
		Disarm: func() { delete(s.actionState, id) },
		Act: func() tea.Cmd {
			if as, ok := s.actionState[id]; !ok || as.label != "again to confirm" {
				s.actionState[id] = actionState{label: "again to confirm"}
				return nil
			}
			delete(s.actionState, id)
			if _, ok := s.studioLayerFor(scope); !ok {
				s.applyActionResult(id, "✗ read the connection first")
				return nil
			}
			return s.studioSaveCmd(scope, acpwire.MarshalAccess{}, id)
		},
	}
}

// studioConnection is the state row's value: what the server said, and why when
// it said nothing.
func (s *state) studioConnection() string {
	switch {
	case s.accessErr != "":
		return "✗ " + s.accessErr
	case !s.accessLoaded:
		return "reading…"
	case s.access.Reason != "":
		return s.access.State + " — " + s.access.Reason
	case s.access.State == "":
		return "unavailable"
	default:
		return s.access.State
	}
}

// studioInstance is the instance row's value: the bridge instance and the
// generation it is on, which is what a credential is scoped to.
func (s *state) studioInstance() string {
	if s.access.InstanceID == "" {
		return "—"
	}
	if s.access.Generation == "" {
		return s.access.InstanceID
	}
	return s.access.InstanceID + " · " + s.access.Generation
}

// studioLayer is the stored access at one level.
func (s *state) studioLayer(scope string) acpwire.MarshalAccess {
	if scope == acpwire.MarshalAccessScopeSession {
		if s.access.Override == nil {
			return acpwire.MarshalAccess{}
		}
		return s.access.Override.Access
	}
	return s.access.Default.Access
}

// studioLayerFor is studioLayer with the version the write must name. False
// means the surface has not read the level yet, and a write would name no
// version — so the row refuses instead of guessing one.
func (s *state) studioLayerFor(scope string) (acpwire.MarshalAccess, bool) {
	if s.accessErr != "" || !s.accessLoaded {
		return acpwire.MarshalAccess{}, false
	}
	if s.studioLayerVersion(scope) == "" {
		return acpwire.MarshalAccess{}, false
	}
	return s.studioLayer(scope), true
}

// studioLayerVersion is the opaque version of one level, as the last read
// reported it. A level nobody has written still reports the stable version of
// the absent value, so the first write is conditional on what was shown.
func (s *state) studioLayerVersion(scope string) string {
	if scope == acpwire.MarshalAccessScopeSession {
		return s.access.OverrideVersion
	}
	return s.access.Default.Version
}

// studioReadCmd reads the connection and the stored access through the client
// seam. It is a command, so a frame's getters stay pure and a read never blocks
// rendering.
func (s *state) studioReadCmd() tea.Cmd {
	sess := s.sess
	if sess == nil {
		return func() tea.Msg { return marshalAccessMsg{err: errNoSession} }
	}
	return func() tea.Msg {
		view, err := sess.MarshalAccess(context.Background())
		return marshalAccessMsg{view: view, err: err}
	}
}

// studioSaveCmd writes one level against the version it was read from. A stale
// write comes back as the value that won, with Stale set — an answer, not a
// failure, so the row can show the conflict and keep the user's edit.
func (s *state) studioSaveCmd(scope string, access acpwire.MarshalAccess, fieldID string) tea.Cmd {
	sess := s.sess
	if sess == nil {
		return func() tea.Msg { return marshalAccessMsg{fieldID: fieldID, err: errNoSession} }
	}
	version := s.studioLayerVersion(scope)
	if version == "" {
		return func() tea.Msg {
			return marshalAccessMsg{fieldID: fieldID, err: errAccessNotRead}
		}
	}
	return func() tea.Msg {
		view, err := sess.SaveMarshalAccess(context.Background(), acpwire.SaveMarshalAccessParams{
			Scope: scope, Access: access, Version: version,
		})
		return marshalAccessMsg{view: view, err: err, fieldID: fieldID}
	}
}

// studioSelectInstanceCmd records the project an ambiguous session acts on, or
// clears the choice.
func (s *state) studioSelectInstanceCmd(projectID, fieldID string) tea.Cmd {
	sess := s.sess
	if sess == nil {
		return func() tea.Msg { return marshalAccessMsg{fieldID: fieldID, err: errNoSession} }
	}
	version := s.access.SelectionVersion
	if version == "" {
		return func() tea.Msg { return marshalAccessMsg{fieldID: fieldID, err: errAccessNotRead} }
	}
	return func() tea.Msg {
		view, err := sess.SelectMarshalInstance(context.Background(), acpwire.SelectMarshalInstanceParams{
			ProjectID: projectID, Version: version,
		})
		return marshalAccessMsg{view: view, err: err, fieldID: fieldID}
	}
}

// studioSelectInstance queues one instance choice for the row that made it.
func (s *state) studioSelectInstance(projectID string) error {
	if projectID != "" && !s.accessLoaded {
		return errAccessNotRead
	}
	s.queueCmd(s.studioSelectInstanceCmd(projectID, "studio.instance.select"))
	return nil
}

// studioTriState is one access field as a row reads and writes it.
func studioTriState(v *bool) string {
	switch {
	case v == nil:
		return studioInherit
	case *v:
		return "allow"
	default:
		return "deny"
	}
}

// studioGrantSummary is a level's or the effective access's granted
// capabilities, in one line.
func studioGrantSummary(a acpwire.MarshalAccess) string {
	var granted []string
	for _, capability := range studioCapabilities {
		if v := capability.get(a); v != nil && *v {
			granted = append(granted, strings.ToLower(capability.title))
		}
	}
	if len(granted) == 0 {
		return "nothing granted"
	}
	return strings.Join(granted, ", ")
}

// studioCandidates renders the candidate list, or a dash for a session the
// server tracks nowhere.
func studioCandidates(candidates []string) string {
	if len(candidates) == 0 {
		return "—"
	}
	return strings.Join(candidates, ", ")
}

// studioOrDash renders an identity the view may not carry.
func studioOrDash(v string) string {
	if v == "" {
		return "—"
	}
	return v
}
