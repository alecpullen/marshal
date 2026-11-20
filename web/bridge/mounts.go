package bridge

import (
	"fmt"
	"path/filepath"
)

// bindMount is a --mount argument binding source on the host at target.
//
// The spelling is the same for docker and podman, so no runtime name is
// threaded here. readonly appends ",readonly", which both accept.
func bindMount(source, target string, readonly bool) []string {
	spec := fmt.Sprintf("type=bind,source=%s,target=%s", source, target)
	if readonly {
		spec += ",readonly"
	}
	return []string{"--mount", spec}
}

// stateMount binds stateDir/subpath at target: a fleet's state is always a
// host directory, so every state mount is a bind of a path under it.
//
// readonly appends ",readonly", which both runtimes accept.
func stateMount(stateDir, target, subpath string, readonly bool) []string {
	return bindMount(filepath.Join(stateDir, subpath), target, readonly)
}
