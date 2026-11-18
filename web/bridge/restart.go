package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The restart sweep's bounds (P6 B4).
const (
	restartSweepInterval = 30 * time.Second
	restartStateTimeout  = 5 * time.Second
	restartQuiet         = 2 * time.Minute
	buildCheckTimeout    = 10 * time.Second
	waitingForQuiet      = "a quiet moment"
)

// recycleTimeout bounds a restart's wait for its new generation (decision 7).
// A var so a test can shorten it, as restartBackoff is.
var recycleTimeout = 60 * time.Second

// projectState is project/state's answer as the bridge reads it.
type projectState struct {
	Stale   []string `json:"stale"`
	Busy    []string `json:"busy"`
	Version string   `json:"version"`
}

// RunRestartSweep runs SweepRestarts every restartSweepInterval until ctx
// ends or the fleet closes.
func (f *Fleet) RunRestartSweep(ctx context.Context) {
	ticker := time.NewTicker(restartSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.done:
			return
		case <-ticker.C:
			f.SweepRestarts(ctx, time.Now())
		}
	}
}

// SweepRestarts checks every ready project child once, restarts each stale
// one that is idle at now, and returns when those restarts are done
// (decision 6).
func (f *Fleet) SweepRestarts(ctx context.Context, now time.Time) {
	// One sweep at a time: a restart's own wait must not be walked over by a
	// second sweep that saw the same child stale.
	f.sweepMu.Lock()
	defer f.sweepMu.Unlock()

	for _, pc := range f.projectChildSnapshot() {
		pc.mu.Lock()
		child, reg, ready, recycling := pc.child, pc.reg, pc.ready, pc.recycling
		pc.mu.Unlock()
		if child == nil || reg == nil || !ready || recycling != nil {
			continue
		}

		state, err := f.askProjectState(ctx, pc)
		if err != nil {
			// The child's last view is left as it was: a state the bridge
			// could not read says nothing about the build on disk.
			slog.Default().Warn("webbridge: project state failed", "root", pc.root, "err", err)
			continue
		}
		if len(state.Stale) == 0 {
			pc.mu.Lock()
			pc.restart = nil
			pc.mu.Unlock()
			continue
		}

		// What holds the restart back: a busy child, or a child someone is
		// looking at that was active within the quiet window (decision 3).
		waitingFor := ""
		switch {
		case len(state.Busy) > 0:
			waitingFor = strings.Join(state.Busy, ", ")
		case !reg.quietSince(now.Add(-restartQuiet)):
			waitingFor = waitingForQuiet
		}
		pc.mu.Lock()
		// The refused build's sentence is kept beside the file it describes,
		// so a sweep that saw the child current in between still reports why
		// the build on disk was refused (decision 8).
		refused := pc.refusedText
		pc.restart = &DaemonRestart{Reasons: state.Stale, WaitingFor: waitingFor, Refused: refused}
		pc.mu.Unlock()
		if waitingFor != "" {
			continue
		}

		// The new build is checked before the note, so a restart that would
		// spawn a binary that cannot start is refused instead (decision 9).
		bin, err := exec.LookPath(f.marshalBin)
		if err != nil {
			slog.Default().Warn("webbridge: resolve the marshal binary",
				"root", pc.root, "bin", f.marshalBin, "err", err)
			continue
		}
		info, err := os.Stat(bin)
		if err != nil {
			slog.Default().Warn("webbridge: stat the marshal binary",
				"root", pc.root, "bin", bin, "err", err)
			continue
		}
		pc.mu.Lock()
		refusedInfo := pc.refused
		pc.mu.Unlock()
		if sameBinary(refusedInfo, info) {
			// This exact binary already failed its check: a restart would
			// spawn it, so nothing restarts this child for any reason until
			// the file's identity changes (decision 8).
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, buildCheckTimeout)
		version, err := f.checkNewBuild(cctx, bin)
		cancel()
		if err != nil {
			line := headerLine(err.Error())
			pc.mu.Lock()
			pc.refused, pc.refusedText = info, line
			pc.restart = &DaemonRestart{Reasons: state.Stale, Refused: line}
			pc.mu.Unlock()
			staysOn := state.Version
			if staysOn == "" {
				staysOn = "its current build"
			}
			f.sendProjectNote(ctx, pc, fmt.Sprintf(
				"A new marshal build could not start (%s); this project stays on %s.", line, staysOn))
			slog.Default().Warn("webbridge: new marshal build could not start",
				"root", pc.root, "bin", bin, "err", line)
			continue
		}
		pc.mu.Lock()
		pc.refused, pc.refusedText = nil, ""
		if pc.restart != nil && pc.restart.Refused != "" {
			// The build that could not start is gone or fixed, so its text
			// no longer describes anything.
			pc.restart = &DaemonRestart{Reasons: pc.restart.Reasons, WaitingFor: pc.restart.WaitingFor}
		}
		pc.mu.Unlock()

		// Busy is asked again, right before the note, so the window a prompt
		// can start in is the time a note takes (decision 2).
		again, err := f.askProjectState(ctx, pc)
		if err != nil {
			slog.Default().Warn("webbridge: project state before a restart failed",
				"root", pc.root, "err", err)
			continue
		}
		if len(again.Busy) > 0 {
			waitingFor := strings.Join(again.Busy, ", ")
			pc.mu.Lock()
			if pc.restart != nil {
				pc.restart = &DaemonRestart{Reasons: pc.restart.Reasons, WaitingFor: waitingFor}
			}
			pc.mu.Unlock()
			continue
		}

		f.recycleProjectChild(ctx, pc, state, version)
	}

	// The bridge's own binary is checked last, still under sweepMu: every
	// child has been walked, so a child that is mid-restart holds this back
	// rather than being restarted under (decisions 3 and 4).
	f.sweepOwnRestart(ctx, now)
}

// askProjectState asks pc's child for its project/state, bounded by
// restartStateTimeout.
func (f *Fleet) askProjectState(ctx context.Context, pc *projectChild) (projectState, error) {
	pc.mu.Lock()
	child := pc.child
	pc.mu.Unlock()
	if child == nil {
		return projectState{}, fmt.Errorf("bridge: project child for %s is not running", pc.root)
	}
	sctx, cancel := context.WithTimeout(ctx, restartStateTimeout)
	defer cancel()
	raw, err := child.Request(sctx, "project/state", nil)
	if err != nil {
		return projectState{}, err
	}
	var st projectState
	if err := json.Unmarshal(raw, &st); err != nil {
		return projectState{}, fmt.Errorf("bridge: decode project/state for %s: %w", pc.root, err)
	}
	return st, nil
}

// sendProjectNote tells every live session on pc's child text, bounded by
// restartStateTimeout. A failure is logged rather than returned: a note is a
// courtesy, and a child that cannot take one can still restart.
func (f *Fleet) sendProjectNote(ctx context.Context, pc *projectChild, text string) {
	pc.mu.Lock()
	child := pc.child
	pc.mu.Unlock()
	if child == nil {
		return
	}
	nctx, cancel := context.WithTimeout(ctx, restartStateTimeout)
	defer cancel()
	if _, err := child.Request(nctx, "project/note", map[string]any{"text": text}); err != nil {
		slog.Default().Warn("webbridge: project note failed", "root", pc.root, "err", err)
	}
}

// recycleProjectChild restarts pc's child on the new build: one note while the
// old process is still up (decision 1), the announcement to its live viewers,
// then the recycle itself (decision 4).
func (f *Fleet) recycleProjectChild(ctx context.Context, pc *projectChild, state projectState, version string) {
	reasons := strings.Join(state.Stale, ", ")
	f.sendProjectNote(ctx, pc, "Restarting on "+version+": "+reasons+".")

	marker := make(chan struct{})
	pc.mu.Lock()
	child, reg := pc.child, pc.reg
	if child == nil {
		// The child went away between the sweep's checks and the note. There
		// is nothing to recycle, and opening the marker here would leave
		// every later attach waiting on a channel no one will close.
		pc.mu.Unlock()
		return
	}
	// The generation this restart means to end. Only a later generation
	// counts as the restart coming back: spawn bumps the counter before
	// OnRestart runs, so a handshake already in flight for an earlier
	// generation can never be mistaken for this one's (decision 7).
	ending := child.Generation()
	pc.recycling, pc.ready = marker, false
	pc.mu.Unlock()

	f.announceRestart(reg, reasons, version)

	if err := child.Recycle(); err != nil {
		f.dropProjectChild(pc, child, version, reasons, err)
		return
	}
	var failure error
	select {
	case <-marker:
		if child.Generation() <= ending {
			failure = fmt.Errorf("the marker closed on generation %d, not past %d", child.Generation(), ending)
		}
	case <-time.After(recycleTimeout):
		// The marker closing in the same moment the timeout fires is a
		// restart that did come back: read it under the lock rather than
		// acting on the race.
		pc.mu.Lock()
		closed := pc.recycling != marker
		pc.closeRecycleLocked()
		pc.mu.Unlock()
		if !closed {
			failure = fmt.Errorf("no new generation within %s", recycleTimeout)
		} else if child.Generation() <= ending {
			failure = fmt.Errorf("the marker closed on generation %d, not past %d", child.Generation(), ending)
		}
	case <-ctx.Done():
		// The bridge is going away: end the marker so nothing waits on it,
		// rather than hold the sweep for the rest of recycleTimeout.
		pc.mu.Lock()
		pc.closeRecycleLocked()
		pc.mu.Unlock()
		failure = fmt.Errorf("the bridge closed before the new generation came up: %w", ctx.Err())
	}
	if failure != nil {
		f.dropProjectChild(pc, child, version, reasons, failure)
		return
	}
	pc.mu.Lock()
	pc.restart = nil
	pc.mu.Unlock()
	slog.Default().Info("webbridge: restarted a project child",
		"root", pc.root, "version", version, "reasons", reasons)
}

// dropProjectChild gives up on a restart that did not come back (decision 7):
// the marker is closed so the next attach builds a fresh child, and this one
// is stopped and forgotten as a failed start is.
func (f *Fleet) dropProjectChild(pc *projectChild, child *Child, version, reasons string, err error) {
	slog.Default().Warn("webbridge: project child restart failed",
		"root", pc.root, "version", version, "reasons", reasons, "err", err,
		"stderr", child.StderrLog())
	pc.mu.Lock()
	pc.closeRecycleLocked()
	pc.mu.Unlock()
	pc.stop()
	f.forgetProjectChild(pc)
}

// checkNewBuild runs bin --version through f.checkBuild, or the binary when
// that is nil, and answers its version (decision 9).
func (f *Fleet) checkNewBuild(ctx context.Context, bin string) (string, error) {
	if f.checkBuild != nil {
		return f.checkBuild(ctx, bin)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "--version")
	// Separate streams, so a failure's first line is the stderr one when
	// there is one; the environment is the bridge's own.
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if line := headerLine(stderr.String()); line != "" {
			return "", errors.New(line)
		}
		if line := headerLine(stdout.String()); line != "" {
			return "", errors.New(line)
		}
		return "", errors.New(headerLine(err.Error()))
	}
	return buildVersionOf(stdout.String()), nil
}

// buildVersionOf is decision 9's rule over --version's standard output.
func buildVersionOf(stdout string) string {
	line := stdout
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if fields := strings.Fields(line); len(fields) >= 2 {
		return fields[1]
	}
	return line
}

// sameBinary reports whether a and b are the same build: the same file, with
// the same size and modification time (decision 8).
func sameBinary(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
