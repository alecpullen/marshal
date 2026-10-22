// Package procenv sanitizes the environment marshal hands down to the
// processes it spawns.
package procenv

import (
	"os"
	"strings"
)

// mallocDebugKeys are macOS libmalloc's debugging knobs. libmalloc reads
// them in every process that inherits them, and each one makes that
// process print a diagnostic line to stderr — for example:
//
//	marshal(53398) MallocStackLogging: can't turn off malloc stack logging
//	because it was not enabled.
//
// Marshal spawns a child process per tool call, so one inherited knob
// becomes a stream of lines written straight to the terminal, which
// corrupts the TUI's rendering. None of them mean anything for a tool
// subprocess, so they are dropped from the environment marshal passes
// on. A user who wants one for a specific command can still set it on
// that command's own command line.
var mallocDebugKeys = []string{
	"MallocStackLogging",
	"MallocStackLoggingNoCompact",
	"MallocScribble",
	"MallocGuardEdges",
	"MallocErrorAbort",
	"MallocCorruptionAbort",
	"MallocCheckHeapStart",
	"MallocCheckHeapEach",
	"MallocCheckHeapAbort",
	"MallocNanoZone",
	"MallocLargeCache",
	"MallocMaxMagazines",
	"MallocRecirclemap",
	"MallocSpaceEfficient",
}

// ScrubMallocDebugEnv removes libmalloc's debugging knobs from this
// process's environment, so every child spawned afterwards inherits a
// clean one. Call it once, as early as possible in main.
//
// libmalloc reads these variables when a process starts, so this cannot
// undo a diagnostic marshal's own process already printed. It stops the
// noise from every process marshal spawns, which is where the volume
// comes from.
func ScrubMallocDebugEnv() {
	for _, key := range mallocDebugKeys {
		_ = os.Unsetenv(key)
	}
}

// ScrubMallocDebug returns env without libmalloc's debugging knobs. It
// is the slice form of ScrubMallocDebugEnv, for callers that build a
// child environment explicitly instead of inheriting this process's.
func ScrubMallocDebug(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && isMallocDebugKey(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// isMallocDebugKey reports whether key is one of the knobs, matching
// exactly: a lookalike such as MallocStackLoggingExtra is not ours to
// drop.
func isMallocDebugKey(key string) bool {
	for _, k := range mallocDebugKeys {
		if key == k {
			return true
		}
	}
	return false
}
