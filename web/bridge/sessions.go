package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
		if pc.reg == nil || !pc.reg.tracksClient(id) {
			continue
		}
		return ResolvedSession{Reg: pc.reg, Log: pc.log, SessionID: id, Root: pc.root}, nil
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

	f.listMu.Lock()
	if f.listings == nil {
		f.listings = make(map[string]listingEntry)
	}
	f.listings[root] = listingEntry{sessions: sessions, err: err, readAt: f.listClock()}
	if err == nil {
		if f.sessionRoots == nil {
			f.sessionRoots = make(map[string]string)
		}
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

// dropListing forgets root's cached listing; dropListings forgets every one,
// failures included.
func (f *Fleet) dropListing(root string) {
	root = filepath.Clean(root)
	f.listMu.Lock()
	defer f.listMu.Unlock()
	delete(f.listings, root)
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

// followClientSession follows a client session on a project child, logging a
// failure as followAgentSession does: following is an observation, and it
// never fails the call that asked for it.
func (f *Fleet) followClientSession(ctx context.Context, reg *Registry, sessionID string) {
	if err := reg.Follow(ctx, sessionID); err != nil && !errors.Is(err, ErrUnsupported{Feature: "follow"}) {
		slog.Default().Warn("webbridge: follow a client session failed",
			"session", sessionID, "err", err)
	}
}
