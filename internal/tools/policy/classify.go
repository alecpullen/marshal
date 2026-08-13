package policy

import (
	"fmt"
	"strings"

	"github.com/google/shlex"

	"marshal/internal/tools/registry"
)

// Classification represents the risk level and reason for a shell command
// after argv-aware analysis.
type Classification struct {
	Risk   registry.RiskLevel
	Reason string
}

// ClassifyCommand parses a shell command string using shell-aware splitting
// and classifies its risk level based on the command and flags present.
//
// Known destructive patterns:
//   - rm with both recursive (-r/-R/--recursive) and force (-f/--force) flags
//   - git clean with force flags (-f/-fd/-fdx/-fx)
//   - git reset --hard
//   - chmod/chown with recursive flags (-R/--recursive)
//   - find with -delete, or with -exec/-execdir running a destructive payload
//   - dd writing to an output file (of=), unconditionally so for a device
//
// All other commands return Classification with Risk set to RiskCommand.
func ClassifyCommand(input string) (Classification, error) {
	args, err := shlex.Split(input)
	if err != nil {
		return Classification{}, fmt.Errorf("classify command: %w", err)
	}
	if len(args) == 0 {
		return Classification{Risk: registry.RiskCommand, Reason: "empty command"}, nil
	}

	name := lastSegment(args[0])

	if reason := classifyEnvironment(name, args); reason != "" {
		return Classification{Risk: registry.RiskEnvironment, Reason: reason}, nil
	}

	switch name {
	case "rm":
		if hasFlagInArgs(args[1:], "r", "R", "recursive") && hasFlagInArgs(args[1:], "f", "force") {
			return Classification{Risk: registry.RiskDestructive, Reason: "rm -r -f"}, nil
		}
	case "git":
		if hasSubcmd(args, "clean") && hasFlagInArgs(args[1:], "f", "force") {
			return Classification{Risk: registry.RiskDestructive, Reason: "git clean -f*"}, nil
		}
		if hasSubcmd(args, "reset") && hasArg(args, "hard") {
			return Classification{Risk: registry.RiskDestructive, Reason: "git reset --hard"}, nil
		}
	case "chmod", "chown":
		if hasFlagInArgs(args[1:], "r", "R", "recursive") {
			return Classification{Risk: registry.RiskDestructive, Reason: name + " -R"}, nil
		}
	case "find":
		if reason := classifyFind(args[1:]); reason != "" {
			return Classification{Risk: registry.RiskDestructive, Reason: reason}, nil
		}
	case "dd":
		if reason := classifyDD(args[1:]); reason != "" {
			return Classification{Risk: registry.RiskDestructive, Reason: reason}, nil
		}
	}

	return Classification{Risk: registry.RiskCommand, Reason: "command"}, nil
}

// lastSegment returns the last /-separated component of p.
// destructivePayloads are argv0 names that make a find -exec destructive.
var destructivePayloads = map[string]bool{
	"rm": true, "rmdir": true, "shred": true, "truncate": true,
	"unlink": true, "dd": true, "mkfs": true,
}

// classifyFind reports why a find invocation is destructive, or "" if it is
// not. find is a first-class deletion primitive with no "rm" anywhere in the
// command string, so neither the substring guardrails nor the argv0 switch
// above would otherwise see it.
//
// Matching is argv-aware on purpose: `find . -name '*-delete*'` is a search,
// not a deletion, and must not trip this.
func classifyFind(args []string) string {
	for i, a := range args {
		switch a {
		case "-delete":
			return "find -delete"
		case "-exec", "-execdir", "-ok", "-okdir":
			// The payload is the next token; anything after it is that
			// command's own arguments.
			if i+1 < len(args) && destructivePayloads[lastSegment(args[i+1])] {
				return "find " + a + " " + lastSegment(args[i+1])
			}
		}
	}
	return ""
}

// classifyDD reports why a dd invocation is destructive, or "" if it is not.
// dd without an of= operand only reads, which is why the check is on the
// output operand rather than the command name.
func classifyDD(args []string) string {
	for _, a := range args {
		out, ok := strings.CutPrefix(a, "of=")
		if !ok {
			continue
		}
		if strings.HasPrefix(out, "/dev/") {
			return "dd of=" + out + " (writes to a device)"
		}
		return "dd of=" + out
	}
	return ""
}

func lastSegment(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// hasArg checks whether -name or --name appears in argv.
func hasArg(argv []string, name string) bool {
	short := "-" + name
	long := "--" + name
	for _, a := range argv {
		if a == short || a == long {
			return true
		}
	}
	return false
}

// hasShortArg checks whether arg is a short flag string that contains the
// single-character name. This handles combined flags like -rf (contains both
// r and f) as well as standalone flags like -r. Long flags (--foo) are
// rejected.
func hasShortArg(arg, name string) bool {
	if len(arg) < 2 || arg[0] != '-' || (len(arg) > 2 && arg[1] == '-') {
		return false
	}
	return strings.ContainsRune(arg[1:], rune(name[0]))
}

// hasSubcmd finds a non-flag argument in argv (starting at index 1) that
// matches sub. It is used to detect subcommands like "clean" in "git clean".
func hasSubcmd(argv []string, sub string) bool {
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") && a == sub {
			return true
		}
	}
	return false
}

// hasFlagInArgs checks whether any of the given flag names (short or long)
// appear in args. Short names are checked via hasShortArg (handling combined
// flags like -rf), long names are checked via exact match.
func hasFlagInArgs(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if len(n) == 1 {
				// Single character: short flag with possible combining
				if hasShortArg(a, n) {
					return true
				}
			} else {
				// Multi-character: long flag, exact match with --
				if a == "--"+n {
					return true
				}
			}
		}
	}
	return false
}

// classifyEnvironment reports why a command mutates state outside the
// working directory (build/package caches, global git config, system
// package managers), or "" if it does not. Checked before the destructive
// switch: the two tiers never overlap (environment commands touch global
// state; destructive commands destroy workspace files).
func classifyEnvironment(name string, args []string) string {
	switch name {
	case "go":
		if hasSubcmd(args, "clean") && (hasArg(args, "cache") || hasArg(args, "modcache")) {
			return "go clean -cache/-modcache"
		}
	case "git":
		if hasSubcmd(args, "config") && hasArg(args, "global") {
			return "git config --global"
		}
	case "npm":
		if hasSubcmd(args, "cache") && hasSubcmd(args, "clean") {
			return "npm cache clean"
		}
	case "pip", "pip3":
		if hasSubcmd(args, "cache") && (hasSubcmd(args, "purge") || hasSubcmd(args, "remove")) {
			return name + " cache purge/remove"
		}
	case "yarn":
		if hasSubcmd(args, "cache") && hasSubcmd(args, "clean") {
			return "yarn cache clean"
		}
	case "brew":
		// install is a routine dev operation (adding a package); only
		// destructive/global mutations warrant an environment confirmation.
		for _, sub := range []string{"uninstall", "cleanup"} {
			if hasSubcmd(args, sub) {
				return "brew " + sub
			}
		}
	case "apt", "apt-get", "dnf", "yum":
		// install is routine; remove/purge are destructive.
		for _, sub := range []string{"remove", "purge"} {
			if hasSubcmd(args, sub) {
				return name + " " + sub
			}
		}
	}
	return ""
}
