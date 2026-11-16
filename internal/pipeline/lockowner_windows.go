//go:build windows

package pipeline

// pidAlive reports whether pid names a running process. known is false when
// the platform cannot answer, in which case the caller must treat the owner
// as unknown rather than dead.
//
// Windows cannot answer this question, so every same-run lock there is
// "unknown" and is never taken over automatically — the conservative
// direction: a crashed run needs its lock removed by hand rather than a live
// owner being displaced.
func pidAlive(pid int) (alive bool, known bool) {
	return false, false
}
