package app

import (
	"sync"
	"time"

	"marshal/internal/app/config"
)

// Hold holds root's project host open with no session (P4 D9). The host is
// opened if it is not, its index work starts as a session's attach starts it,
// and its watcher runs while any hold is taken, whatever cfg's indexing.watch
// says. cfg becomes the host's config only when no session has given it one.
// The release is safe to call more than once; the last release lets the host
// linger as a session's does.
func (hs *Hosts) Hold(root string, cfg config.Config) (release func(), err error) {
	h, err := hs.acquire(root, time.Now())
	if err != nil {
		return nil, err
	}
	projectID, err := h.db.GetOrCreateProject(root, cfg.Project.Name)
	if err != nil {
		h.release()
		return nil, err
	}
	h.hold(cfg, projectID)

	var once sync.Once
	return func() {
		once.Do(func() {
			h.unhold()
			h.release()
		})
	}, nil
}

// hold registers one hold and, when no session has attached, makes cfg the
// host's config and projectID its project, then refreshes the index work.
func (h *ProjectHost) hold(cfg config.Config, projectID int64) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.holds++
	if !h.attached {
		h.attached = true
		h.cfg = cfg
		h.projectID = projectID
	}
	h.mu.Unlock()
	h.refreshIndex()
}

// unhold drops one hold and refreshes the index work.
func (h *ProjectHost) unhold() {
	h.mu.Lock()
	if h.holds > 0 {
		h.holds--
	}
	h.mu.Unlock()
	h.refreshIndex()
}
