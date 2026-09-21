package agent

import (
	"errors"
	"strings"

	"marshal/internal/llm/schema"
)

// Thinking-loop detection thresholds. Fixed constants with no config surface,
// mirroring the existing tuning precedent (defaultReconnectMaxWait in chat.go):
// the failure mode is unambiguous and a knob would only invite tuning a value
// nobody can tune by feel.
const (
	// loopMinBlock is the smallest repeated unit, in bytes, that counts as a
	// loop. Below this a repeated fragment is ordinary reasoning rhythm.
	loopMinBlock = 200
	// loopMinRepeats is how many consecutive repetitions of a
	// loopMinBlock-byte block must sit at the tail of the window before the
	// stream is aborted.
	loopMinRepeats = 3
	// loopWindow is the rolling tail window, in bytes, that the detector keeps
	// and scans. A repetition whose period exceeds loopWindow/loopMinRepeats
	// can never be confirmed and is an accepted loss.
	loopWindow = 8192
	// maxThinkingLoopRetries bounds the nudge-retry: one resend with a system
	// note appended. One is the minimum that can recover a model which merely
	// lost the thread, without spending a second full request timeout on a
	// model that is genuinely stuck.
	maxThinkingLoopRetries = 1
)

// thinkingLoopNudge is appended as a trailing system message to the retry
// request only. It is never added to the conversation record.
const thinkingLoopNudge = "Your previous reasoning entered a repetitive loop and was cut off. Do not repeat that line of reasoning; be concise and move forward."

// errThinkingLoop is the sentinel returned when the detector confirmed a loop
// and the stream was aborted. chatOnce wraps it with the repeated snippet;
// isRetryableChatError must refuse to retry it.
var errThinkingLoop = errors.New("thinking loop detected")

// loopDetector watches a stream of thinking deltas for verbatim repetition.
// Construct one per attempt; a detector that has fired stays fired, because the
// caller aborts immediately.
type loopDetector struct {
	buf string // normalized rolling tail window of thinking text
	pi  []int  // reused KMP prefix-function scratch over the reversed window
}

func newLoopDetector() *loopDetector { return &loopDetector{} }

// feed appends a thinking delta and reports whether the buffer's tail is a
// confirmed loop, returning the repeated block so the caller can quote a
// snippet in logs and errors.
//
// Normalization collapses every run of whitespace to a single space before
// comparison, so a loop that drifts by line breaks or indentation is still a
// loop. That is the whole of the tolerance: no fuzzy or similarity matching.
func (d *loopDetector) feed(delta string) (bool, string) {
	d.buf = appendNormalized(d.buf, delta)
	if len(d.buf) > loopWindow {
		// Tail-kept: a loop that is still repeating has its most recent
		// repetitions at the end, which is what matters.
		d.buf = d.buf[len(d.buf)-loopWindow:]
	}
	return d.detect()
}

// appendNormalized appends delta to buf with runs of whitespace collapsed to a
// single space. The trailing-space state is derived from buf rather than
// tracked across calls so the function stays pure.
//
// It walks delta by rune, so invalid UTF-8 is rewritten as U+FFFD and the
// result is not byte-faithful to its input — it can even be longer than
// len(buf)+len(delta). Detection is unaffected: identical byte sequences
// normalize identically, and both the window and the reported block are only
// ever compared against themselves.
func appendNormalized(buf, delta string) string {
	if delta == "" {
		return buf
	}
	var sb strings.Builder
	sb.Grow(len(buf) + len(delta))
	sb.WriteString(buf)
	prevSpace := strings.HasSuffix(buf, " ")
	for _, r := range delta {
		switch r {
		case ' ', '\t', '\n', '\r':
			if !prevSpace {
				sb.WriteByte(' ')
				prevSpace = true
			}
		default:
			sb.WriteRune(r)
			prevSpace = false
		}
	}
	return sb.String()
}

// detect reports whether some suffix of the window is a confirmed loop, and
// returns the repeated block.
//
// It walks every suffix length from the smallest that could possibly qualify up
// to the whole window. For each suffix, its shortest period p is
// length - pi[length-1], where pi is the prefix function of the reversed window
// (periods of a string correspond one-to-one with its borders, so the longest
// border gives the shortest period). The suffix is therefore exactly
// floor(length/p) copies of its final p bytes.
//
// Two rules turn that into the spec's "a >= loopMinBlock block repeated
// >= loopMinRepeats times":
//
//   - The period is grown to the loopMinBlock floor: m = ceil(loopMinBlock/p)
//     copies of the period are themselves a p*m-byte block, and a 30-byte
//     sentence repeated three hundred times is a loop even though no single
//     30-byte unit qualifies.
//   - The suffix must hold loopMinRepeats of those blocks, i.e.
//     length >= loopMinRepeats*p*m.
//
// Scanning lengths upward and reporting the first match keeps the reported
// block as close to the repetition as possible. Detection does not depend on
// the whole window being periodic, so reasoning that only *ends* in a loop is
// still caught.
//
// The sharpest false positive the rule allows is p = 1: 600 identical
// non-whitespace bytes — a long decorative divider inside a thinking block,
// say — confirm as a loop. That is the verbatim rule at its limit, and the
// cost is one cancelled attempt plus one nudge retry, not a failed turn.
func (d *loopDetector) detect() (bool, string) {
	s := d.buf
	n := len(s)
	if n < loopMinBlock*loopMinRepeats {
		return false, ""
	}
	pi := d.suffixFn(s)
	for length := loopMinBlock * loopMinRepeats; length <= n; length++ {
		// p >= 1 always: the prefix function satisfies pi[i] <= i, so
		// length-pi[length-1] >= 1. No guard needed.
		p := length - pi[length-1]
		m := (loopMinBlock + p - 1) / p
		block := p * m
		if length < loopMinRepeats*block {
			continue
		}
		return true, s[n-block:]
	}
	return false, ""
}

// suffixFn returns the KMP prefix function of the reversed window: pi[i] is the
// length of the longest border of the suffix of s of length i+1. It reads s
// backwards through the index mapping r[j] = s[n-1-j] rather than copying and
// reversing the window, which for an 8KB window would allocate on every
// thinking delta. The scratch slice is kept on the detector for the same
// reason.
func (d *loopDetector) suffixFn(s string) []int {
	n := len(s)
	if cap(d.pi) < n {
		d.pi = make([]int, n)
	}
	pi := d.pi[:n]
	pi[0] = 0
	for i := 1; i < n; i++ {
		j := pi[i-1]
		for j > 0 && s[n-1-i] != s[n-1-j] {
			j = pi[j-1]
		}
		if s[n-1-i] == s[n-1-j] {
			j++
		}
		pi[i] = j
	}
	return pi
}

// withThinkingLoopNudge returns a copy of messages with the nudge appended as a
// trailing system message. The caller's slice is never mutated in place, so the
// nudge stays out of the conversation record and out of every other caller's
// view.
func withThinkingLoopNudge(messages []schema.ChatMessage) []schema.ChatMessage {
	out := make([]schema.ChatMessage, 0, len(messages)+1)
	out = append(out, messages...)
	return append(out, schema.ChatMessage{Role: schema.RoleSystem, Content: thinkingLoopNudge})
}

// thinkingLoopEscalateAt is the abort count per provider|model at which the
// runner raises its one escalation warning.
const thinkingLoopEscalateAt = 3

// noteThinkingLoopAbort records a confirmed mid-stream abort for provider|model
// and logs one escalation warning when the count reaches thinkingLoopEscalateAt.
// The counter is runner-lifetime and never persisted; the nil zero value is
// safe, mirroring warnTemperatureLockedOnce.
func (r *Runner) noteThinkingLoopAbort(provider, model string) {
	key := provider + "|" + model
	r.thinkingLoopMu.Lock()
	if r.thinkingLoopCounts == nil {
		r.thinkingLoopCounts = make(map[string]int)
	}
	r.thinkingLoopCounts[key]++
	count := r.thinkingLoopCounts[key]
	r.thinkingLoopMu.Unlock()

	if count == thinkingLoopEscalateAt {
		r.State.Logger().Warn("model repeatedly enters reasoning loops; consider switching models or lowering the thinking budget",
			"provider", provider,
			"model", model,
			"aborts", count)
	}
}
