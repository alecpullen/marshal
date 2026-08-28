package bridge

import (
	"fmt"
	"path/filepath"
	"strings"
)

// volumeMount builds a --mount argument for one subpath of the shared
// state volume.
//
// The option name differs by runtime: docker spells it
// "volume-subpath", podman spells it "subpath" (podman-run(1)). Both
// mean the same thing, and getting it wrong fails at spawn rather than
// at build, so the runtime name is threaded here rather than guessed.
func volumeMount(runtime, volume, target, subpath string) []string {
	key := "volume-subpath"
	if runtime == "podman" {
		key = "subpath"
	}
	return []string{"--mount", fmt.Sprintf(
		"type=volume,source=%s,target=%s,%s=%s", volume, target, key, subpath)}
}

// ProjectMount maps a host path to the bridge's in-container view of it.
// A containerized bridge reads a checkout at its own mount point, but
// Docker resolves a bind-mount source against the daemon's view, so
// the bridge must hand Docker the host path, not its own.
type ProjectMount struct {
	Host      string
	Container string
}

// TranslateToHost converts a path as the bridge sees it into the path
// the container runtime needs.
//
// A bind-mount source is resolved by the daemon, not by the bridge, so
// passing the bridge's own view produces "mounts denied" at spawn. An
// undeclared path is refused here instead, where the error can name the
// flag that fixes it.
func TranslateToHost(mounts []ProjectMount, containerPath string) (string, error) {
	clean := filepath.Clean(containerPath)
	for _, m := range mounts {
		root := filepath.Clean(m.Container)
		// Compare on segment boundaries: "/host-projects-evil" must not
		// match a "/host-projects" root.
		if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
			continue
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			return "", err
		}
		return filepath.Join(m.Host, rel), nil
	}
	return "", fmt.Errorf("bridge: %s is not under any declared --project-mount root", containerPath)
}
