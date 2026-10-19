package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LeaseFile is the lock file, relative to a project root, that a process
// holds while it indexes that root. One holder at a time keeps two processes
// on one tree (the TUI and a fleet agent, or two agents) from scanning and
// embedding the same files twice.
const LeaseFile = ".marshal/index.lock"

// TryLease tries to take root's index lease without blocking.
//
// held is false, with a nil error, when another holder has the lease. Any
// other failure (the lock file cannot be opened, or the platform has no lock
// binding) is returned as err, and the caller decides whether to index
// without the lease. release is non-nil only when held is true; it is safe to
// call more than once.
func TryLease(root string) (release func(), held bool, err error) {
	f, err := os.OpenFile(filepath.Join(root, LeaseFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open index lease: %w", err)
	}
	ok, err := tryFlock(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("lock index lease: %w", err)
	}
	if !ok {
		_ = f.Close()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = releaseFlock(f)
			_ = f.Close()
		})
	}, true, nil
}
