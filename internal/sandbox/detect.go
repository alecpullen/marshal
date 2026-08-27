package sandbox

// DetectRuntime finds an available container runtime and pins its absolute
// path. cfg is "auto" (or empty) to probe docker then podman, or an explicit
// "docker"/"podman" to verify just that one. It returns the runtime name for
// user-facing messages, its absolute executable path pinned at detect time,
// and whether a usable runtime was found.
//
// Callers outside this package cannot import internal/sandbox's unexported
// probe; this is the supported entry point.
func DetectRuntime(cfg string) (name, absPath string, ok bool) {
	return detectRuntime(cfg)
}
