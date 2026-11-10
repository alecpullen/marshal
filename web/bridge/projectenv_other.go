//go:build !linux && !darwin

package bridge

import "os/exec"

// configureEnvStep cannot reach a process group on this system, so it bounds
// the wait after the kill with WaitDelay alone (decision 2).
func configureEnvStep(cmd *exec.Cmd) {
	cmd.WaitDelay = envKillWaitDelay
}
