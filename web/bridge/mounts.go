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
//
// An empty stateDir is refused rather than joined: filepath.Join("", "work/a1")
// is a relative source, which the runtime would resolve wherever it likes.
// Production always has one (NewFleet guarantees Fleet.stateDir), so this
// catches the zero-value ContainerConfig instead of mounting the wrong tree.
func stateMount(stateDir, target, subpath string, readonly bool) []string {
	if stateDir == "" {
		panic("bridge: state mount with an empty state dir; every state subpath binds under the fleet's state directory")
	}
	return bindMount(filepath.Join(stateDir, subpath), target, readonly)
}
