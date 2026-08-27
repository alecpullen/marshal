package bridge

import (
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
