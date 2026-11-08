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

// PromptHistory handles session/prompt_history: the session's recent prompts,
// newest first.
func (m *BrowserManager) PromptHistory(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PromptHistoryParams
	if err := decodeParams(params, &p, acpwire.MethodPromptHistory); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	prompts, err := sess.RecentPrompts(ctx, p.Limit)
	if err != nil {
		return nil, err
	}
	return acpwire.PromptHistoryResult{Prompts: prompts}, nil
}

// PromptHistorySave handles session/prompt_history_save: it records one prompt
// in the session's history.
func (m *BrowserManager) PromptHistorySave(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PromptSaveParams
	if err := decodeParams(params, &p, acpwire.MethodPromptHistorySave); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.SavePrompt(ctx, p.Text); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// Messages handles session/messages: one page of the session's transcript
// selected by the caller's query.
func (m *BrowserManager) Messages(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.MessagesParams
	if err := decodeParams(params, &p, acpwire.MethodMessages); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.Messages(ctx, p.Query)
}

// FileIndex handles session/file_index: the paths the client's @ popup offers.
func (m *BrowserManager) FileIndex(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodFileIndex); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	paths, err := sess.FileIndex(ctx)
	if err != nil {
		return nil, err
	}
	return acpwire.FileIndexResult{Paths: paths}, nil
}

// Intelligence handles session/intelligence: the doctor's four checks.
func (m *BrowserManager) Intelligence(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodIntelligence); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	checks, err := sess.Intelligence(ctx)
	if err != nil {
		return nil, err
	}
	return acpwire.IntelligenceResult{Checks: checks}, nil
}

// Plans handles session/plans: the SDD plans the project discovers.
func (m *BrowserManager) Plans(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodPlans); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	plans, err := sess.Plans(ctx)
	if err != nil {
		return nil, err
	}
	return acpwire.PlansResult{Plans: plans}, nil
}

// SDDDiscard handles session/sdd_discard: it removes an authored candidate.
func (m *BrowserManager) SDDDiscard(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PathParams
	if err := decodeParams(params, &p, acpwire.MethodSDDDiscard); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.DiscardCandidate(ctx, p.Path); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

// SDDScaffold handles session/sdd_scaffold: it writes a starter plan when the
// draft has none.
func (m *BrowserManager) SDDScaffold(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.ControlParams
	if err := decodeParams(params, &p, acpwire.MethodSDDScaffold); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.ScaffoldPlan(ctx)
}

// Diff handles session/diff for a marshal client: the unified diff of the
// range Base..Head, which ACP's handler cannot name.
func (m *BrowserManager) Diff(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.DiffCallParams
	if err := decodeParams(params, &p, acpwire.MethodDiff); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	diff, err := sess.Diff(ctx, acpwire.DiffQuery{Base: p.Base, Head: p.Head})
	if err != nil {
		return nil, err
	}
	return acpwire.DiffResult{Diff: diff}, nil
}

// File handles session/file for a marshal client: one file's content, read
// inside the session's working directory.
func (m *BrowserManager) File(ctx context.Context, params json.RawMessage) (any, error) {
	var p acpwire.PathParams
	if err := decodeParams(params, &p, acpwire.MethodFile); err != nil {
		return nil, invalidParamsError("%v", err)
	}
	sess, err := m.service(p.SessionID)
	if err != nil {
		return nil, err
	}
	return sess.ReadFile(ctx, p.Path)
}

// registerBrowsers registers the fourteen new methods on srv, each through
// settingsFailureOf, and the fourteen existing ones through marshalOr, whose
// marshal branch is wrapped in settingsFailureOf: a client without the
// capability reaches others[method], unchanged.
func registerBrowsers(srv *Server, browsers *BrowserManager, others map[string]Handler) {
	srv.Handle(acpwire.MethodNote, settingsFailureOf(browsers.Note))
	srv.Handle(acpwire.MethodCommandAction, settingsFailureOf(browsers.CommandAction))
	srv.Handle(acpwire.MethodPromptHistory, settingsFailureOf(browsers.PromptHistory))
	srv.Handle(acpwire.MethodPromptHistorySave, settingsFailureOf(browsers.PromptHistorySave))
	srv.Handle(acpwire.MethodMessages, settingsFailureOf(browsers.Messages))
	srv.Handle(acpwire.MethodFileIndex, settingsFailureOf(browsers.FileIndex))
	srv.Handle(acpwire.MethodIntelligence, settingsFailureOf(browsers.Intelligence))
	srv.Handle(acpwire.MethodPlans, settingsFailureOf(browsers.Plans))
	srv.Handle(acpwire.MethodSDDDiscard, settingsFailureOf(browsers.SDDDiscard))
	srv.Handle(acpwire.MethodSDDScaffold, settingsFailureOf(browsers.SDDScaffold))

	srv.Handle(acpwire.MethodCommand,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.Command), others[acpwire.MethodCommand]))
	srv.Handle(acpwire.MethodCommandList,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.CommandList), others[acpwire.MethodCommandList]))
	srv.Handle(acpwire.MethodDiff,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.Diff), others[acpwire.MethodDiff]))
	srv.Handle(acpwire.MethodFile,
		marshalOr(srv.marshalClient, settingsFailureOf(browsers.File), others[acpwire.MethodFile]))
}
