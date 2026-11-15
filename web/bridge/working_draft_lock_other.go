//go:build !linux && !darwin

package bridge

// workingDraftOwnerAlive answers conservatively where this package has no
// liveness probe: an ownership lock is never reclaimed, so a lock left behind by
// an unclean exit still has to be removed by hand before the store reopens.
func workingDraftOwnerAlive(pid int) bool {
	return true
}
