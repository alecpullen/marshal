package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/daemon"
)

// The built-in Studio access surface: a person's settings for the integration —
// a project's access defaults, one session's override and the project an
// ambiguous session acts on.
//
// The policy lives in the Studio bridge's workspace, so every read and every
// write goes to the daemon that holds it, over the same owner-only socket
// enrollment uses. Nothing here is reachable by the model: these hooks serve the
// client seam's settings methods only, and no MCP tool names them.
//
// A session whose daemon is absent, too old, or refuses the call gets the one
// unavailable error the seam knows. That is a settings failure and never an
// agent failure: a turn, a tool and a prompt read none of this, and the surface
// still shows the connection the session can verify for itself.

// marshalAccessTimeout bounds one settings round trip. The daemon is local and
// the answer is a small object; a call that takes longer is a daemon that is not
// answering, and the surface should say so rather than hang the settings panel.
const marshalAccessTimeout = 5 * time.Second

// marshalAccessHooks builds the seam's three access hooks for one session.
// sessionID is the session those settings describe; the bridge resolves the
// project from the association it already tracks when a caller names none.
func marshalAccessHooks(homeDir, sessionID string) (
	func() (acpwire.MarshalAccessView, error),
	func(acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error),
	func(acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error),
) {
	client := daemon.NewClient(daemon.PathsFor(homeDir).Socket)

	read := func() (acpwire.MarshalAccessView, error) {
		return marshalAccessRead(context.Background(), client, "", sessionID, "")
	}
	save := func(params acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error) {
		return marshalAccessSave(context.Background(), client, sessionID, params)
	}
	selectInstance := func(params acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error) {
		return marshalAccessSelectInstance(context.Background(), client, sessionID, params)
	}
	return read, save, selectInstance
}

// marshalAccessRead reads one session's access surface.
func marshalAccessRead(ctx context.Context, client *daemon.Client, projectID, sessionID, workerID string) (acpwire.MarshalAccessView, error) {
	callCtx, cancel := context.WithTimeout(ctx, marshalAccessTimeout)
	defer cancel()
	answer, err := client.MarshalAccess(callCtx, projectID, sessionID, workerID)
	if err != nil {
		return acpwire.MarshalAccessView{}, marshalAccessError(err)
	}
	return marshalAccessViewOf(answer)
}

// marshalAccessViewOf decodes one answer. A conditional write that lost answers
// 409 with the value that won, and that is reported as Stale rather than as an
// error, so an editor keeps the edit it was making and shows the stored value
// beside it.
func marshalAccessViewOf(answer daemon.MarshalAccessAnswer) (acpwire.MarshalAccessView, error) {
	var view acpwire.MarshalAccessView
	if err := json.Unmarshal(answer.Body, &view); err != nil {
		return acpwire.MarshalAccessView{}, marshalAccessUnavailable("the answer was not an access view")
	}
	view.Stale = answer.Status == 409
	return view, nil
}

// marshalAccessSave writes one layer. The project is read first because the
// session names it rather than the surface: the bridge's project ids are opaque
// and only its own association can supply one.
func marshalAccessSave(ctx context.Context, client *daemon.Client, sessionID string, params acpwire.SaveMarshalAccessParams) (acpwire.MarshalAccessView, error) {
	current, err := marshalAccessRead(ctx, client, "", sessionID, "")
	if err != nil {
		return acpwire.MarshalAccessView{}, err
	}
	projectID := current.ProjectID
	if projectID == "" {
		projectID = current.SelectedProjectID
	}
	if projectID == "" {
		// An ambiguous or disconnected session has no project to write a
		// default for, and guessing one would widen where the policy applies.
		return acpwire.MarshalAccessView{}, marshalAccessUnavailable("this session has no single Studio project")
	}

	callCtx, cancel := context.WithTimeout(ctx, marshalAccessTimeout)
	defer cancel()

	var answer daemon.MarshalAccessAnswer
	switch params.Scope {
	case acpwire.MarshalAccessScopeProject:
		body, err := json.Marshal(struct {
			ProjectID string                `json:"projectId"`
			Access    acpwire.MarshalAccess `json:"access"`
			Version   string                `json:"version"`
		}{ProjectID: projectID, Access: params.Access, Version: params.Version})
		if err != nil {
			return acpwire.MarshalAccessView{}, marshalAccessUnavailable("the project defaults could not be encoded")
		}
		answer, err = client.SaveMarshalAccessDefault(callCtx, body)
		if err != nil {
			return acpwire.MarshalAccessView{}, marshalAccessError(err)
		}
	case acpwire.MarshalAccessScopeSession:
		body, err := json.Marshal(struct {
			ProjectID string                `json:"projectId"`
			SessionID string                `json:"sessionId"`
			WorkerID  string                `json:"workerId,omitempty"`
			Access    acpwire.MarshalAccess `json:"access"`
			Version   string                `json:"version"`
		}{ProjectID: projectID, SessionID: sessionID, WorkerID: current.WorkerID, Access: params.Access, Version: params.Version})
		if err != nil {
			return acpwire.MarshalAccessView{}, marshalAccessUnavailable("the session override could not be encoded")
		}
		answer, err = client.SaveMarshalAccessOverride(callCtx, body)
		if err != nil {
			return acpwire.MarshalAccessView{}, marshalAccessError(err)
		}
	default:
		return acpwire.MarshalAccessView{}, marshalAccessUnavailable("unknown access scope")
	}
	return marshalAccessViewOf(answer)
}

// marshalAccessSelectInstance records the project an ambiguous session acts on,
// or clears the choice when params.ProjectID is empty.
func marshalAccessSelectInstance(ctx context.Context, client *daemon.Client, sessionID string, params acpwire.SelectMarshalInstanceParams) (acpwire.MarshalAccessView, error) {
	body, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		ProjectID string `json:"projectId,omitempty"`
		Version   string `json:"version"`
	}{SessionID: sessionID, ProjectID: params.ProjectID, Version: params.Version})
	if err != nil {
		return acpwire.MarshalAccessView{}, marshalAccessUnavailable("the selection could not be encoded")
	}
	callCtx, cancel := context.WithTimeout(ctx, marshalAccessTimeout)
	defer cancel()
	answer, err := client.SelectMarshalInstance(callCtx, body)
	if err != nil {
		return acpwire.MarshalAccessView{}, marshalAccessError(err)
	}
	return marshalAccessViewOf(answer)
}

// marshalAccessError is the seam's one settings failure. The daemon's own text
// names no path and no credential, so it is kept as the reason.
func marshalAccessError(err error) error {
	return &acpwire.Error{
		Code:    acpwire.CodeMarshalAccessUnavailable,
		Message: fmt.Sprintf("The Studio access surface is unavailable: %v", err),
	}
}

// marshalAccessUnavailable is the same failure for a condition this side
// detected before reaching the daemon.
func marshalAccessUnavailable(reason string) error {
	return &acpwire.Error{
		Code:    acpwire.CodeMarshalAccessUnavailable,
		Message: fmt.Sprintf("The Studio access surface is unavailable: %s", reason),
	}
}
