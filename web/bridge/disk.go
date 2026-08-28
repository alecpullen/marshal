package bridge

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// diskUsage is a snapshot of how much disk the state directory consumes,
// split between git mirrors (repos) and agent working trees (work).
type diskUsage struct {
	Repos      int64
	Work       int64
	Total      int64
	MeasuredAt time.Time
}

// measureDisk walks the repos and work subtrees of stateDir and sums the
// size of every file. A missing subtree contributes zero rather than
// erroring — a fresh install has neither, and the fleet UI must still be
// able to show a zero figure.
func measureDisk(stateDir string) (diskUsage, error) {
	var d diskUsage
	d.MeasuredAt = time.Now()

	repos, err := sumTree(filepath.Join(stateDir, "repos"))
	if err != nil {
		return d, err
	}
	work, err := sumTree(filepath.Join(stateDir, "work"))
	if err != nil {
		return d, err
	}
	d.Repos = repos
	d.Work = work
	d.Total = repos + work
	return d, nil
}

// sumTree returns the total size in bytes of every regular file under
// root. A root that does not exist contributes zero.
func sumTree(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			// A missing root is not an error; anything else is.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if de.Type().IsRegular() {
			info, ierr := de.Info()
			if ierr != nil {
				return ierr
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// diskUsage returns the cached disk usage for the fleet's state
// directory, measuring it on first call and after every invalidation.
func (f *Fleet) diskUsage() diskUsage {
	f.diskCacheMu.Lock()
	defer f.diskCacheMu.Unlock()
	if f.diskCacheOK {
		return f.diskCache
	}
	d, err := measureDisk(f.stateDir)
	if err != nil {
		// A measurement failure leaves the cache invalid so the next
		// call retries; return a zero snapshot rather than blocking the
		// fleet UI on an error.
		return diskUsage{MeasuredAt: time.Now()}
	}
	f.diskCache = d
	f.diskCacheOK = true
	return d
}

// invalidateDisk drops the cached disk usage so the next call to
// diskUsage re-measures. It is called after any prune or tree removal.
func (f *Fleet) invalidateDisk() {
	f.diskCacheMu.Lock()
	f.diskCacheOK = false
	f.diskCacheMu.Unlock()
}

// liveMirrors is the set of mirror directories some agent still depends
// on.
//
// Membership is by PERSISTED agent, not by live runtime: a paused agent
// has no runtime but its workspace and its history are still there, and
// pruning its mirror would strand it.
func (f *Fleet) liveMirrors() map[string]bool {
	live := make(map[string]bool)
	for _, a := range f.ws.Agents() {
		if a.SourceKind != "git" {
			continue
		}
		url := a.SourceRef
		if r, ok := f.ws.Repo(a.SourceRef); ok {
			url = r.URL
		}
		live[mirrorDir(f.stateDir, url)] = true
	}
	return live
}

// Prune removes state no agent depends on and reports the bytes
// reclaimed.
//
// It removes only two things: mirrors no persisted agent resolves to,
// and work directories whose agent is gone from the workspace. It never
// stops an agent and never removes a live agent's workspace — reclaiming
// a gigabyte is not worth destroying an hour of work.
func (f *Fleet) Prune() (int64, error) {
	live := f.liveMirrors()
	liveAgents := make(map[string]bool)
	for _, a := range f.ws.Agents() {
		liveAgents[a.ID] = true
	}

	var reclaimed int64

	// Prune unreferenced mirrors.
	reposDir := filepath.Join(f.stateDir, "repos")
	if entries, err := os.ReadDir(reposDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(reposDir, entry.Name())
			if live[dir] {
				continue
			}
			n, err := removeTree(dir)
			if err != nil {
				return reclaimed, fmt.Errorf("prune mirror %s: %w", dir, err)
			}
			reclaimed += n
		}
	}

	// Prune orphaned work directories.
	workDir := filepath.Join(f.stateDir, "work")
	if entries, err := os.ReadDir(workDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(workDir, entry.Name())
			agentID := entry.Name()
			if liveAgents[agentID] {
				continue
			}
			n, err := removeTree(dir)
			if err != nil {
				return reclaimed, fmt.Errorf("prune work dir %s: %w", dir, err)
			}
			reclaimed += n
		}
	}

	f.invalidateDisk()
	f.auditf(AuditEvent{Event: AuditPrune, Bytes: reclaimed})
	return reclaimed, nil
}

// removeTree removes a directory tree and returns the total size in
// bytes of the regular files it contained.
func removeTree(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, os.RemoveAll(path)
}
