package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

type toolCategory string

const (
	catRead   toolCategory = "read"
	catSearch toolCategory = "search"
	catShell  toolCategory = "shell"
	catWrite  toolCategory = "write"
	catPatch  toolCategory = "patch"
	catOther  toolCategory = "other"
)

func categorize(toolName string) toolCategory {
	switch toolName {
	case "file.read", "repo.card", "repo.index", "repo.map", "symbols.find":
		return catRead
	case "repo.search":
		return catSearch
	case "shell.run":
		return catShell
	case "file.write_patch":
		return catPatch
	case "file.write":
		return catWrite
	default:
		return catOther
	}
}

// mutationKind classifies a tool call by whether it is known to change
// repository or system state.
type mutationKind int

const (
	// mutNone: the call is read-only or unknown, so it must not be treated
	// as a mutation. Unknown is deliberately neutral: a tool that turns out
	// to mutate gets one missed reset/gate instead of research turns being
	// nagged by a heuristic guess (the old default-mutating behavior).
	mutNone mutationKind = iota
	// mutKnown: the call is a known edit to repository or system state.
	mutKnown
)

// mutationOf reports whether a category of tool call is a known state
// change. After a mutating call, previously gathered observations are
// stale, so repeating an earlier call counts as fresh progress again.
func mutationOf(cat toolCategory, args string) mutationKind {
	switch cat {
	case catWrite, catPatch:
		return mutKnown
	case catShell:
		return shellMutationKind(args)
	default:
		return mutNone
	}
}

// shellMutationKind classifies a shell.run call. shell.run is dual-use —
// it is how models both mutate (install, generate, delete) and work
// read-only (git log, grep, find) — so only commands on the explicit
// mutating list count as mutations. Everything else (including commands
// the list does not recognise) is neutral: it neither resets repeat
// streaks nor arms the verification gate. Verification-shaped commands
// (go test, make test, …) are checked first and are never mutations, so
// a build that happens to write artifacts keeps its verification role.
// Both checks run on the quote-stripped command, so a keyword inside
// quotes ("make test" in a commit message) is inert in both directions.
func shellMutationKind(args string) mutationKind {
	if looksLikeVerificationCommand(args) {
		return mutNone
	}
	if looksLikeMutatingShellCommand(args) {
		return mutKnown
	}
	return mutNone
}

type assessment int

const (
	assessProgressing assessment = iota
	assessHardStall
)

// Escalation ladder for identical repeated calls (kimi-code's thresholds):
// a gentle reminder at 3, an explicit one at 5, a "stop calling tools" one
// at 8, and a hard stall — which asks the user how to proceed — at 12.
const (
	repeatRemindGentle = 3
	repeatRemindStrong = 5
	repeatRemindStop   = 8
	repeatHardStall    = 12
)

// idleEntryName is the sentinel name used for synthetic idle entries recorded
// by recordIdle. It deliberately starts with "<" so it can never collide with
// a real tool name.
const idleEntryName = "<idle>"

// idleStallThreshold consecutive <idle> entries escalate directly to a hard
// stall: a model that has gone silent should be handled rather than
// re-prompted indefinitely.
const idleStallThreshold = 3

type callEntry struct {
	name string
	args string
	ok   bool // whether the call executed successfully; idle sentinels are always false
}

// progressTracker counts repeats of identical (tool, args, output) signatures.
// Including the output hash in the signature means re-running a command whose
// result changed — e.g. a test that now passes — is never a repeat (crush's
// loop-detection insight).
type progressTracker struct {
	history    []callEntry // real calls and idle sentinels, in order
	counts     map[string]int
	lastRepeat int // repeat count returned by the most recent record()
	idleRun    int // consecutive recordIdle calls with no tool call between
}

func newProgressTracker() *progressTracker {
	return &progressTracker{counts: make(map[string]int)}
}

func hashToolResult(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// record notes one executed tool call and returns how many times this exact
// (name, args, output) signature has now occurred since the last mutating
// call.
func (t *progressTracker) record(name, normalizedArgs, resultHash string, success bool) int {
	if success && mutationOf(categorize(name), normalizedArgs) == mutKnown {
		// State actually changed: earlier observations are stale, future
		// repeats are fresh progress. A FAILING mutating call changes
		// nothing, so it must not reset the streak — otherwise identical
		// futile calls (e.g. the same malformed patch) never accumulate
		// enough repeats to trip loop detection. Only KNOWN mutations
		// reset: file.write/file.write_patch, or shell.run commands on
		// the explicit mutating allowlist. Read-only and unknown shell
		// commands keep the streak so genuine loops still trip it.
		t.counts = make(map[string]int)
	}
	key := name + "\x00" + normalizedArgs + "\x00" + resultHash
	t.counts[key]++
	t.lastRepeat = t.counts[key]
	t.idleRun = 0
	t.history = append(t.history, callEntry{name: name, args: normalizedArgs, ok: success})
	return t.lastRepeat
}

// recordIdle appends a synthetic idle entry so assess() can detect sustained
// silence (empty responses, declined ask_user).
func (t *progressTracker) recordIdle(reason string) {
	t.idleRun++
	t.history = append(t.history, callEntry{name: idleEntryName, args: reason, ok: false})
}

// resetCounts clears repeat streaks after the user has given fresh guidance,
// so the next identical call starts a new streak instead of instantly
// re-tripping the hard stall.
func (t *progressTracker) resetCounts() {
	t.counts = make(map[string]int)
	t.lastRepeat = 0
	t.idleRun = 0
}

// lastCall returns the most recent recorded real call so stall messages can
// name it. ok is false when nothing has been recorded.
func (t *progressTracker) lastCall() (name, args string, ok bool) {
	for i := len(t.history) - 1; i >= 0; i-- {
		if t.history[i].name != idleEntryName {
			return t.history[i].name, t.history[i].args, true
		}
	}
	return "", "", false
}

func (t *progressTracker) assess() assessment {
	if t.idleRun >= idleStallThreshold {
		return assessHardStall
	}
	if t.lastRepeat >= repeatHardStall {
		return assessHardStall
	}
	return assessProgressing
}

// repeatReminder returns escalating guidance to append to a repeated call's
// tool result. Putting the reminder in the result (not a separate system
// message) keeps it adjacent to the evidence the model is ignoring.
func repeatReminder(count int, name, args string) string {
	switch {
	case count >= repeatRemindStop:
		return "\n\n<system-reminder>\nYou are stuck: this exact tool call has produced the identical result " +
			fmt.Sprintf("%d times. Stop all tool calls in your next response. ", count) +
			"Review what you have learned, then reply with a text-only summary of the problem, what you tried, and what decision or information you need.\n</system-reminder>"
	case count >= repeatRemindStrong:
		return fmt.Sprintf("\n\n<system-reminder>\nRepeated tool call detected:\n- tool: %s\n- repeated_times: %d\n- arguments: %s\nThese repeats made no progress. Do not issue this exact call again; choose a different action, different arguments, or finish the task with the evidence you already have.\n</system-reminder>", name, count, args)
	case count >= repeatRemindGentle:
		return "\n\n<system-reminder>\nYou are repeating the exact same tool call with identical arguments and identical output. Analyze the result above; if the task is not complete, take a different action instead of repeating this call.\n</system-reminder>"
	default:
		return ""
	}
}

// shellCommandField extracts the "command" field from a shell.run args
// payload. All shell.run classifiers match the command field only —
// matching the whole args JSON would let an unrelated field (e.g. a
// background note containing the word "pytest") influence classification.
func shellCommandField(args string) (string, bool) {
	var a struct {
		Command string `json:"command"`
	}
	if len(args) == 0 || json.Unmarshal([]byte(args), &a) != nil {
		return "", false
	}
	return a.Command, true
}

// mutatingCommandPatterns are the explicit allowlist of shell.run commands
// that change repository or system state. Classification is allowlist-based,
// not "anything not recognised is mutating": shell.run is dual-use, and
// treating every unrecognized command as a mutation nagged research turns
// (git log, grep, find) with the verification reminder and wrongly reset
// repeat-loop streaks. A genuinely new mutating command gets one missed
// gate/reset until it is added here — the cheap direction to err.
//
// The list covers: file deletion and in-place rewrites (sed -i, perl -i,
// including clustered short-flag forms like sed -ni and perl -pi), git
// state changes (checkout/switch/restore/reset/clean/rebase/merge/
// cherry-pick/revert/am/apply/rm/mv and the state-changing stash forms),
// build and code generators writing outputs, docker/container mutations
// (exec only when the command it runs is destructive), remote execution,
// downloads that land in files, curl piped to a shell, sudo, and
// redirection into files (except /dev/null sinks and fd-numbered
// diagnostics redirects). It does NOT need to cover file creation via
// file.write/file.write_patch (classified by tool name), verification
// commands (checked earlier), or housekeeping (git add/commit/push, mkdir/
// cp/mv, gofmt, installs) — those stay neutral by not being on any list.
// Commit and push are deliberately absent even though they change state:
// they do not invalidate a verification of the working tree, and push is
// separately guarded by the approval policy.
//
// Known limitations (accepted trade-offs — the cheap failure direction is
// a missed nudge, not a false one): prefix wrappers (env, nohup, time,
// timeout, nice, FOO=bar) and subshell/brace bodies without a `;`/`&&`
// anchor (`bash -c 'echo hi > f'`, `eval 'rm x'`) stay neutral because the
// quote-stripper cannot distinguish data-quotes (commit messages) from
// code-quotes; `find -delete`/`-exec rm` and archive extraction (`tar -xzf`,
// `unzip`) stay neutral as unrecognized commands. Adding any of them is
// one regexp.MustCompile line.
//
// Generator and destructive patterns anchor to a command-segment start
// (start of command, or after ; | & \n and the (/{ subshell-group openers),
// so `cd x && make` arms while `grep make Makefile` and
// `git commit -m "make timeout configurable"` do not — quoted spans are
// stripped before matching, so words inside quotes are always data.
// segStart anchors a pattern to the start of a shell command segment: the
// start of the payload, a [;|&\n] boundary, optionally followed by ONE
// subshell/brace group opener that begins the segment (`&& (rm y)`,
// `; { make; }`). A mid-segment ( or { — e.g. the group in
// `grep -E '(rm|ls)' f` (whose quotes were stripped before matching) — is
// a plain token, not a segment boundary, and does not anchor. Newline is
// included so multi-line payloads (`ls\nrm -rf build`) anchor each line's
// commands. `do`/`then` bodies (`for …; do rm $f; done`,
// `if …; then rm x; fi`) anchor at the `do`/`then` keyword, which is
// followed by the body command.
const segStart = `(?:^|[;|&\n]|\bdo\b|\bthen\b)\s*(?:[({]\s*)?`

var mutatingCommandPatterns = []*regexp.Regexp{
	// Destructive file operations, as the first word of a command segment.
	// Bare sed/awk are deliberately absent: `sed -n '5p' f` and
	// `awk '{print $1}' f` are read-only
	// research; their writing forms are covered by sed -i and the
	// redirection rule below. Bare xargs is absent too — `find | xargs grep`
	// is research — only xargs feeding a destructive command counts.
	// ssh anchors with a following whitespace so `ssh-keygen` (read-only
	// fingerprint check) does not false-arm. `--help`/`-h` forms of
	// otherwise-mutating commands (truncate --help, git stash --help) are
	// excluded by helpFlagStrip before matching.
	// ssh anchors with a following whitespace or end-of-string so
	// `ssh-keygen` (read-only fingerprint check) does not false-arm. The
	// \b is on each alternative individually because ssh(?:\s|$) consumes
	// the space, leaving a non-word char next (e.g. `-n`) where \b would
	// fail.
	regexp.MustCompile(segStart + `(?:rm\b|rmdir\b|dd\b|truncate\b|tee\b|wget\b|ssh(?:\s|$)|scp\b|rsync\b|sudo\b)`),
	// xargs feeding a destructive command: flags with values (-n 2) are
	// tolerated alongside bare flags (-0).
	regexp.MustCompile(`\bxargs\s+(?:-[A-Za-z0-9]+(?:\s+\S+)?\s+)*(rm|tee|dd|truncate)\b`),
	// sed -i / --in-place and perl -i rewrite files in place. The -i flag
	// is matched inside a short-flag cluster so `sed -ni`, `perl -pi`,
	// `perl -i.bak` all arm. Anchored to segStart so `grep sed -i f`
	// (pattern-then-flag order) does not false-arm. `--in-place` is
	// matched as a long flag.
	regexp.MustCompile(segStart + `sed\b[^;|&]*\s-[A-Za-z]*i[A-Za-z]*\b`),
	regexp.MustCompile(segStart + `sed\b[^;|&]*\s--in-place\b`),
	regexp.MustCompile(segStart + `perl\b[^;|&]*\s-[A-Za-z]*i[A-Za-z]*\b`),
	// curl writing to a file: -o or -O standalone or inside a short-flag
	// cluster (-sLo), --output, --remote-name. A plain curl is research.
	regexp.MustCompile(segStart + `curl\b[^;|&]*\s(-[A-Za-z]*[oO][A-Za-z]*|--output|--remote-name)\b`),
	// Git state changes: checkout/switch/restore/reset/clean change the
	// working tree; rebase/merge/cherry-pick/revert/am/apply change history
	// or the tree; rm/mv change the index and tree. Only the
	// state-changing stash forms arm — bare `git stash`, the flag form
	// `git stash -m msg`, and push/pop/apply/drop/clear/branch/save —
	// while `git stash list`/`show` are read-only research. Commit, add,
	// tag, fetch, pull, push, status, log, show, diff, and branch are
	// deliberately absent (housekeeping or read-only).
	regexp.MustCompile(segStart + `git\s+(checkout|switch|restore|reset|clean|rebase|merge|cherry-pick|revert|am|apply|rm|mv)\b`),
	regexp.MustCompile(segStart + `git\s+stash\s+(push|pop|apply|drop|clear|branch|save)\b`),
	regexp.MustCompile(segStart + `git\s+stash\s*(?:$|[;|&\n])`),
	regexp.MustCompile(segStart + `git\s+stash\s+-`),
	// Build and code generators that write artifacts into the workspace,
	// anchored to a segment start so the names match as commands, not as
	// arguments or quoted text (`grep make Makefile` stays neutral).
	// Dry-run forms (`make -n`, `--dry-run`) are stripped by dryRunStrip
	// before matching so a read-only preview does not arm the gate.
	// cargo build is absent: it is already verification-classified, and
	// verification wins (see shellMutationKind).
	regexp.MustCompile(segStart + `go\s+generate\b`),
	regexp.MustCompile(segStart + `(make|cmake|gradle|mvn)\b`),
	// Docker/container mutations; exec arms only when the command it runs
	// is itself destructive (`docker exec c ls` stays neutral). The
	// container name and flags are skipped with a non-greedy scan so
	// `docker exec -it c rm -rf /data` arms.
	regexp.MustCompile(segStart + `docker\s+(build|run|rm|rmi|kill|stop|compose)\b`),
	regexp.MustCompile(segStart + `docker\s+exec\s+\S+\s+(?:-[A-Za-z]+\s+)*(?:\S+\s+)*(rm|dd|truncate|tee)\b`),
	// curl piped to a shell downloads and executes arbitrary code:
	// `curl -s https://x/install.sh | bash` / `| sh` / `| sudo bash`.
	// The sudo variant also arms via the sudo pattern; this catches
	// the non-sudo pipe-to-shell form.
	regexp.MustCompile(segStart + `curl\b[^;|&]*\|\s*(bash|sh)\b`),
	// Redirection into a file covers the open-ended cases the named
	// commands miss: `echo hi > out.txt`, `cmd >> append.log`. It anchors
	// to a word followed by a redirect operator and a target. Quoted spans,
	// fd-numbered redirects (`2> err.log`, `2>&1`, `>&1`) and /dev/null
	// sinks are stripped first in looksLikeMutatingShellCommand, so
	// redirecting diagnostics or discarding output is never an edit.
	regexp.MustCompile(segStart + `[A-Za-z0-9_./"'-]+[^;|&\n]*[^0-9]\s*>+\s*\S`),
}

// fdRedirectStrip removes fd-numbered redirect operators (`2>`, `2>>`,
// `2>&1`) and descriptor dups (`>&1`, `>&2`) from a command before
// classification. These redirect diagnostics or duplicate existing
// descriptors, not files, and would otherwise trip the redirection rule
// (`git commit -m msg 2> err.log`, `cmd >&1`). The fd number must start its
// token (command start or after whitespace), so `head -2>f` — where the 2
// is a flag value, not an fd — is left unstripped. The redirection rule's
// `[^0-9]` guard then rejects it (the char before `>` is the digit `2`),
// leaving `head -2>f` neutral — an ultra-rare form where the safe direction
// is a missed nudge. A digit followed by whitespace before `>` (e.g.
// `sleep 2 > f`) is likewise left alone — that form is lexically ambiguous
// and stays classified as mutating, the safe direction.
var fdRedirectStrip = regexp.MustCompile(`(?:^|[\s])[0-9]+>>?(?:&[0-9]+)?|>&[0-9]+`)

// quotedSpanStrip removes single- and double-quoted spans before
// classification. Words inside quotes are data, not commands: commit
// messages ("make timeout configurable"), grep patterns (">"), and awk
// predicates ('$3 > 5') must never arm the gate. Unclosed quotes match
// nothing and leave the command untouched.
var quotedSpanStrip = regexp.MustCompile(`'[^']*'|"[^"]*"`)

// readOnlyGitStrip removes read-only invocations of otherwise mutating git
// subcommands before matching: `git apply --stat/--check` inspect a patch
// without touching the tree, unlike a bare `git apply`.
var readOnlyGitStrip = regexp.MustCompile(`\bgit\s+apply\s+--(?:stat|check)\b`)

// devNullStrip removes redirects targeting /dev/null before classification.
// Discarding output mutates nothing; without this, `ls > /dev/null` armed
// the gate while the already-stripped `cmd 2>/dev/null` did not.
var devNullStrip = regexp.MustCompile(`(?:>>?)\s*/dev/null\b`)

// isDryRun reports whether a command contains a dry-run flag
// (`--dry-run`, `--just-print`, `--no-run`, `--recon`, or `-n` for make
// and git clean specifically). A dry run writes nothing: it must neither
// arm the gate nor satisfy it. Checked early in both directions so the
// remaining command is never classified after a dry-run flag is present.
// The bare `-n` is scoped to make and git clean — the only tools on the
// mutating list where `-n` means dry-run. Elsewhere `-n` is a count flag
// (git log -n 5, head -n 5), parallelism (pytest -n auto), max-args
// (xargs -n 2 rm), or stdin-from-/dev/null (ssh -n host); a bare `-n`
// there would mask real mutations and suppress real verifications.
var isDryRun = regexp.MustCompile(
	`(?:^|\s)--?(?:dry-run|just-print|no-run|recon)\b` +
		`|(?:^|\s)make\s+(?:-\w+\s+)*-n\b` +
		`|(?:^|\s)git\s+clean\s+(?:-\w+\s+)*-n\b`)

// isHelpFlag reports whether a command contains a `--help` or `-h` flag.
// Help flags print documentation and change nothing, so a command with
// `--help` must never arm the gate. Checked early, like isDryRun, because
// stripping the flag would leave the bare command name (e.g. `truncate`)
// which then matches the mutating list. The bare `-h` is kept unscoped:
// it is `--human-readable` for some tools (ls, du, df), but masking a
// mutation (`rm -rf build && ls -h`) is the cheap failure direction (a
// missed nudge, not a false one), and scoping it to a segment start would
// regress `git stash -h` (help) back to arming via the stash pattern.
var isHelpFlag = regexp.MustCompile(`(?:^|\s)--help\b|(?:^|\s)-h\b`)

// looksLikeMutatingShellCommand reports whether a shell.run args payload is
// on the explicit mutating-command allowlist. Quoted spans are stripped
// first — words inside quotes are data, not commands — then
// verification-shaped commands are excluded (the two lists overlap:
// \bmake\b matches "make test"), then fd-numbered redirects, read-only git
// forms, dry-run/help flags, and /dev/null sinks are stripped, and the
// mutating list is checked.
func looksLikeMutatingShellCommand(args string) bool {
	cmd, ok := shellCommandField(args)
	if !ok {
		return false
	}
	cmd = quotedSpanStrip.ReplaceAllString(cmd, "")
	// Dry-run and help forms write nothing and must not arm the gate.
	if isDryRun.MatchString(cmd) || isHelpFlag.MatchString(cmd) {
		return false
	}
	// Verification-shaped commands are never mutations, even where the
	// two lists overlap (\bmake\b matches "make test"; go/cargo builds
	// write artifacts). Checked here as well as in shellMutationKind so
	// the raw classifier and the gate agree. An unbalanced quote leaves
	// its tail intact, so `go test ./...` in a mangled command still
	// reads as verification — the safe direction.
	for _, p := range verificationCommandPatterns {
		if p.MatchString(cmd) {
			return false
		}
	}
	cmd = fdRedirectStrip.ReplaceAllString(cmd, "")
	cmd = readOnlyGitStrip.ReplaceAllString(cmd, "")
	cmd = devNullStrip.ReplaceAllString(cmd, "")
	for _, p := range mutatingCommandPatterns {
		if p.MatchString(cmd) {
			return true
		}
	}
	return false
}

// verificationCommandPatterns match shell.run argument payloads that are
// themselves a verification step (running tests, vet, lint, or a build).
// Matching happens against the raw normalized args JSON, e.g.
// {"command":"go test ./..."}. Build-tool targets (make/gradle/mvn) are
// matched with flag-tolerant patterns so `make -j4 test`,
// `make -C build test`, and `mvn -q verify` still count as verification.
// The flag-value pattern `(?:-\w+\s+\S+\s+)*` handles flags that take a
// value (make -C dir) alongside bare flags (make -j4).
var verificationCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgo\s+(test|vet|build)\b`),
	regexp.MustCompile(`\bnpm\s+(run\s+)?test\b`),
	regexp.MustCompile(`\b(pnpm|yarn)\s+(run\s+)?test\b`),
	regexp.MustCompile(`\bpytest\b`),
	regexp.MustCompile(`\bcargo\s+(test|check|clippy|build)\b`),
	// make/gradle/mvn: tolerate bare flags (-j4) and flag-with-value
	// (-C dir) between the tool name and the target.
	regexp.MustCompile(`\bmake\s+(?:-\w+\s+(?:\S+\s+)?)*(?:test|check)\b`),
	regexp.MustCompile(`\b(gradle|mvn)\s+(?:-\w+\s+(?:\S+\s+)?)*(?:test|check|verify)\b`),
}

// looksLikeVerificationCommand reports whether a shell.run args payload is a
// test/vet/lint/build invocation. shell.run is dual-use: it is how models
// both mutate (install, generate, delete) and verify (go test), so the
// finalize gate cannot treat every shell call as a mutation. Only the
// "command" field is matched — matching the whole args JSON would let an
// unrelated field (e.g. a background note or an env hint containing the word
// "pytest") count as verification and let a mutation masquerade as one.
func looksLikeVerificationCommand(args string) bool {
	cmd, ok := shellCommandField(args)
	if !ok {
		return false
	}
	// Quoted spans are stripped before matching: `git commit -m "make
	// test pass"` carries the keyword as data and must not satisfy the
	// gate. An unbalanced quote leaves its tail intact, so a mangled
	// verifier still reads as one — the safe direction. Dry-run forms
	// are neither verification nor mutation: `make -n test` prints a
	// plan but runs nothing.
	cmd = quotedSpanStrip.ReplaceAllString(cmd, "")
	if isDryRun.MatchString(cmd) {
		return false
	}
	for _, p := range verificationCommandPatterns {
		if p.MatchString(cmd) {
			return true
		}
	}
	return false
}

// unverifiedMutation reports the last successful mutating call and whether
// it lacks any later verification call. Verification means test.run,
// diagnostics.check, or a test-like shell.run. Mutations are KNOWN edits
// only: file.write/file.write_patch by name, or shell.run commands on the
// explicit mutating allowlist (looksLikeMutatingShellCommand). Every other
// shell.run — housekeeping like git commit, research like git log, and any
// unrecognized command — is neutral: it neither arms the gate nor
// satisfies it. Only a SUCCESSFUL mutation counts (a failed mutation
// changes nothing, so it cannot arm the gate), but a verification call
// counts whether or not it succeeded: test.run and shell.run surface a
// non-zero exit as a tool error, so a red test is recorded as a failed
// call — and it must still satisfy the gate. Otherwise a model that
// honestly runs tests, finds them red, and reports "tests still fail"
// would be penalized identically to one that never verified at all, which
// is the opposite of what the gate is for. The finalize gate deliberately
// does not loop until tests pass; it nudges once and then flags the answer
// as unverified.
func (t *progressTracker) unverifiedMutation() (callEntry, bool) {
	lastMutationIdx := -1
	lastVerifyIdx := -1
	var lastMutation callEntry
	for i, e := range t.history {
		if e.name == idleEntryName {
			continue
		}
		isVerification := e.name == "test.run" || e.name == "diagnostics.check" ||
			(e.name == "shell.run" && looksLikeVerificationCommand(e.args))
		if isVerification {
			// A verification call satisfies the gate regardless of whether it
			// passed (see the comment above): ok is intentionally not checked
			// here.
			lastVerifyIdx = i
			continue
		}
		if e.ok && mutationOf(categorize(e.name), e.args) == mutKnown {
			lastMutationIdx = i
			lastMutation = e
		}
	}
	if lastMutationIdx < 0 {
		return callEntry{}, false
	}
	return lastMutation, lastVerifyIdx < lastMutationIdx
}
