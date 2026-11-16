//go:build windows

package hooks

import "os/exec"

// setHookProcessGroup cannot create a process group on Windows, so the hook
// tree is bounded by the direct child only — the same fallback
// web/bridge/projectenv_other.go uses.
func setHookProcessGroup(cmd *exec.Cmd) {}

// killHookProcessGroup falls back to killing the direct child.
func killHookProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
