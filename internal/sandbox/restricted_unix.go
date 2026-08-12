//go:build !windows

package sandbox

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
)

var (
	errEmptyCommand = errors.New("sandbox: command is required")
	errEmptyDir     = errors.New("sandbox: working directory is required")
)

// restrictedResourceLimitsSupported reports whether the platform supports
// the ulimit-based caps (cpu/file-size/max-procs). darwin cannot limit
// address space (ulimit -v) but the other caps still apply, so this is true
// on all unix; memory caps simply report as 0 on darwin (see metaFor).
//
// This file is unix-only (see the build constraint), so the answer is always
// true here; the windows build supplies its own version returning false.
func restrictedResourceLimitsSupported() bool {
	return true
}

// ulimitSupportsMem reports whether address-space limits (ulimit -v) work on
// this unix. Disabled on darwin, where the flag is unsupported.
func ulimitSupportsMem() bool {
	return runtime.GOOS != "darwin"
}

// ulimitBlockSize reports the byte size of a single `ulimit -f` block on
// the current unix. Linux uses 1024-byte blocks; BSD/darwin uses 512-byte
// blocks. See setrlimit(2) RLIMIT_FSIZE.
func ulimitBlockSize() int {
	if runtime.GOOS == "linux" {
		return 1024
	}
	return 512
}

// restrictedWrapCommand wraps a command in ulimit caps the restricted config
// requested. Only positive limits are applied; zero means "unset". The
// resulting script is `/bin/sh -lc`-runnable.
//
//	cpu_seconds      -> ulimit -t <s>
//	file_size_limit  -> ulimit -f <blocks>  (blocks = bytes / ulimitBlockSize)
//	max_processes    -> ulimit -u <n>
//	memory_limit_mb  -> ulimit -v <kb>      (darwin: unsupported, skipped)
//
// The command is appended as-is, with no `exec` prefix. An earlier version
// emitted `exec <command>` to save the wrapper shell process, but `exec`
// binds tighter than the shell's list operators, so it silently truncated
// every compound command: `exec a; b` ran only `a` and still exited 0, and
// `exec cd x && y` failed outright with "exec: cd: not found" because exec
// cannot run a builtin. Dropping it costs one shell process, which
// terminateProcessTree already reaps via the process group.
//
// Everything is written to the same strings.Builder to avoid heap-escape
// and post-concat allocations.
func restrictedWrapCommand(command string, cfg Config) string {
	if cfg.CPUSeconds == 0 && cfg.MaxProcesses == 0 && cfg.FileSizeLimitMB == 0 &&
		!(cfg.MemoryLimitMB > 0 && ulimitSupportsMem()) {
		return command
	}
	var pre strings.Builder
	if cfg.CPUSeconds > 0 {
		pre.WriteString("ulimit -t ")
		pre.WriteString(strconv.Itoa(cfg.CPUSeconds))
		pre.WriteString("; ")
	}
	if cfg.FileSizeLimitMB > 0 {
		blockSize := ulimitBlockSize()
		blocks := (cfg.FileSizeLimitMB * 1024 * 1024) / blockSize
		pre.WriteString("ulimit -f ")
		pre.WriteString(strconv.Itoa(blocks))
		pre.WriteString("; ")
	}
	if cfg.MaxProcesses > 0 {
		pre.WriteString("ulimit -u ")
		pre.WriteString(strconv.Itoa(cfg.MaxProcesses))
		pre.WriteString("; ")
	}
	if cfg.MemoryLimitMB > 0 && ulimitSupportsMem() {
		pre.WriteString("ulimit -v ")
		pre.WriteString(strconv.Itoa(cfg.MemoryLimitMB * 1024))
		pre.WriteString("; ")
	}
	pre.WriteString(command)
	return pre.String()
}
