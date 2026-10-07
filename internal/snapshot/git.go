package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// This file holds the shared Git plumbing: the sanitized invocation used by
// EVERY managed command, the workspace hash both layouts agree on, and the
// diagnostic formatting that turns a bare "exit status 1" back into Git's own
// message.
//
// What it deliberately no longer holds is the legacy capture path — the
// `git add`/`git commit` shadow-repo snapshot, its approximate ignore matcher,
// and its work-tree walk. Those are gone rather than disabled: capture now goes
// through Manager.AdmitAndCaptureWithCleanup, which plans, reserves, writes
// loose objects itself, and never lets a subprocess write into the store. A
// retained copy of the old path would be a second, unbounded write path into
// the same directories, which is exactly what this work exists to remove.

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// gitConfigPins are the -c arguments pinned onto every managed git invocation.
// They disable everything that could write or execute outside the manager's
// control during a snapshot operation:
//
//   - gc.auto / gc.autoDetach / maintenance.auto: no background repack may be
//     started under a capture, which is how abandoned tmp_pack_* files
//     accumulated.
//   - core.hooksPath= : an empty hooks path means no hook is found, so a
//     project cannot run arbitrary code through a shadow-repo hook.
//   - core.fsmonitor / core.untrackedCache: no persistent daemon or index
//     side-state.
//
// External diff is not pinned here: "-c diff.external=" makes git try to
// execute an empty program. It is disabled per invocation with --no-ext-diff
// where a diff runs, and GIT_EXTERNAL_DIFF is cleared from the environment.
func gitConfigPins() []string {
	return []string{
		"-c", "gc.auto=0",
		"-c", "gc.autoDetach=false",
		"-c", "maintenance.auto=false",
		"-c", "core.hooksPath=",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
	}
}

// unsafeGitEnvVars are inherited variables that let the surrounding
// environment redirect, replace, or extend what git reads and writes. All are
// cleared so a managed invocation can only touch the Git directory the manager
// named explicitly.
var unsafeGitEnvVars = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_EXTERNAL_DIFF":                true,
	"GIT_DIFF_OPTS":                    true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_COUNT":                 true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_NAMESPACE":                    true,
	"GIT_PREFIX":                       true,
	"GIT_TEMPLATE_DIR":                 true,
	"GIT_OPTIONAL_LOCKS":               true,
}

// sanitizedGitEnv builds the child environment for a managed git invocation:
// the inherited environment minus every unsafe variable, plus explicit safe
// values and a nonexistent user/system config path so a user's gitconfig cannot
// change snapshot behaviour.
//
// readOnly additionally sets GIT_OPTIONAL_LOCKS=0, which stops git from taking
// an index lock for an operation that only reports state.
func sanitizedGitEnv(readOnly bool) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || unsafeGitEnvVars[name] {
			continue
		}
		env = append(env, kv)
	}
	// A path that does not exist makes git ignore the file entirely, which is
	// the intended "no user config" state (unlike pointing at the null device,
	// whose meaning differs per platform).
	absent := filepath.Join(os.TempDir(), "marshal-absent-gitconfig")
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+absent,
		"GIT_CONFIG_SYSTEM="+absent,
	)
	if readOnly {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	return env
}

// newGitCmd builds a sanitized git *exec.Cmd. gitDir is required; workTree,
// when non-empty, is passed as --work-tree and used as the process working
// directory.
func newGitCmd(ctx context.Context, gitBin, gitDir, workTree string, readOnly bool, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+4)
	full = append(full, gitConfigPins()...)
	full = append(full, "--git-dir="+gitDir)
	if workTree != "" {
		full = append(full, "--work-tree="+workTree)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, gitBin, full...)
	if workTree != "" {
		cmd.Dir = workTree
	}
	cmd.Env = sanitizedGitEnv(readOnly)
	return cmd
}

// runGitCombined runs a prepared git command in its own process tree and
// returns its combined output. The process-tree lifetime guarantee is what lets
// a caller release store ownership knowing no cancelled git is still writing.
func runGitCombined(ctx context.Context, cmd *exec.Cmd, maxOutput int) ([]byte, error) {
	return runProcessTree(ctx, cmd, maxOutput)
}

// projectHash returns the 12-hex workspace identifier for a workspace root.
func projectHash(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// gitOutputSuffix formats git's own output for inclusion in an error, or ""
// when it said nothing. Callers log these errors at Warn while the raw output
// is only logged at Debug, so without this a failure reads as a bare "exit
// status 1" — which is how a snapshot bug stayed invisible through hundreds of
// occurrences.
func gitOutputSuffix(out []byte) string {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return ""
	}
	msg = strings.ReplaceAll(msg, "\n", "; ")
	const cap = 300
	if len(msg) > cap {
		msg = msg[:cap] + "…"
	}
	return ": " + msg
}
