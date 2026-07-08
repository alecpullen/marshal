package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
func (t *progressTracker) record(name, normalizedArgs, resultHash string) int {
	if mutating(categorize(name)) {
		// State changed: earlier observations are stale, future repeats are
		// fresh progress.
		t.counts = make(map[string]int)
	}
	key := name + "\x00" + normalizedArgs + "\x00" + resultHash
	t.counts[key]++
	t.lastRepeat = t.counts[key]
	t.idleRun = 0
	t.history = append(t.history, callEntry{name: name, args: normalizedArgs})
	return t.lastRepeat
}

// recordIdle appends a synthetic idle entry so assess() can detect sustained
// silence (empty responses, declined ask_user).
func (t *progressTracker) recordIdle(reason string) {
	t.idleRun++
	t.history = append(t.history, callEntry{name: idleEntryName, args: reason})
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
