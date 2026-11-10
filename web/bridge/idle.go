package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

const (
	idleSweepInterval = 30 * time.Second
	idleStatusTimeout = 5 * time.Second
)

// SetIdleSessionTimeout sets how long a project session with no viewer and
// no run lives on (P4 D7a); zero never ends one.
func (f *Fleet) SetIdleSessionTimeout(d time.Duration) { f.idleTimeout = d }

// RunIdleSweep sweeps every idleSweepInterval until ctx ends or the fleet
// closes. It returns at once when the timeout is zero.
func (f *Fleet) RunIdleSweep(ctx context.Context) {
	if f.idleTimeout <= 0 {
		return
	}
	ticker := time.NewTicker(idleSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.done:
			return
		case <-ticker.C:
			f.sweepIdleSessions(ctx, time.Now())
		}
	}
}

// sweepIdleSessions closes every project session idle past the timeout at
// now, and returns the IDs it closed.
//
// A session is only closed once its own status has been read and says it is
// idle (decision 8): a status request that fails skips the session rather
// than closing one the bridge could not read.
func (f *Fleet) sweepIdleSessions(ctx context.Context, now time.Time) []string {
	timeout := f.idleTimeout
	if timeout <= 0 {
		return nil
	}
	cutoff := now.Add(-timeout)
	var closed []string
	for _, pc := range f.projectChildSnapshot() {
		pc.mu.Lock()
		child, reg := pc.child, pc.reg
		pc.mu.Unlock()
		if child == nil || reg == nil {
			continue
		}
		for _, id := range reg.idleCandidates(cutoff) {
			if !f.sessionIsIdle(ctx, child, pc.root, id, cutoff) {
				continue
			}
			ok, err := reg.closeIfIdle(ctx, id, cutoff)
			if err != nil {
				slog.Default().Warn("webbridge: close idle session failed",
					"root", pc.root, "session", id, "err", err)
				continue
			}
			if !ok {
				continue
			}
			slog.Default().Info("webbridge: closed idle session",
				"root", pc.root, "session", id, "reason", "idle")
			closed = append(closed, id)
		}
		// The child's last session may be the one just closed. An attach
		// cancels the linger, so put it back for a child left with nothing to
		// keep it alive (decision 8).
		if reg.count() == 0 {
			f.rearmLinger(pc)
		}
	}
	return closed
}

// sessionIsIdle asks the child for one session's status and reports whether
// the session is idle at cutoff: no run in flight, and its last finished run
// ended at or before the cutoff. A status request that fails, or answers
// something unreadable, reports false — never true — so a session the bridge
// could not read is kept.
func (f *Fleet) sessionIsIdle(ctx context.Context, child *Child, root, id string, cutoff time.Time) bool {
	sctx, cancel := context.WithTimeout(ctx, idleStatusTimeout)
	defer cancel()
	raw, err := child.Request(sctx, "session/status", map[string]string{"sessionId": id})
	if err != nil {
		slog.Default().Warn("webbridge: idle session status failed",
			"root", root, "session", id, "err", err)
		return false
	}
	// Only these two fields are read: the rest of a status is large and the
	// sweep needs nothing else from it.
	var st struct {
		Run     json.RawMessage `json:"run"`
		LastRun struct {
			EndedAt int64 `json:"endedAt"` // Unix milliseconds
		} `json:"lastRun"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		slog.Default().Warn("webbridge: decode idle session status failed",
			"root", root, "session", id, "err", err)
		return false
	}
	if len(st.Run) > 0 && string(st.Run) != "null" {
		return false
	}
	if st.LastRun.EndedAt > cutoff.UnixMilli() {
		return false
	}
	return true
}

// projectChildSnapshot copies the fleet's project children, so a sweep never
// holds projMu while a child's own locks are taken. A nil map (a closing
// fleet) yields nothing.
func (f *Fleet) projectChildSnapshot() []*projectChild {
	f.projMu.Lock()
	defer f.projMu.Unlock()
	out := make([]*projectChild, 0, len(f.projects))
	for _, pc := range f.projects {
		out = append(out, pc)
	}
	return out
}
