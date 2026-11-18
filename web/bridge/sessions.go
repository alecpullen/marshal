package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The listing cache's lifetimes (P7 D10). A success is served for ten
// seconds and a failure for thirty: a root that cannot answer is usually a
// child that is starting or gone, and retrying it on every read would fan one
// broken root out into every caller.
const (
	listFreshFor = 10 * time.Second
	listStaleFor = 30 * time.Second
	// listTimeoutDefault bounds each session/list when the fleet sets none.
	listTimeoutDefault = 5 * time.Second
)

// listingEntry is one root's cached listing: the rows it answered with, the
// error it failed with (nil on success), and when it was read.
type listingEntry struct {
	sessions []contract.SessionSummary
	err      error
	readAt   time.Time
}

// ResolvedSession is where one session id lives (P7 D2).
type ResolvedSession struct {
	Reg       *Registry
	Log       *EventLog
	SessionID string // the ACP session id to address Reg with
	Root      string // the project root
	Agent     *agentRuntime
}

// ErrSessionOwned refuses a session the bridge owns (P7 D5).
type ErrSessionOwned struct{ Owner string } // "agent <name>", "the control agent", "planner <id>"

func (e ErrSessionOwned) Error() string { return "bridge: session belongs to " + e.Owner }

// ResolveSession finds id among the live: an agent runtime by session id, an
// agent id or persisted agent, then a live project child that tracks it as a
// client's session. It never starts a project child.
func (f *Fleet) ResolveSession(id string) (ResolvedSession, error) {
	rt, err := f.RuntimeForSession(id)
	if err == nil {
		return ResolvedSession{
			Reg:       rt.reg,
			Log:       rt.log,
			SessionID: f.sessionIDFor(rt),
			Root:      rt.root,
			Agent:     rt,
		}, nil
	}
	if !errors.Is(err, ErrUnknownSession) {
		return ResolvedSession{}, err
	}
	for _, pc := range f.projectChildrenInRootOrder() {
		// The child's registry and log are written under its own lock by a
		// start, so they are read under it too, as every other reader does.
		pc.mu.Lock()
		reg, log := pc.reg, pc.log
		pc.mu.Unlock()
		if reg == nil || !reg.tracksClient(id) {
			continue
		}
		return ResolvedSession{Reg: reg, Log: log, SessionID: id, Root: pc.root}, nil
	}
	return ResolvedSession{}, ErrUnknownSession
}

// OpenSession is ResolveSession, and for a closed session the reopening on its
// project's child (P7 D3).
func (f *Fleet) OpenSession(ctx context.Context, id string) (ResolvedSession, error) {
	rs, err := f.ResolveSession(id)
	if err == nil {
		return rs, nil
	}
	if !errors.Is(err, ErrUnknownSession) {
		return ResolvedSession{}, err
	}
	// The bridge's own sessions are never reopened: an agent's session is its
	// child's to address, the control agent's sessions are the Studio's, and a
	// planner's session belongs to the planner (P7 D5).
	if owner, ok := f.ownedSessions()[id]; ok {
		return ResolvedSession{}, ErrSessionOwned{Owner: owner}
	}
	root, err := f.rootOfSession(ctx, id)
	if err != nil {
		return ResolvedSession{}, err
	}
	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		return ResolvedSession{}, err
	}
	pc.mu.Lock()
	reg, log := pc.reg, pc.log
	pc.mu.Unlock()
	if reg == nil {
		return ResolvedSession{}, fmt.Errorf("bridge: project child for %s is not running", pc.root)
	}
	if !reg.tracksClient(id) {
		if err := reg.Resume(ctx, AgentPath(pc.root), id); err != nil {
			// Resolving the child cancelled its linger; a resume that leaves
			// nothing behind must arm it again, as the attach handshake does
			// (attach.go:185-191).
			f.rearmLinger(pc)
			return ResolvedSession{}, err
		}
		f.followClientSession(ctx, reg, id)
	}
	return ResolvedSession{Reg: reg, Log: log, SessionID: id, Root: pc.root}, nil
}

// ownedSessions maps each session the bridge owns to its owner phrase.
func (f *Fleet) ownedSessions() map[string]string {
	out := make(map[string]string)
	for _, a := range f.ws.Agents() {
		if a.SessionID == "" {
			continue
		}
		out[a.SessionID] = "agent " + agentDisplayName(a)
	}
	// The classifier's map is read in one hold: it is written under f.mu by a
	// spawn, and an entry can name an agent whose record is already gone.
	f.mu.Lock()
	classified := make(map[string]string, len(f.sessionAgent))
	for sessionID, agentID := range f.sessionAgent {
		classified[sessionID] = agentID
	}
	f.mu.Unlock()
	for sessionID, agentID := range classified {
		if sessionID == "" {
			continue
		}
		name := agentID
		if a, ok := f.ws.Agent(agentID); ok {
			name = agentDisplayName(a)
		}
		out[sessionID] = "agent " + name
	}
	// The control agent is read here rather than through control(ctx): this
	// must never start it.
	if c := f.ctl; c != nil {
		c.mu.Lock()
		if c.sessionID != "" {
			out[c.sessionID] = "the control agent"
		}
		for _, sessionID := range c.projectSessions {
			if sessionID != "" {
				out[sessionID] = "the control agent"
			}
		}
		c.mu.Unlock()
	}
	if f.planners != nil {
		for sessionID, plannerID := range f.planners.sessionIDs() {
			if sessionID == "" {
				continue
			}
			out[sessionID] = "planner " + plannerID
		}
	}
	return out
}

// knownRoots is the sorted union of the workspace's projects and the live
// project children's roots.
func (f *Fleet) knownRoots() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(root string) {
		root = filepath.Clean(root)
		if root == "" || root == "." || seen[root] {
			return
		}
		seen[root] = true
		out = append(out, root)
	}
	for _, root := range f.ws.Projects() {
		add(root)
	}
	f.projMu.Lock()
	for root := range f.projects {
		add(root)
	}
	f.projMu.Unlock()
	sort.Strings(out)
	return out
}

// rootOfSession is the root whose listing names id.
func (f *Fleet) rootOfSession(ctx context.Context, id string) (string, error) {
	f.listMu.Lock()
	root, ok := f.sessionRoots[id]
	f.listMu.Unlock()
	if ok {
		return root, nil
	}
	// Every known root is listed in parallel, each through listRoot's own
	// bound, so one stalled root cannot hold the answer (decision 6).
	roots := f.knownRoots()
	var wg sync.WaitGroup
	for _, root := range roots {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			_, _ = f.listRoot(ctx, root)
		}(root)
	}
	wg.Wait()

	f.listMu.Lock()
	root, ok = f.sessionRoots[id]
	f.listMu.Unlock()
	if !ok {
		return "", ErrUnknownSession
	}
	return root, nil
}

// listRoot is root's first page of stored sessions, normalised (P7 D8),
// through the listing cache (P7 D10). The cache's lock is read and written
// around the child request, never across it.
func (f *Fleet) listRoot(ctx context.Context, root string) ([]contract.SessionSummary, error) {
	root = filepath.Clean(root)
	now := f.listClock()
	f.listMu.Lock()
	if entry, ok := f.listings[root]; ok {
		age := now.Sub(entry.readAt)
		if entry.err == nil && age < listFreshFor {
			sessions := entry.sessions
			f.listMu.Unlock()
			return sessions, nil
		}
		if entry.err != nil && age < listStaleFor {
			err := entry.err
			f.listMu.Unlock()
			return nil, err
		}
	}
	f.listMu.Unlock()

	sessions, err := f.fetchListing(ctx, root)

	// A caller that went away mid-request is not a failure of the root: the
	// child may be perfectly healthy, and caching the cancellation would mark
	// every session in this root dead for the whole failure TTL. Only the
	// listing's own timeout is a fact about the root worth keeping (P7 D10).
	if ctx.Err() != nil {
		return sessions, err
	}

	f.listMu.Lock()
	if f.listings == nil {
		f.listings = make(map[string]listingEntry)
	}
	f.listings[root] = listingEntry{sessions: sessions, err: err, readAt: f.listClock()}
	if err == nil {
		if f.sessionRoots == nil {
			f.sessionRoots = make(map[string]string)
		}
		f.forgetRootSessionsLocked(root)
		for _, s := range sessions {
			f.sessionRoots[s.SessionID] = root
		}
	}
	f.listMu.Unlock()
	return sessions, err
}

// fetchListing is listRoot's uncached read: the control child's session/list
// for root, bounded by listTimeout, normalised.
func (f *Fleet) fetchListing(ctx context.Context, root string) ([]contract.SessionSummary, error) {
	child, err := f.controlChild(ctx)
	if err != nil {
		return nil, err
	}
	timeout := f.listTimeout
	if timeout <= 0 {
		timeout = listTimeoutDefault
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := child.Request(lctx, "session/list", map[string]string{"cwd": root})
	if err != nil {
		return nil, err
	}
	return normaliseListing(raw, root)
}

// listingItem is one item of a session/list answer, as the child sends it.
type listingItem struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Meta      struct {
		MessageCount int `json:"messageCount"`
	} `json:"_meta"`
}

// normaliseListing turns a session/list answer into the rows the bridge
// reports: updatedAt from RFC3339 to Unix milliseconds, 0 when absent or
// unreadable, and messageCount from _meta.messageCount.
func normaliseListing(raw json.RawMessage, root string) ([]contract.SessionSummary, error) {
	var page struct {
		Sessions []listingItem `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("bridge: decode the session list for %s: %w", root, err)
	}
	out := make([]contract.SessionSummary, 0, len(page.Sessions))
	for _, item := range page.Sessions {
		summary := contract.SessionSummary{
			SessionID:    item.SessionID,
			Title:        item.Title,
			MessageCount: item.Meta.MessageCount,
		}
		if item.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339, item.UpdatedAt); err == nil {
				summary.UpdatedAt = t.UnixMilli()
			}
		}
		out = append(out, summary)
	}
	return out, nil
}

// dropListing forgets root's cached listing and every id it contributed;
// dropListings forgets every one, failures included.
func (f *Fleet) dropListing(root string) {
	root = filepath.Clean(root)
	f.listMu.Lock()
	defer f.listMu.Unlock()
	delete(f.listings, root)
	f.forgetRootSessionsLocked(root)
}

// forgetRootSessionsLocked drops every id the cache recorded for root, so a
// dropped or replaced listing leaves no id pointing at it and the map does not
// grow for the daemon's lifetime. The caller holds listMu.
func (f *Fleet) forgetRootSessionsLocked(root string) {
	for id, owner := range f.sessionRoots {
		if owner == root {
			delete(f.sessionRoots, id)
		}
	}
}

func (f *Fleet) dropListings() {
	f.listMu.Lock()
	defer f.listMu.Unlock()
	f.listings = nil
	f.sessionRoots = nil
}

// listClock is the listing cache's clock, so a test ages an entry without
// sleeping.
func (f *Fleet) listClock() time.Time {
	if f.listNow != nil {
		return f.listNow()
	}
	return time.Now()
}

// projectChildrenInRootOrder is projectChildSnapshot sorted by root, so a walk
// answers the same child for the same id on every call.
func (f *Fleet) projectChildrenInRootOrder() []*projectChild {
	children := f.projectChildSnapshot()
	sort.Slice(children, func(i, j int) bool { return children[i].root < children[j].root })
	return children
}

// SessionIndex builds GET /api/sessions/all's answer (P7 D7); root narrows it
// to that project. Every root's listing is read in parallel, each through
// listRoot's own bound, so one stalled child cannot hold the answer
// (decision 6). Only a root the fleet knows is listed: an unknown root is an
// index with no projects.
func (f *Fleet) SessionIndex(ctx context.Context, root string) contract.SessionIndex {
	index := contract.SessionIndex{Projects: []contract.ProjectSessions{}}
	roots := f.knownRoots()
	if root != "" {
		root = filepath.Clean(root)
		narrowed := make([]string, 0, 1)
		for _, known := range roots {
			if known == root {
				narrowed = append(narrowed, root)
			}
		}
		roots = narrowed
	}
	if len(roots) == 0 {
		return index
	}

	// The listings are fetched side by side: a root that never answers costs
	// its own timeout and no other root's.
	type rootListing struct {
		rows []contract.SessionSummary
		err  error
	}
	listings := make([]rootListing, len(roots))
	var wg sync.WaitGroup
	for i, r := range roots {
		wg.Add(1)
		go func(i int, r string) {
			defer wg.Done()
			rows, err := f.listRoot(ctx, r)
			listings[i] = rootListing{rows: rows, err: err}
		}(i, r)
	}
	wg.Wait()

	// One ownership map for the whole index: it is the same for every root.
	owned := f.ownedSessions()
	children := make(map[string]*projectChild)
	for _, pc := range f.projectChildSnapshot() {
		children[filepath.Clean(pc.root)] = pc
	}

	index.Projects = make([]contract.ProjectSessions, 0, len(roots))
	for i, r := range roots {
		group := contract.ProjectSessions{
			Root:     r,
			Sessions: make([]contract.SessionRow, 0, len(listings[i].rows)),
		}
		if listings[i].err != nil {
			group.Error = listings[i].err.Error()
		}
		named := make(map[string]contract.SessionSummary, len(listings[i].rows))
		for _, s := range listings[i].rows {
			named[s.SessionID] = s
		}

		// The open rows come first: what the root's live child is tracking
		// right now, minus the bridge's own sessions. A session the listing
		// names takes the listing's title and message count; one only the
		// registry knows falls back to when it was last active.
		open := make([]contract.SessionRow, 0)
		openIDs := make(map[string]bool)
		var reg *Registry
		if pc := children[r]; pc != nil {
			pc.mu.Lock()
			reg = pc.reg
			pc.mu.Unlock()
		}
		if reg != nil {
			for _, cs := range reg.clientSessions() {
				if _, isOwned := owned[cs.ID]; isOwned {
					continue
				}
				row := contract.SessionRow{
					SessionID: cs.ID,
					UpdatedAt: cs.LastActive.UnixMilli(),
					Open:      true,
					Viewers:   cs.Viewers,
					Busy:      cs.Busy,
				}
				if s, ok := named[cs.ID]; ok {
					row.Title = s.Title
					row.UpdatedAt = s.UpdatedAt
					row.MessageCount = s.MessageCount
				}
				if pending, ok := reg.PendingRequest(cs.ID); ok {
					p := pending
					row.Pending = &p
				}
				openIDs[cs.ID] = true
				open = append(open, row)
			}
		}
		sortNewestFirst(open)
		group.Sessions = append(group.Sessions, open...)

		// Then the closed ones: everything the listing names that is neither
		// open on the child nor the bridge's own.
		closed := make([]contract.SessionRow, 0, len(listings[i].rows))
		for _, s := range listings[i].rows {
			if openIDs[s.SessionID] {
				continue
			}
			if _, isOwned := owned[s.SessionID]; isOwned {
				continue
			}
			closed = append(closed, contract.SessionRow{
				SessionID:    s.SessionID,
				Title:        s.Title,
				UpdatedAt:    s.UpdatedAt,
				MessageCount: s.MessageCount,
			})
		}
		sortNewestFirst(closed)
		group.Sessions = append(group.Sessions, closed...)
		index.Projects = append(index.Projects, group)
	}
	return index
}

// sortNewestFirst orders rows by the updatedAt they report, newest first, ties
// broken by id so a listing reads the same on every call.
func sortNewestFirst(rows []contract.SessionRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt != rows[j].UpdatedAt {
			return rows[i].UpdatedAt > rows[j].UpdatedAt
		}
		return rows[i].SessionID < rows[j].SessionID
	})
}

// serveSessionIndex serves GET /api/sessions/all: every project's sessions,
// open and closed, narrowed by the root query parameter.
func (s *Server) serveSessionIndex(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge has no fleet"})
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.SessionIndex(r.Context(), r.URL.Query().Get("root")))
}

// followClientSession follows a client session on a project child, logging a
// failure as followAgentSession does: following is an observation, and it
// never fails the call that asked for it.
func (f *Fleet) followClientSession(ctx context.Context, reg *Registry, sessionID string) {
	if err := reg.Follow(ctx, sessionID); err != nil && !errors.Is(err, ErrUnsupported{Feature: "follow"}) {
		slog.Default().Warn("webbridge: follow a client session failed",
			"session", sessionID, "err", err)
	}
}
