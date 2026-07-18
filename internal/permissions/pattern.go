package permissions

import (
	"path/filepath"
	"strings"

	"github.com/google/shlex"

	"marshal/internal/app/session"
	"marshal/internal/tools/patch"
)

// PatternForApproval builds the glob-like pattern persisted in the
// user's config when the user picks "always allow" in the approval
// chooser. The result is matched against the full parsed command
// (post-A2), not just argv0.
func PatternForApproval(tc *session.PendingToolCall) string {
	if tc.Name == "shell.run" || tc.Name == "test.run" {
		argv, err := shlex.Split(tc.Command)
		if err != nil || len(argv) == 0 {
			// If shlex fails to parse, fall back to the raw command as an exact pattern.
			if tc.Command != "" {
				return tc.Command
			}
			return "*"
		}
		return strings.Join(argv, " ")
	}
	if tc.Name == "file.write_patch" {
		patches, err := patch.Parse(tc.Args)
		if err != nil || len(patches) == 0 {
			return "**"
		}
		dir := commonDir(patches)
		if dir == "" || dir == "." {
			return "**"
		}
		return dir + "/**"
	}
	return "*"
}

func commonDir(patches []patch.FilePatch) string {
	if len(patches) == 0 {
		return ""
	}
	var dirs []string
	for _, p := range patches {
		dirs = append(dirs, filepath.Dir(p.Path))
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	common := dirs[0]
	for _, d := range dirs[1:] {
		common = longestCommonPrefix(common, d, string(filepath.Separator))
		if common == "" {
			return ""
		}
	}
	return common
}

func longestCommonPrefix(a, b, sep string) string {
	partsA := strings.Split(a, sep)
	partsB := strings.Split(b, sep)
	var common []string
	for i := 0; i < len(partsA) && i < len(partsB); i++ {
		if partsA[i] == partsB[i] {
			common = append(common, partsA[i])
		} else {
			break
		}
	}
	return strings.Join(common, sep)
}
