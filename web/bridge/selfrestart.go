package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"marshal/web/bridge/contract"
)

// daemonRestartPath is the socket's restart route (P6 B5).
const daemonRestartPath = "/api/daemon/restart"

// restartRequestedInterval is how often a requested restart sweeps (decision 4).
var restartRequestedInterval = 2 * time.Second

// The two reasons a self restart names (decision 3).
const (
	selfRestartNewBuild  = "a new webbridge build"
	selfRestartRequested = "a restart was asked for"
)

// errNoOwnBinary is decision 7's refusal: a daemon that never recorded its own
// executable cannot restart itself.
var errNoOwnBinary = errors.New("this daemon cannot restart itself")

// SelfRestart is the fleet's decision to re-execute the daemon (decision 3).
type SelfRestart struct {
	Path    string // the binary to execute
	Version string // what its --version reported
	Reason  string // "a new webbridge build" or "a restart was asked for"
}

// SetOwnBinary records the daemon's own executable for the sweep and the route.
func (f *Fleet) SetOwnBinary(path string) error {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f.sweepMu.Lock()
	defer f.sweepMu.Unlock()
	f.ownPath, f.ownInfo = path, info
	f.selfRestart = make(chan SelfRestart, 1)
	return nil
}

// SelfRestarts delivers at most one SelfRestart.
func (f *Fleet) SelfRestarts() <-chan SelfRestart {
	return f.selfRestart
}

// hasOwnBinary reports whether the daemon recorded its own executable.
func (f *Fleet) hasOwnBinary() bool {
	f.sweepMu.Lock()
	defer f.sweepMu.Unlock()
	return f.ownPath != ""
}

// ownRestart is the daemon's own pending restart for GET /api/daemon. It is a
// fresh value: a caller never shares the field a sweep may be replacing.
func (f *Fleet) ownRestart() *DaemonRestart {
	f.ownRestartMu.Lock()
	defer f.ownRestartMu.Unlock()
	if f.ownRestartState == nil {
		return nil
	}
	out := *f.ownRestartState
	if f.ownRestartState.Reasons != nil {
		out.Reasons = append([]string(nil), f.ownRestartState.Reasons...)
	}
	return &out
}

// setOwnRestart replaces the daemon's own pending restart; nil clears it. The
// value is copied, so nothing outside ownRestartMu can read a torn state.
func (f *Fleet) setOwnRestart(state *DaemonRestart) {
	f.ownRestartMu.Lock()
	defer f.ownRestartMu.Unlock()
	if state == nil {
		f.ownRestartState = nil
		return
	}
	copied := *state
	if state.Reasons != nil {
		copied.Reasons = append([]string(nil), state.Reasons...)
	}
	f.ownRestartState = &copied
}

// sweepOwnRestart is the unasked restart at the end of a sweep: a replaced
// webbridge restarts the daemon once every child is walked and nothing holds it
// (decisions 3 and 4). The caller holds sweepMu.
func (f *Fleet) sweepOwnRestart(ctx context.Context, now time.Time) {
	if f.ownPath == "" || f.selfFired {
		return
	}
	info, err := os.Stat(f.ownPath)
	if err != nil {
		// The file the daemon was started from is not readable right now, so
		// nothing is known about a new build: the state is left as it was.
		slog.Default().Warn("webbridge: stat its own binary", "path", f.ownPath, "err", err)
		return
	}
	if sameBinary(f.ownInfo, info) {
		// The daemon is running the build on disk.
		f.setOwnRestart(nil)
		return
	}
	if sameBinary(f.ownRefused, info) {
		// This exact build already failed its check, and restarting would
		// execute it: the refusal stands until the file changes (decision 9).
		return
	}

	holds := f.daemonHolds(ctx, now, true)
	state := &DaemonRestart{Reasons: []string{selfRestartNewBuild}}
	if len(holds) > 0 {
		state.WaitingFor = holds[0]
	}
	f.setOwnRestart(state)
	if len(holds) > 0 {
		return
	}
	f.fireSelfRestart(ctx, selfRestartNewBuild)
}

// daemonHolds lists what keeps the daemon from restarting at now: every
// project child's busy lines, the quiet lines when quiet is true, and the
// running agents (decision 1). The caller holds sweepMu.
func (f *Fleet) daemonHolds(ctx context.Context, now time.Time, quiet bool) []string {
	var lines []string
	seen := make(map[string]bool)
	add := func(line string) {
		if line == "" || seen[line] {
			return
		}
		seen[line] = true
		lines = append(lines, line)
	}

	children := f.projectChildSnapshot()
	// Root order, so the first hold a caller reports is the same one twice.
	sort.Slice(children, func(i, j int) bool { return children[i].root < children[j].root })
	for _, pc := range children {
		pc.mu.Lock()
		child, reg, ready, recycling, root := pc.child, pc.reg, pc.ready, pc.recycling, pc.root
		pc.mu.Unlock()
		if child == nil || !ready || recycling != nil {
			// A child that is not serving yet, or is being replaced, is not
			// something a restart would interrupt: the sweep skips it too.
			continue
		}
		// Asked fresh, and never with pc.mu held: this is a request.
		state, err := f.askProjectState(ctx, pc)
		if err != nil {
			add("project " + root + " did not answer")
			continue
		}
		for _, line := range state.Busy {
			add(line)
		}
		if quiet && reg != nil && !reg.quietSince(now.Add(-restartQuiet)) {
			add(waitingForQuiet)
		}
	}

	for _, a := range f.Snapshot() {
		name := a.Name
		if name == "" {
			name = a.ID
		}
		switch a.Status {
		case contract.AgentRunning:
			add("agent " + name + " is running")
		case contract.AgentAwaitingApproval, contract.AgentAwaitingQuestion:
			add("agent " + name + " is waiting for an answer")
		}
	}
	return lines
}

// fireSelfRestart checks the own binary, announces and sends; it answers the
// last progress line. The caller holds sweepMu.
func (f *Fleet) fireSelfRestart(ctx context.Context, reason string) DaemonRestartProgress {
	if f.selfFired {
		// The decision is already made: the same version is answered, and
		// nothing is announced or sent a second time.
		return DaemonRestartProgress{Restarting: true, Version: f.selfVersion}
	}

	cctx, cancel := context.WithTimeout(ctx, buildCheckTimeout)
	version, err := f.checkNewBuild(cctx, f.ownPath)
	cancel()
	if err != nil {
		line := headerLine(err.Error())
		// The file on disk is what failed, so the refusal is recorded against
		// it: no later sweep restarts on this build (decision 9).
		if info, statErr := os.Stat(f.ownPath); statErr == nil {
			f.ownRefused = info
		}
		state := &DaemonRestart{Reasons: []string{reason}, Refused: line}
		if current := f.ownRestart(); current != nil {
			state.Reasons, state.WaitingFor = current.Reasons, current.WaitingFor
		}
		f.setOwnRestart(state)
		slog.Default().Warn("webbridge: the new webbridge build could not start",
			"path", f.ownPath, "err", line)
		return DaemonRestartProgress{Error: "the new webbridge build could not start (" + line + ")"}
	}

	f.selfFired, f.selfVersion = true, version
	f.announceRestartAll(reason, version)
	// At most one decision per process, so a full channel means the decision
	// is already on its way; never block a sweep on a main that stopped
	// listening.
	select {
	case f.selfRestart <- SelfRestart{Path: f.ownPath, Version: version, Reason: reason}:
	default:
	}
	slog.Default().Info("webbridge: restarting the daemon", "version", version, "reason", reason)
	return DaemonRestartProgress{Restarting: true, Version: version}
}

// announceRestartAll is announceRestart for every tracked relay: every viewer
// of every child hears the same frame, because the restart ends them all.
func (f *Fleet) announceRestartAll(reason, version string) int {
	f.relayMu.Lock()
	regs := make([]*Registry, 0, len(f.relays))
	for reg := range f.relays {
		regs = append(regs, reg)
	}
	f.relayMu.Unlock()

	reached := 0
	for _, reg := range regs {
		reached += f.announceRestart(reg, reason, version)
	}
	return reached
}

// requestRestart serves one restart request until it fires, fails or ctx ends.
// Nothing is left behind when the caller goes away: the request withdraws by
// its context alone.
func (f *Fleet) requestRestart(ctx context.Context, now bool, progress func(DaemonRestartProgress)) error {
	if !f.hasOwnBinary() {
		return errNoOwnBinary
	}
	if now {
		f.sweepMu.Lock()
		line := f.fireSelfRestart(ctx, selfRestartRequested)
		f.sweepMu.Unlock()
		progress(line)
		return nil
	}
	for {
		f.sweepMu.Lock()
		holds := f.daemonHolds(ctx, time.Now(), false)
		if len(holds) == 0 {
			line := f.fireSelfRestart(ctx, selfRestartRequested)
			f.sweepMu.Unlock()
			progress(line)
			return nil
		}
		f.sweepMu.Unlock()
		// The line is written without sweepMu held: a slow client must not
		// hold up a sweep.
		progress(DaemonRestartProgress{WaitingFor: holds})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(restartRequestedInterval):
		}
	}
}

// restartDaemon serves POST /api/daemon/restart on the socket (P6 B5). The
// answer is newline-delimited JSON: one line per sweep, ending with the fire
// or its refusal.
func (s *Server) restartDaemon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "restart is a POST"})
		return
	}
	if s.fleet == nil || !s.fleet.hasOwnBinary() {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": errNoOwnBinary.Error()})
		return
	}

	// An empty body is {"now": false}: the request asks for a restart, not
	// necessarily this second.
	var req DaemonRestartRequest
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	progress := func(line DaemonRestartProgress) {
		data, err := json.Marshal(line)
		if err != nil {
			return
		}
		data = append(data, '\n')
		if _, err := w.Write(data); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := s.fleet.requestRestart(r.Context(), req.Now, progress); err != nil && !errors.Is(err, context.Canceled) {
		slog.Default().Warn("webbridge: restart request ended", "err", err)
	}
}
