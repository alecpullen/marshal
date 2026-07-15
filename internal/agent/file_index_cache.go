package agent

import "sync"

// fileIndexCache memoises the (projectID, set of known paths) pair so
// extractPinnedFiles does not hit the DB on every steering-message drain.
// The zero value is ready to use — get returns ok=false until set is called.
type fileIndexCache struct {
	mu        sync.Mutex
	projectID int64
	paths     map[string]struct{}
	loaded    bool
}

// get returns the cached path-set for projectID, or nil if the cache is
// empty or stale.
func (c *fileIndexCache) get(projectID int64) (map[string]struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || c.projectID != projectID {
		return nil, false
	}
	return c.paths, true
}

// set stores a fresh path set for projectID, keyed for O(1) membership.
func (c *fileIndexCache) set(projectID int64, paths map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.projectID = projectID
	c.paths = paths
	c.loaded = true
}

// invalidate clears the cache. Called when the project changes.
func (c *fileIndexCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
	c.paths = nil
}
