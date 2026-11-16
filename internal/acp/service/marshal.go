package service

import (
	"context"
	"fmt"

	"marshal/internal/acpwire"
)

// The built-in Studio session's access surface (task 6).
//
// These are a user's settings, reached only through the client seam: the TUI
// reads and writes them like any other session setting, and the model-facing
// tool surface names none of them. The session is always the caller's own —
// the transport supplies the ID the handler looked the session up with, and
// nothing here trusts another one.
//
// A host that supplies no access hooks still serves the surface: it reports the
// Studio association the session can verify for itself and refuses writes,
// because the policy lives in the Studio bridge and a session that cannot reach
// one says so instead of inventing authority. That state never blocks ordinary
// agent work: no prompt, no tool and no turn reads this.

// errMarshalAccessUnavailable is what a write returns when the session has no
// Studio bridge to write to. The read surface reports the same condition as a
// state, so only a write — which a user asked for — fails.
var errMarshalAccessUnavailable = &acpwire.Error{
	Code:    acpwire.CodeMarshalAccessUnavailable,
	Message: "inproc: this session has no Studio access surface",
}

// MarshalAccess reports the session's Studio connection and the built-in access
// it holds: the instance and project it enrolled on, every candidate when the
// enrollment was ambiguous, the stored layers, the effective access and each
// field's source.
func (s *Session) MarshalAccess(ctx context.Context) (acpwire.MarshalAccessView, error) {
	if s.closed.Load() {
		return acpwire.MarshalAccessView{}, errSessionClosed
	}
	if s.marshalAccess != nil {
		view, err := s.marshalAccess()
		if err != nil {
			return acpwire.MarshalAccessView{}, err
		}
		return s.ownAccessView(view), nil
	}
	return s.localAccessView(), nil
}

// SaveMarshalAccess writes one access layer — the project's defaults or this
// session's override — against the version it was read from. A field left unset
// inherits the layer beneath it, so writing an access with every field unset is
// the reset: it restores inheritance instead of storing a denial.
func (s *Session) SaveMarshalAccess(ctx context.Context, params acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error) {
	if s.closed.Load() {
		return acpwire.MarshalAccessView{}, errSessionClosed
	}
	switch params.Scope {
	case acpwire.MarshalAccessScopeProject, acpwire.MarshalAccessScopeSession:
	case "":
		return acpwire.MarshalAccessView{}, fmt.Errorf("inproc: an access write needs a scope")
	default:
		return acpwire.MarshalAccessView{}, fmt.Errorf("inproc: unknown access scope %q", params.Scope)
	}
	if s.saveMarshalAccess == nil {
		return acpwire.MarshalAccessView{}, errMarshalAccessUnavailable
	}
	params.SessionID = s.ID()
	view, err := s.saveMarshalAccess(params)
	if err != nil {
		return acpwire.MarshalAccessView{}, err
	}
	return s.ownAccessView(view), nil
}

// SelectMarshalInstance records the project an ambiguous session acts on, or
// clears the choice when params.ProjectID is empty.
func (s *Session) SelectMarshalInstance(ctx context.Context, params acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error) {
	if s.closed.Load() {
		return acpwire.MarshalAccessView{}, errSessionClosed
	}
	if s.selectMarshalInstance == nil {
		return acpwire.MarshalAccessView{}, errMarshalAccessUnavailable
	}
	params.SessionID = s.ID()
	view, err := s.selectMarshalInstance(params)
	if err != nil {
		return acpwire.MarshalAccessView{}, err
	}
	return s.ownAccessView(view), nil
}

// ownAccessView names the session the answer describes. The bridge reports the
// session a binding named; this one is the session the caller is talking to, so
// a surface never has to reconcile the two.
func (s *Session) ownAccessView(view acpwire.MarshalAccessView) acpwire.MarshalAccessView {
	view.SessionID = s.ID()
	return view
}

// localAccessView is what a session with no bridge reports: the association its
// own enrollment recorded, or none. It is the connection identity and nothing
// else — the effective access and the layers are empty because this session
// cannot read them, and it says so in Reason rather than showing a fabricated
// policy.
func (s *Session) localAccessView() acpwire.MarshalAccessView {
	view := acpwire.MarshalAccessView{
		State:  acpwire.MarshalAccessUnavailable,
		Reason: "This session reports its Studio connection only; access is managed in Studio.",
	}
	if s == nil || s.state == nil {
		return view
	}
	view.SessionID = s.ID()
	assoc := s.state.MarshalSession()
	if !assoc.Verified() {
		return view
	}
	view.State = acpwire.MarshalAccessConnected
	view.InstanceID, view.Generation, view.ProjectID = assoc.InstanceID, assoc.Generation, assoc.ProjectID
	return view
}
