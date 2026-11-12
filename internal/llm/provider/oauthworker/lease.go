package oauthworker

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LeaseFile is the lock file, in the data dir, held by the one process that
// refreshes OAuth tokens. One holder at a time keeps the TUI, a fleet agent
// and a webbridge child from refreshing the same token concurrently.
const LeaseFile = "oauth-refresh.lock"

// TryLease tries to take the refresh lease in dataDir without blocking.
//
// held is false, with a nil error, when another holder has it. Any other
// failure (the file cannot be opened, or the platform has no lock binding) is
// returned as err, and the caller decides whether to refresh without the
// lease. release is non-nil only when held is true; it is safe to call more
// than once.
func TryLease(dataDir string) (release func(), held bool, err error) {
	// A first run may have no data dir yet; the lease goes in one either way.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, false, fmt.Errorf("create oauth lease dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dataDir, LeaseFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open oauth lease: %w", err)
	}
	ok, err := tryFlock(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fmt.Errorf("lock oauth lease: %w", err)
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
