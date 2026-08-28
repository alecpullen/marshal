package bridge

import "fmt"

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
