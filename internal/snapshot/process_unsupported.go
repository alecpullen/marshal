//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

import (
	"os/exec"
)

// processTree is unavailable on this platform: there is no implementation here
// that can prove a child's descendants have stopped, so managed subprocesses
// fail closed rather than run with a weaker lifetime guarantee.
type processTree struct{}

func newProcessTree(*exec.Cmd) (*processTree, error) {
	return nil, storeErrorf(ReasonUnsupportedPlatform, "process-tree lifetime management")
}

func (t *processTree) afterStart(int) error { return nil }
func (t *processTree) kill() error          { return nil }
func (t *processTree) waitGone() error      { return nil }
func (t *processTree) close()               {}
