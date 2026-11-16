package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/web/bridge/contract"
)

// listPulls is GET /api/pulls: every open record with its checks, review
// summary and derived state. The state is the record's own, derived from the
// evidence on its current head.
func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pulls": s.fleet.pulls()})
}

// getPull is GET /api/pulls/{id}: one record, with each failing check's log
// tail and the origin's narrated report when it has one.
func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.fleet.pull(id); !ok {
		writeErr(w, fmt.Errorf("%w: %s", ErrUnknownPull, id))
		return
	}
	writeJSON(w, http.StatusOK, s.fleet.pullDetail(r.Context(), id))
}

// pullDetail is one record with the evidence only the detail route carries: the
// tail of each failing check's log, and the narrated report when the origin has
// one. It is a live read of the forge, not a stored copy.
func (f *Fleet) pullDetail(ctx context.Context, id string) contract.PullRecord {
	rec, ok := f.pull(id)
	if !ok {
		return contract.PullRecord{}
	}
	out := rec
	out.Checks = append([]contract.PullCheck(nil), rec.Checks...)
	if rec.Unavailable == "" && rec.HeadSHA != "" {
		if repo, ok := f.ws.Repo(rec.RepoID); ok {
			if forge, cred, err := f.forgeFor(ctx, repo); err == nil {
				if failed, err := forge.ListFailedChecks(ctx, repo, rec.HeadSHA, cred); err == nil {
					for i := range out.Checks {
						if out.Checks[i].State != contract.CheckFailed {
							continue
						}
						check, ok := failedCheckNamed(failed, out.Checks[i].Name)
						if !ok {
							continue
						}
						log, err := forge.CheckLog(ctx, repo, check, cred)
						if err != nil {
							continue
						}
						out.Checks[i].LogTail = tailString(log, pullLogTailLimit)
					}
				}
			}
		}
	}
	if rec.Origin == contract.PullOriginFactory {
		out.Report = f.pullReport(rec)
	}
	return out
}

// failedCheckNamed finds one failed check by name.
func failedCheckNamed(failed []CheckInfo, name string) (CheckInfo, bool) {
	for _, c := range failed {
		if c.Name == name {
			return c, true
		}
	}
	return CheckInfo{}, false
}

// fixPull is POST /api/pulls/{id}/fix: it names a failing check, and optionally
// a note, a mode and a worker routing, and answers at once with the dispatch
// record. The work itself is asynchronous.
func (s *Server) fixPull(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	var req contract.PullFixRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid fix request: " + err.Error()})
			return
		}
	}
	rec, err := s.fleet.dispatchPullFix(r.Context(), r.PathValue("id"), req, DefaultOwnerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, rec)
}

// stopPullFix is POST /api/pulls/{id}/fix/stop: it stops the worker on the pull
// request and clears the fix.
func (s *Server) stopPullFix(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	rec, err := s.fleet.stopPullFix(r.Context(), r.PathValue("id"), DefaultOwnerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}
