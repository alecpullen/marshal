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

// mutating reports whether a category of tool call can change repository or
// system state. After a mutating call, previously gathered observations are
// stale, so repeating an earlier call counts as fresh progress again.
func mutating(cat toolCategory) bool {
	return cat == catShell || cat == catWrite || cat == catPatch
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
	if success && mutating(categorize(name)) {
		// State actually changed: earlier observations are stale, future
		// repeats are fresh progress. A FAILING mutating call changes
		// nothing, so it must not reset the streak — otherwise identical
		// futile calls (e.g. the same malformed patch) never accumulate
		// enough repeats to trip loop detection.
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

// verificationCommandPatterns match shell.run argument payloads that are
// themselves a verification step (running tests, vet, lint, or a build).
// Matching happens against the raw normalized args JSON, e.g.
// {"command":"go test ./..."}.
var verificationCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgo\s+(test|vet|build)\b`),
	regexp.MustCompile(`\bnpm\s+(run\s+)?test\b`),
	regexp.MustCompile(`\b(pnpm|yarn)\s+(run\s+)?test\b`),
	regexp.MustCompile(`\bpytest\b`),
	regexp.MustCompile(`\bcargo\s+(test|check|clippy|build)\b`),
	regexp.MustCompile(`\bmake\s+(test|check)\b`),
}

// looksLikeVerificationCommand reports whether a shell.run args payload is a
// test/vet/lint/build invocation. shell.run is dual-use: it is how models
// both mutate (install, generate, delete) and verify (go test), so the
// finalize gate cannot treat every shell call as a mutation. Only the
// "command" field is matched — matching the whole args JSON would let an
// unrelated field (e.g. a background note or an env hint containing the word
// "pytest") count as verification and let a mutation masquerade as one.
func looksLikeVerificationCommand(args string) bool {
	var a struct {
		Command string `json:"command"`
	}
	if len(args) == 0 || json.Unmarshal([]byte(args), &a) != nil {
		return false
	}
	for _, p := range verificationCommandPatterns {
		if p.MatchString(a.Command) {
			return true
		}
	}
	return false
}

// housekeepingCommandPatterns match shell.run command payloads that change
// no code behavior: VCS bookkeeping and read-only queries, file
// housekeeping, formatters, and package installs. They must neither arm nor
// satisfy the verification gate. Deliberately conservative: commands that
// alter working-tree CONTENT (rm, git checkout/switch/restore) are not here
// and still arm the gate.
var housekeepingCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgit\s+(add|commit|tag|push|fetch|pull|status|log|show|diff|branch)\b`),
	regexp.MustCompile(`\b(mkdir|cp|mv|touch|chmod|ln)\b`),
	regexp.MustCompile(`\bgofmt\b`),
	regexp.MustCompile(`\bgo\s+mod\s+(tidy|download|vendor)\b`),
	regexp.MustCompile(`\bnpm\s+(install|ci)\b`),
	regexp.MustCompile(`\b(pnpm|yarn)\s+(install|add)\b`),
	regexp.MustCompile(`\bpip\s+install\b`),
}

// looksLikeHousekeepingCommand reports whether a shell.run args payload is
// a housekeeping command. Like looksLikeVerificationCommand, only the
// "command" field is matched.
func looksLikeHousekeepingCommand(args string) bool {
	var a struct {
		Command string `json:"command"`
	}
	if len(args) == 0 || json.Unmarshal([]byte(args), &a) != nil {
		return false
	}
	for _, p := range housekeepingCommandPatterns {
		if p.MatchString(a.Command) {
			return true
		}
	}
	return false
}

// unverifiedMutation reports the last successful mutating call and whether
// it lacks any later verification call. Verification means test.run,
// diagnostics.check, or a test-like shell.run. Only a SUCCESSFUL mutation
// counts (a failed mutation changes nothing, so it cannot arm the gate), but
// a verification call counts whether or not it succeeded: test.run and
// shell.run surface a non-zero exit as a tool error, so a red test is recorded
// as a failed call — and it must still satisfy the gate. Otherwise a model
// that honestly runs tests, finds them red, and reports "tests still fail"
// would be penalized identically to one that never verified at all, which is
// the opposite of what the gate is for. The finalize gate deliberately does
// not loop until tests pass; it nudges once and then flags the answer as
// unverified.
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
		if e.name == "shell.run" && looksLikeHousekeepingCommand(e.args) {
			// Housekeeping (git commit, mkdir, installs) changes no code
			// behavior: it neither arms the gate nor satisfies it.
			continue
		}
		if e.ok && mutating(categorize(e.name)) {
			lastMutationIdx = i
			lastMutation = e
		}
	}
	if lastMutationIdx < 0 {
		return callEntry{}, false
	}
	return lastMutation, lastVerifyIdx < lastMutationIdx
}
