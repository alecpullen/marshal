//go:build !windows

package sandbox

import (
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

var (
	errEmptyCommand = errors.New("sandbox: command is required")
	errEmptyDir     = errors.New("sandbox: working directory is required")
)

// restrictedResourceLimitsSupported reports whether the platform supports
// the ulimit-based caps (cpu/file-size/max-procs). darwin cannot limit
// address space (ulimit -v) but the other caps still apply, so this is true
// on all unix; memory caps simply report as 0 on darwin (see metaFor).
func restrictedResourceLimitsSupported() bool {
	return runtime.GOOS != "windows"
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
// Everything including the `exec <command>` tail is written to the same
// strings.Builder to avoid heap-escape and post-concat allocations.
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
	pre.WriteString("exec ")
	pre.WriteString(command)
	return pre.String()
}

// applyProcmgmt makes a command killable as a process group: Setpgid places
// the child (and its descendants) in a fresh group, and cmd.Cancel is set so
// that when ctx fires CommandContext kills the ENTIRE group (including any
// grandchildren) instead of just the direct child — fixing the orphaned-
// grandchild gap that plain CommandContext leaves.
func applyProcmgmt(cmd *exec.Cmd) {
	if cmd.Process != nil {
		return // too late: already started
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid signals the whole group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
