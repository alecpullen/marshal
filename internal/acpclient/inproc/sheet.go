package inproc

import (
	"context"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/repo/changedfiles"
	"marshal/internal/viewmodel"
	"marshal/internal/worktree"
)

// sheetCache holds what RefreshSheet read. Session.mu guards it.
type sheetCache struct {
	base     string // HEAD of baseRoot at the last rebase; "" before the first
	baseRoot string
	changed  []acpwire.SheetChangedFile
	turns    []acpwire.SheetTurn
	totals   acpwire.SheetTotals
	repo     acpwire.SheetRepo
	fleet    []acpwire.SheetFleetRow
}

// Sheet is what the session sheet's sections read (C5): the session's own
// sheet when subagentID is 0, or the sheet of the subagent with that ID
// anywhere in the session's tree. An ID that names no child session reads as
// 0, and the result's SubagentID says which sheet was read.
//
// The database and git values are the ones the last RefreshSheet read. The
// cached slices are replaced, never changed in place, so they are returned
// without copying.
func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet {
	st := s.state
	id := int64(0)
	if subagentID != 0 {
		if child := childState(s.state, subagentID); child != nil {
			st = child
			id = subagentID
		}
	}
	sheet := activityOf(st)
	sheet.SubagentID = id
	// Read the pack before taking the mutex: the cache is the only thing the
	// lock protects.
	var ctxSheet acpwire.SheetContext
	if id == 0 {
		ctxSheet = contextOf(st.ContextPack())
	}
	s.mu.Lock()
	sheet.Fleet = s.sheet.fleet
	sheet.Repo = s.sheet.repo
	if id == 0 {
		sheet.Changed = s.sheet.changed
		sheet.Turns = s.sheet.turns
		sheet.Totals = s.sheet.totals
		sheet.Context = ctxSheet
	}
	s.mu.Unlock()
	return sheet
}

// RefreshSheet re-reads the sheet values req names from the database and git,
// then fires Changed. It blocks on those reads.
//
// The reads run outside the mutex; the results are stored under it, and only
// when the workspace they were read for is still the active one. ctx is not
// consulted: none of these reads takes one.
func (s *Session) RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	ws := s.state.Workspace()
	root := ws.ActiveRoot
	fleetRoot := ws.ProjectRoot
	if fleetRoot == "" {
		fleetRoot = s.state.WorkingDir
	}

	var (
		changed      []acpwire.SheetChangedFile
		readChanged  bool
		rebased      bool
		newBase      string
		readBase     string
		readBaseRoot string
	)
	if (req.Rebase || req.Changed) && root != "" {
		s.mu.Lock()
		readBase, readBaseRoot = s.sheet.base, s.sheet.baseRoot
		s.mu.Unlock()
		// A Changed read with no base yet, or one read for another root,
		// rebases first.
		rebased = req.Rebase || readBase == "" || readBaseRoot != root
		if rebased {
			// An unreadable HEAD keeps the base and the changed files as they
			// were.
			if head := s.headSHA(root); head != "" {
				changed = changedOf(changedfiles.Read(root, head))
				newBase = head
				readChanged = true
			}
		} else {
			changed = changedOf(changedfiles.Read(root, readBase))
			readChanged = true
		}
	}

	var (
		turns      []acpwire.SheetTurn
		totals     acpwire.SheetTotals
		repo       acpwire.SheetRepo
		haveTurns  bool
		haveTotals bool
		haveRepo   bool
	)
	if req.Stats {
		if database := s.state.DB(); database != nil {
			if rows, err := database.RecentTurnMetrics(s.projectID, 24); err == nil {
				turns, haveTurns = turnsOf(rows), true
			}
			if t, err := database.SessionUsage(s.projectID, s.state.SessionID()); err == nil {
				totals, haveTotals = totalsOf(t), true
			}
			if s.projectID != 0 {
				files, ferr := database.CountFiles(s.projectID)
				syms, serr := database.CountSymbols(s.projectID)
				if ferr == nil && serr == nil {
					repo, haveRepo = acpwire.SheetRepo{Files: files, Symbols: syms}, true
				}
			}
		}
	}

	var fleet []acpwire.SheetFleetRow
	if req.Fleet {
		// Any failure empties the list.
		if base, err := (worktree.CLIGitOps{}).RevParse(fleetRoot, "HEAD"); err == nil {
			if rows, lerr := worktree.ListFleet(worktree.CLIGitOps{}, fleetRoot, base); lerr == nil {
				fleet = fleetOf(rows)
			}
		}
	}

	// A read for a root the workspace has since left is dropped: linked
	// worktrees share the object store, so it would describe the wrong tree.
	if s.state.Workspace().ActiveRoot != root {
		readChanged = false
	}

	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		return errSessionClosed
	}
	if readChanged {
		// A Changed read is also dropped when the base moved under it.
		if rebased || (s.sheet.base == readBase && s.sheet.baseRoot == readBaseRoot) {
			s.sheet.changed = changed
			if rebased {
				s.sheet.base = newBase
				s.sheet.baseRoot = root
			}
		}
	}
	if haveTurns {
		s.sheet.turns = turns
	}
	if haveTotals {
		s.sheet.totals = totals
	}
	if haveRepo {
		s.sheet.repo = repo
	}
	if req.Fleet {
		s.sheet.fleet = fleet
	}
	s.mu.Unlock()

	s.signal()
	return nil
}

// changedFiles is the number of files changed against the sheet's base.
func (s *Session) changedFiles() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sheet.changed)
}

// childState is the state of the subagent with id anywhere in st's tree, or
// nil when no view names it. A view with no child state is skipped: it has no
// activity of its own to read.
func childState(st *session.State, id int64) *session.State {
	for _, v := range st.Subagents() {
		if v.Child == nil {
			continue
		}
		if v.ID == id {
			return v.Child
		}
		if found := childState(v.Child, id); found != nil {
			return found
		}
	}
	return nil
}

// activityOf fills the sheet values a state carries in memory. The database
// and git values are left for RefreshSheet.
func activityOf(st *session.State) acpwire.SessionSheet {
	events := st.AuditLog()
	budget := st.ToolBudget()
	sandbox := st.SandboxInfo()
	return acpwire.SessionSheet{
		StartedAt: acpwire.Millis(st.StartedAt),
		Tools:     viewmodel.SheetToolsOf(events),
		Files:     viewmodel.SheetFilesOf(events),
		Rules:     nilIfEmpty(st.SessionRules()),
		Skills:    nilIfEmpty(st.ActiveSkills()),
		ToolBudget: acpwire.SheetToolBudget{
			Used: budget.Used,
			Max:  budget.Max,
		},
		LoadedTools: len(st.LoadedToolNames()),
		// Backend is the sandbox actually in force; AllowNetwork is the
		// configured intent. Session.SandboxInfo reports observed network
		// isolation instead, which is a different question: a restricted
		// backend isolates even where the config would allow the network.
		Sandbox: acpwire.SheetSandbox{
			Backend:      sandbox.Backend,
			AllowNetwork: st.Config.Tools.Shell.AllowNetwork,
		},
	}
}

// contextOf copies a context pack's size and its sections' sizes. Every
// section is listed, an empty one included: the context section is relevant
// whenever the pack has any section. A pack with no sections gives nil
// Sections.
func contextOf(p contextpack.Pack) acpwire.SheetContext {
	out := acpwire.SheetContext{
		MaxTokens:       p.TokenUsage.MaxTokens,
		EstimatedTokens: p.TokenUsage.EstimatedTokens,
	}
	if len(p.Sections) == 0 {
		return out
	}
	out.Sections = make([]acpwire.SheetContextSection, 0, len(p.Sections))
	for _, s := range p.Sections {
		out.Sections = append(out.Sections, acpwire.SheetContextSection{
			Title:           s.Title,
			EstimatedTokens: s.EstimatedTokens,
		})
	}
	return out
}

// nilIfEmpty returns nil for an empty slice, so the wire value encodes as
// absent under omitempty.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// changedOf copies the repo package's changed-file rows onto the wire.
func changedOf(files []changedfiles.File) []acpwire.SheetChangedFile {
	if files == nil {
		return nil
	}
	out := make([]acpwire.SheetChangedFile, 0, len(files))
	for _, f := range files {
		out = append(out, acpwire.SheetChangedFile{
			Path:    f.Path,
			Status:  string(f.Status),
			Added:   f.Added,
			Removed: f.Removed,
		})
	}
	return out
}

// turnsOf copies the turn-metrics rows onto the wire, most recent first as
// the query returns them.
func turnsOf(rows []db.TurnMetricsRow) []acpwire.SheetTurn {
	if rows == nil {
		return nil
	}
	out := make([]acpwire.SheetTurn, 0, len(rows))
	for _, r := range rows {
		out = append(out, acpwire.SheetTurn{
			StartedAt:        acpwire.Millis(r.StartedAt),
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			CacheReadTokens:  r.CacheReadTokens,
		})
	}
	return out
}

// totalsOf copies the session's usage aggregate onto the wire.
func totalsOf(t db.UsageTotals) acpwire.SheetTotals {
	return acpwire.SheetTotals{
		Turns:            t.Turns,
		PromptTokens:     t.PromptTokens,
		CompletionTokens: t.CompletionTokens,
		CacheReadTokens:  t.CacheReadTokens,
	}
}

// fleetOf copies the agent-worktree rows onto the wire. Path and Session are
// not carried: the sheet renders the branch and its divergence only.
func fleetOf(rows []worktree.FleetRow) []acpwire.SheetFleetRow {
	if rows == nil {
		return nil
	}
	out := make([]acpwire.SheetFleetRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, acpwire.SheetFleetRow{
			Branch: r.Branch,
			Ahead:  r.Ahead,
			Behind: r.Behind,
			Dirty:  r.Dirty,
			AgeMs:  r.Age.Milliseconds(),
		})
	}
	return out
}
