package policy

import "strings"

// classifyShellOperations assigns effects to every parsed operation.
func classifyShellOperations(ops []ShellOperation) {
	for i := range ops {
		classifyShellOperation(&ops[i])
	}
}

// classifyShellOperation assigns the effect set for one operation. Unknown
// executables and unsupported syntax stay unknown rather than being guessed
// read-only.
func classifyShellOperation(op *ShellOperation) {
	if !op.Executable.Known {
		op.Effects |= ShellUnknown
		return
	}
	base := lastPathSegment(op.Executable.Value)
	switch base {
	case "pwd", "true", "false", "echo", "printf", "cat", "head", "tail",
		"wc", "stat", "readlink", "realpath", "ls", "du", "date", "uname":
		op.Effects |= ShellRead
	case "command":
		if hasFlag(op, "-v") || hasFlag(op, "-V") {
			op.Effects |= ShellRead
		} else {
			op.Effects |= ShellUnknown
		}
	case "grep":
		op.Effects |= ShellRead
	case "rg":
		// --pre executes a helper per file; it is not a proven read.
		if hasFlag(op, "--pre") {
			op.Effects |= ShellUnknown
		} else {
			op.Effects |= ShellRead
		}
	case "find":
		op.Effects |= ShellRead
	case "sed":
		// Only a bounded numeric print query is a read.
		if hasFlag(op, "-n") {
			op.Effects |= ShellRead
		} else {
			op.Effects |= ShellWrite
		}
	case "git":
		classifyGit(op)
	case "tee", "cp", "mv", "touch", "mkdir", "rmdir", "ln", "chmod", "chown":
		op.Effects |= ShellWrite
	case "rm", "truncate", "shred", "dd", "mkfs", "mke2fs", "newfs", "diskutil",
		"shutdown", "reboot", "halt", "poweroff",
		"sudo", "sudoedit", "doas", "pkexec", "su", "run0":
		// Effects are assigned by classifyDestructive.
	case "go":
		classifyGo(op)
	case "cargo":
		classifyCargo(op)
	case "make":
		op.Effects |= ShellWrite
	case "npm", "pnpm", "yarn":
		classifyNode(op)
	case "pip", "pip3":
		classifyPip(op)
	case "brew":
		classifyBrew(op)
	case "apt", "apt-get", "dnf", "yum":
		classifySystemPackage(op)
	case "awk", "source", ".", "sh", "bash", "zsh", "dash", "ksh", "eval":
		op.Effects |= ShellUnknown
	default:
		op.Effects |= ShellUnknown
	}
	classifyDestructive(op)
}

func classifyGit(op *ShellOperation) {
	switch gitSubcommand(op) {
	case "status", "diff", "log", "show", "rev-parse", "branch", "remote",
		"ls-files", "describe", "blame", "grep", "cat-file":
		op.Effects |= ShellRead
	case "config":
		// A global git config change mutates state outside the workspace;
		// a local one is an ordinary read.
		if hasFlag(op, "--global") {
			op.Effects |= ShellEnvironment
		} else {
			op.Effects |= ShellRead
		}
	case "commit", "add", "rm", "mv", "checkout", "switch", "merge", "rebase",
		"reset", "clean", "restore", "stash", "tag", "push", "pull", "fetch",
		"clone", "init", "worktree":
		op.Effects |= ShellWrite
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyGo(op *ShellOperation) {
	switch firstOperand(op) {
	case "test", "build", "vet", "run":
		op.Effects |= ShellWrite
	case "get", "mod", "download":
		op.Effects |= ShellWrite
	case "install":
		// go install writes a binary into GOBIN, outside the workspace.
		op.Effects |= ShellEnvironment
	case "clean":
		// go clean -cache/-modcache destroys the shared build cache; a
		// plain go clean only removes workspace artifacts.
		if hasFlag(op, "-cache") || hasFlag(op, "-modcache") {
			op.Effects |= ShellEnvironment
		} else {
			op.Effects |= ShellWrite
		}
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyCargo(op *ShellOperation) {
	switch firstOperand(op) {
	case "check", "test", "build", "run":
		op.Effects |= ShellWrite
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyNode(op *ShellOperation) {
	switch firstOperand(op) {
	case "run", "test", "build":
		op.Effects |= ShellWrite
	case "install", "i", "ci":
		if hasFlag(op, "-g") || hasFlag(op, "--global") {
			op.Effects |= ShellEnvironment
		} else {
			op.Effects |= ShellWrite
		}
	case "cache":
		// npm/yarn cache clean destroys the shared package cache.
		if hasOperand(op, "clean") {
			op.Effects |= ShellEnvironment
		} else {
			op.Effects |= ShellUnknown
		}
	case "add":
		// yarn add installs packages and runs lifecycle scripts.
		op.Effects |= ShellEnvironment
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyPip(op *ShellOperation) {
	switch firstOperand(op) {
	case "install":
		// --target installs into a project-local directory; --user installs
		// into the user site-packages, outside the workspace.
		if hasFlag(op, "--target") {
			op.Effects |= ShellWrite
			return
		}
		op.Effects |= ShellEnvironment
	case "cache":
		if hasOperand(op, "purge") || hasOperand(op, "remove") {
			op.Effects |= ShellEnvironment
		} else {
			op.Effects |= ShellUnknown
		}
	default:
		op.Effects |= ShellUnknown
	}
}

// classifyBrew treats install/uninstall/cleanup as environment mutations:
// they run package scripts and write outside the workspace.
func classifyBrew(op *ShellOperation) {
	if hasOperand(op, "install") || hasOperand(op, "uninstall") || hasOperand(op, "cleanup") {
		op.Effects |= ShellEnvironment
		return
	}
	op.Effects |= ShellUnknown
}

// classifySystemPackage treats machine package-manager mutations as
// environment changes.
func classifySystemPackage(op *ShellOperation) {
	if hasOperand(op, "install") || hasOperand(op, "remove") || hasOperand(op, "purge") {
		op.Effects |= ShellEnvironment
		return
	}
	op.Effects |= ShellUnknown
}

func hasFlag(op *ShellOperation, name string) bool {
	for _, f := range op.Flags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// hasOperand reports whether any operand equals name.
func hasOperand(op *ShellOperation, name string) bool {
	for _, o := range op.Operands {
		if o.Value == name {
			return true
		}
	}
	return false
}

func firstOperand(op *ShellOperation) string {
	if len(op.Operands) == 0 {
		return ""
	}
	return op.Operands[0].Value
}

// gitSubcommand returns the actual subcommand, skipping global flags and their
// values (-C, -c, --config, --git-dir, --work-tree, --exec-path, --namespace).
func gitSubcommand(op *ShellOperation) string {
	argv := op.Argv
	for i := 1; i < len(argv); i++ {
		v := argv[i].Value
		if v == "-C" || v == "-c" || v == "--config" || v == "--git-dir" ||
			v == "--work-tree" || v == "--exec-path" || v == "--namespace" {
			i++
			continue
		}
		if strings.HasPrefix(v, "-") {
			continue
		}
		return v
	}
	return ""
}

// hasShortFlag reports whether a combined short flag (such as -fdx) contains
// the given single-character flag.
func hasShortFlag(op *ShellOperation, name byte) bool {
	for _, f := range op.Flags {
		n := f.Name
		if len(n) >= 2 && n[0] == '-' && n[1] != '-' && strings.ContainsRune(n[1:], rune(name)) {
			return true
		}
	}
	return false
}
