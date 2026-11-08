package acp

import (
	"context"
	"encoding/json"
	"strings"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
)

// BrowserManagerConfig wires one connection's browser handlers.
type BrowserManagerConfig struct {
	// Lookup resolves a session's service.
	Lookup func(sessionID string) (*service.Session, bool)
}

// BrowserManager serves a session's notes, commands, browsers and project
// reads over its service for one connection. It runs no forwarders.
type BrowserManager struct {
	lookup func(sessionID string) (*service.Session, bool)
}

// NewBrowserManager builds one connection's browser handlers.
func NewBrowserManager(cfg BrowserManagerConfig) *BrowserManager {
	return &BrowserManager{lookup: cfg.Lookup}
}

// service resolves a session's service, or reports why it cannot.
func (m *BrowserManager) service(sessionID string) (*service.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalidParamsError("sessionId is required")
	}
	if m.lookup != nil {
		if sess, ok := m.lookup(sessionID); ok && sess != nil {
			return sess, nil
		}
	}
	return nil, serverErrorf("unknown session: %s", sessionID)
}

// Note handles session/note: it appends the note to the transcript on the
// session's service. It is synchronous, so the note's stack patch is applied
// before the call returns and a read after it shows the note (R6).
func (m *BrowserManager) Note(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.NoteParams
	if err := decodeParams(params, &p, acpwire.MethodNote); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.AddNote(ctx, p.Note); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Command handles session/command for a marshal client: it runs the named
// command's handler over the session's service, row actions included.
func (m *BrowserManager) Command(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.CommandCallParams
	if err := decodeParams(params, &p, acpwire.MethodCommand); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.Command(ctx, p.Name, p.Args)
}

// CommandList handles session/command_list for a marshal client: it answers
// the session's registry in the service's order, with every kind unrewritten.
func (m *BrowserManager) CommandList(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodCommandList); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return acpwire.CommandListResult{Commands: sess.Commands()}, nil
}

// CommandAction handles session/command_action: it runs one of a recent
// result's row actions on the session's service.
func (m *BrowserManager) CommandAction(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.CommandActionParams
	if err := decodeParams(params, &p, acpwire.MethodCommandAction); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.CommandAction(ctx, p.ID)
}

// registerBrowsers registers the fourteen new methods on srv, each through
// settingsFailureOf, and the fourteen existing ones through marshalOr, whose
// marshal branch is wrapped in settingsFailureOf: a client without the
// capability reaches others[method], unchanged.
func registerBrowsers(srv *Server, browsers *BrowserManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodNote, settingsFailureOf(browsers.Note))
	srv.Handle(acpwire.MethodCommandAction, settingsFailureOf(browsers.CommandAction))

	srv.Handle(acpwire.MethodCommand,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.Command), others[acpwire.MethodCommand]))
	srv.Handle(acpwire.MethodCommandList,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.CommandList), others[acpwire.MethodCommandList]))
}
