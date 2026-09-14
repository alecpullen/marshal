package bridge

import (
	"net/http"
	"time"
)

// diskStatusResponse is the wire shape of GET /api/disk. The field
// names are a contract with the SPA: the fleet UI is built against
// them, not against the internal diskUsage type, which carries no JSON
// tags. measuredAt is an RFC 3339 timestamp; budgetMB is the configured
// spawn-time disk budget in megabytes, 0 meaning unlimited.
type diskStatusResponse struct {
	Repos      int64     `json:"repos"`
	Work       int64     `json:"work"`
	Total      int64     `json:"total"`
	MeasuredAt time.Time `json:"measuredAt"`
	BudgetMB   int64     `json:"budgetMB"`
}

// pruneResponse is the wire shape of POST /api/prune. Warning is empty
// on a clean prune — omitempty keeps it off the wire so the happy-path
// body is exactly reclaimed and total — and carries the first failure
// when a prune only partly succeeded.
type pruneResponse struct {
	Reclaimed int64  `json:"reclaimed"`
	Total     int64  `json:"total"`
	Warning   string `json:"warning,omitempty"`
}

// requireFleet rejects requests when the server runs without a fleet.
//
// The disk endpoints are fleet-only: a bare registry server has no
// state directory, so there is nothing to measure and nothing to
// prune. The 503 is written directly rather than through writeErr
// because writeErr reserves 502 for child failures; 503 tells the SPA
// the capability is absent from this deployment, not that a resource
// is missing (404) or a child process broke (502).
func (s *Server) requireFleet(w http.ResponseWriter) bool {
	if s.fleet != nil {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable,
		map[string]string{"error": "disk endpoints require fleet mode"})
	return false
}

// diskStatus reports how much disk the state directory consumes, split
// between git mirrors (repos) and agent working trees (work), next to
// the spawn-time budget. The fleet caches the measurement; this
// handler never walks the tree itself.
func (s *Server) diskStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	u := s.fleet.diskUsage()
	writeJSON(w, http.StatusOK, diskStatusResponse{
		Repos:      u.Repos,
		Work:       u.Work,
		Total:      u.Total,
		MeasuredAt: u.MeasuredAt,
		BudgetMB:   s.fleet.limits.MaxDiskMB,
	})
}

// pruneDisk reclaims space from unreferenced mirrors and orphaned
// work directories and reports the fresh total.
//
// Audit is not the HTTP layer's to write: Fleet.Prune records the
// AuditPrune event, with the reclaimed byte count, on every exit
// path — partial failures included, not only clean runs.
func (s *Server) pruneDisk(w http.ResponseWriter, r *http.Request) {
	if !s.requireFleet(w) {
		return
	}
	reclaimed, err := s.fleet.Prune()
	// Prune now invalidates the disk cache itself on every exit path,
	// partial failures included. Invalidating here too is idempotent
	// belt-and-braces: it keeps the reported total honest even if
	// Prune's own invalidation is ever narrowed.
	s.fleet.invalidateDisk()
	total := s.fleet.diskUsage().Total
	if err != nil {
		if reclaimed > 0 {
			// Partial failure: space was reclaimed but a mirror or a
			// work dir survived. Follow the spawnAgent precedent —
			// report success with the data plus a warning rather than
			// 502 — so the SPA can still show what was reclaimed.
			writeJSON(w, http.StatusOK, pruneResponse{
				Reclaimed: reclaimed,
				Total:     total,
				Warning:   err.Error(),
			})
			return
		}
		// Nothing was reclaimed: the prune failed outright.
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pruneResponse{Reclaimed: reclaimed, Total: total})
}
