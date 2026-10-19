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
	case "awk", "source", ".", "sh", "bash", "zsh", "dash", "ksh", "eval":
		op.Effects |= ShellUnknown
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyGit(op *ShellOperation) {
	switch gitSubcommand(op) {
	case "status", "diff", "log", "show", "rev-parse", "branch", "remote",
		"config", "ls-files", "describe", "blame", "grep", "cat-file":
		op.Effects |= ShellRead
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
	case "get", "mod", "download", "install":
		op.Effects |= ShellWrite
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
	default:
		op.Effects |= ShellUnknown
	}
}

func classifyPip(op *ShellOperation) {
	if firstOperand(op) != "install" {
		op.Effects |= ShellUnknown
		return
	}
	if hasFlag(op, "--user") || hasFlag(op, "--target") {
		op.Effects |= ShellWrite
		return
	}
	op.Effects |= ShellEnvironment
}

func hasFlag(op *ShellOperation, name string) bool {
	for _, f := range op.Flags {
		if f.Name == name {
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
// values (-C, -c, --git-dir, --work-tree).
func gitSubcommand(op *ShellOperation) string {
	argv := op.Argv
	for i := 1; i < len(argv); i++ {
		v := argv[i].Value
		if v == "-C" || v == "-c" || v == "--git-dir" || v == "--work-tree" {
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
