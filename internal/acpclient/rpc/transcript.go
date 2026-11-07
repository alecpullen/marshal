package rpc

import (
	"context"
	"time"

	"marshal/internal/acpwire"
)

// stackCallTimeout bounds the calls Stack makes on a read: the busy flip and a
// first fetch.
const stackCallTimeout = 5 * time.Second

// sheetEntry is one mirrored sheet: the last sheet the session holds, and
// whether an update replaced it since the entry's call went out.
type sheetEntry struct {
	sheet   acpwire.SessionSheet
	updated bool
}

// Stack is the transcript the TUI renders: the session's own when subagentID
// is 0, or that of a subagent. busy says a turn is running, which the agent
// uses for the live beat; it is sent only when it differs from the flag last
// sent, so the first read of a tree is the only one that blocks on a fetch.
//
// A read the agent refuses answers an empty snapshot carrying the subagent ID:
// an ID that names no child reads as empty, and the next read asks again.
func (s *Session) Stack(subagentID int64, busy bool) acpwire.StackSnapshot {
	empty := acpwire.StackSnapshot{SessionID: s.id, SubagentID: subagentID}

	ctx, cancel := context.WithTimeout(context.Background(), stackCallTimeout)
	defer cancel()

	s.mu.Lock()
	flip := !s.busySent || s.busy != busy
	s.mu.Unlock()
	if flip {
		params := acpwire.StackBusyParams{SessionID: s.id, Busy: busy}
		if err := s.conn.Call(ctx, acpwire.MethodStackBusy, params, nil); err == nil {
			// Only a delivered flag is recorded: a failed flip leaves the old
			// record, so the next read tries again.
			s.mu.Lock()
			s.busy = busy
			s.busySent = true
			s.mu.Unlock()
		}
	}

	if s.stacks == nil {
		return empty
	}
	snap, err := s.stacks.Stack(ctx, s.id, subagentID)
	if err != nil {
		// A refused read keeps no entry, so the next read fetches again.
		return empty
	}
	return snap
}

// StackNode is one transcript node's wire form with the detail the projection
// caps or leaves out.
func (s *Session) StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error) {
	var result acpwire.StackNodeResult
	params := acpwire.StackNodeParams{SessionID: s.id, NodeID: nodeID, SubagentID: subagentID}
	if err := s.conn.Call(ctx, acpwire.MethodStackNode, params, &result); err != nil {
		return acpwire.StackNodeResult{}, err
	}
	return result, nil
}

// Callers lists what references the symbol a call row changed. It returns nil
// when the agent omits the list, which is what a row that changed no resolved
// symbol reads as.
func (s *Session) Callers(ctx context.Context, nodeID string) ([]string, error) {
	var result acpwire.CallersResult
	params := acpwire.CallersParams{SessionID: s.id, NodeID: nodeID}
	if err := s.conn.Call(ctx, acpwire.MethodCallers, params, &result); err != nil {
		return nil, err
	}
	return result.Refs, nil
}

// Sheet is the mirrored sheet of a subagent ID: no I/O once the session has
// read it. The first read fetches it with session/sheet, which subscribes the
// connection; a fetch the agent refuses leaves no entry, so the next read asks
// again.
func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet {
	s.mu.Lock()
	if entry, ok := s.sheets[subagentID]; ok {
		sheet := entry.sheet
		s.mu.Unlock()
		return sheet
	}
	entry := &sheetEntry{}
	s.sheets[subagentID] = entry
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), stackCallTimeout)
	defer cancel()

	var sheet acpwire.SessionSheet
	params := acpwire.SheetParams{SessionID: s.id, SubagentID: subagentID}
	if err := s.conn.Call(ctx, acpwire.MethodSheet, params, &sheet); err != nil {
		s.mu.Lock()
		if s.sheets[subagentID] == entry {
			delete(s.sheets, subagentID)
		}
		s.mu.Unlock()
		return acpwire.SessionSheet{}
	}

	s.mu.Lock()
	if s.sheets[subagentID] == entry {
		if !entry.updated {
			// An update that arrived during the call is newer than the result
			// the agent computed before sending it.
			entry.sheet = sheet
		}
		sheet = entry.sheet
	}
	s.mu.Unlock()
	return sheet
}

// RefreshSheet re-reads the sheet values req names, then fires Changed even
// when nothing differed: the TUI's refresh relies on that signal, because it
// has no message of its own to collect.
func (s *Session) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error {
	s.mu.Lock()
	entry, ok := s.sheets[0]
	if !ok {
		entry = &sheetEntry{}
		s.sheets[0] = entry
	}
	entry.updated = false
	s.mu.Unlock()

	var sheet acpwire.SessionSheet
	params := acpwire.SheetParams{SessionID: s.id, Refresh: &req}
	if err := s.conn.Call(ctx, acpwire.MethodSheet, params, &sheet); err != nil {
		s.mu.Lock()
		if !ok && s.sheets[0] == entry {
			delete(s.sheets, 0)
		}
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sheets[0] == entry && !entry.updated {
		entry.sheet = sheet
	}
	s.signalLocked()
	return nil
}

// ReleaseSubagent unsubscribes a subagent's transcript and forgets its sheet.
// The main transcript cannot be released, so 0 does nothing.
func (s *Session) ReleaseSubagent(ctx context.Context, subagentID int64) error {
	if subagentID == 0 {
		return nil
	}
	s.mu.Lock()
	delete(s.sheets, subagentID)
	s.mu.Unlock()

	if s.stacks == nil {
		return errStacksUnbound
	}
	return s.stacks.Release(ctx, s.id, subagentID)
}

// applySheet replaces a sheet the session has read with an update's and fires
// Changed. An update for an ID with no entry is dropped: the next read fetches
// it instead.
func (s *Session) applySheet(update acpwire.SheetUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.sheets[update.SubagentID]
	if !ok {
		return
	}
	entry.sheet = update.Sheet
	entry.updated = true
	s.signalLocked()
}
