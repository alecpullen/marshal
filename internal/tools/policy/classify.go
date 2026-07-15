package policy

import (
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
//
// All other commands return Classification with Risk set to RiskCommand.
func ClassifyCommand(input string) (Classification, error) {
	args, err := shlex.Split(input)
	if err != nil {
		return Classification{Risk: registry.RiskCommand, Reason: "unparseable command"}, err
	}
	if len(args) == 0 {
		return Classification{Risk: registry.RiskCommand, Reason: "empty command"}, nil
	}

	name := basename(args[0])

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
	}

	return Classification{Risk: registry.RiskCommand, Reason: "command"}, nil
}

// basename returns the last /-separated component of p.
func basename(p string) string {
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
