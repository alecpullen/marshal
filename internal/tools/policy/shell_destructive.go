package policy

import "strings"

// classifyDestructive adds destructive, publishing, privilege and
// catastrophic effects to an operation. It never guesses: unknown operands
// stay unknown rather than matching a floor by accident.
func classifyDestructive(op *ShellOperation) {
	base := lastPathSegment(op.Executable.Value)
	if strings.HasPrefix(base, "mkfs") {
		op.Effects |= ShellCatastrophic
		op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorFormat, OperationID: op.ID, Message: "filesystem formatting"})
		return
	}
	switch base {
	case "rm":
		if hasRecursiveShellFlag(op) {
			op.Effects |= ShellDestructive
			op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonDestructive, OperationID: op.ID, Message: "recursive removal"})
			for _, operand := range op.Operands {
				switch operand.Value {
				case "/", "~", "$HOME", "/*":
					op.Effects |= ShellCatastrophic
					op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorRootHome, OperationID: op.ID, Message: "recursive root/home removal"})
				}
			}
		}
	case "git":
		classifyGitDestructive(op)
	case "find":
		if hasFlag(op, "-delete") {
			op.Effects |= ShellDestructive
		}
	case "chmod", "chown":
		if hasRecursiveShellFlag(op) {
			op.Effects |= ShellDestructive
		}
	case "truncate", "shred":
		op.Effects |= ShellDestructive
	case "dd":
		classifyDDEffects(op)
	case "mkfs", "mke2fs", "newfs":
		op.Effects |= ShellCatastrophic
		op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorFormat, OperationID: op.ID, Message: "filesystem formatting"})
	case "diskutil":
		if strings.HasPrefix(firstOperand(op), "erase") {
			op.Effects |= ShellCatastrophic
			op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorFormat, OperationID: op.ID, Message: "disk erase"})
		}
	case "shutdown", "reboot", "halt", "poweroff":
		op.Effects |= ShellCatastrophic
		op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorShutdown, OperationID: op.ID, Message: "system shutdown"})
	case "sudo", "doas", "pkexec", "su":
		op.Effects |= ShellPrivilege
		op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonPrivilegeWrapper, OperationID: op.ID, Message: "privilege wrapper"})
	}
}

func classifyGitDestructive(op *ShellOperation) {
	switch gitSubcommand(op) {
	case "push":
		op.Effects |= ShellPublish
		op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonPublishing, OperationID: op.ID, Message: "git push"})
	case "reset":
		if hasFlag(op, "--hard") {
			op.Effects |= ShellDestructive
		}
	case "clean":
		if !isDryRun(op) && (hasFlag(op, "-f") || hasFlag(op, "--force")) {
			op.Effects |= ShellDestructive
		}
	case "restore", "checkout", "switch":
		op.Effects |= ShellDestructive
	case "branch":
		if hasFlag(op, "-D") || hasFlag(op, "-d") {
			op.Effects |= ShellDestructive
		}
	case "worktree":
		if len(op.Operands) >= 2 && op.Operands[1].Value == "remove" {
			op.Effects |= ShellDestructive
		}
	}
}

func classifyDDEffects(op *ShellOperation) {
	for _, operand := range op.Operands {
		if !strings.HasPrefix(operand.Value, "of=") {
			continue
		}
		target := strings.TrimPrefix(operand.Value, "of=")
		if target == "/dev/null" || target == "/dev/zero" {
			continue
		}
		op.Effects |= ShellDestructive
		if strings.HasPrefix(target, "/dev/") {
			op.Effects |= ShellCatastrophic
			op.Reasons = append(op.Reasons, PolicyReason{Code: ReasonFloorRawDisk, OperationID: op.ID, Message: "raw disk write"})
		}
	}
}

// hasRecursiveFlag reports whether an operation carries a recursive flag,
// including combined short flags such as -rf.
func hasRecursiveShellFlag(op *ShellOperation) bool {
	for _, f := range op.Flags {
		name := f.Name
		if name == "--recursive" || name == "-R" {
			return true
		}
		if strings.HasPrefix(name, "-") && !strings.HasPrefix(name, "--") && strings.Contains(name, "r") {
			return true
		}
	}
	return false
}

// isDryRun reports whether an operation is a dry run.
func isDryRun(op *ShellOperation) bool {
	return hasFlag(op, "-n") || hasFlag(op, "--dry-run")
}
