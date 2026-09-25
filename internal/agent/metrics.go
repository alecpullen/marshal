package agent

import (
	"fmt"
	"time"
	"unicode/utf8"

	"marshal/internal/llm/pricing"
	"marshal/internal/llm/schema"
	"marshal/internal/redact"
	"marshal/internal/strutil"
)

// TurnMetrics summarises one RunTask execution. It is emitted exactly once
// per turn via Runner.MetricsObserver, including on error exits, so every
// turn is measurable: outcome, iterations, parse failures, stalls, tokens.
type TurnMetrics struct {
	StartedAt  time.Time
	DurationMs int64
	Goal       string
	Class      string
	Role       string
	Provider   string
	Model      string
	Iterations int
	ToolCalls  int
	ToolErrors int
	CacheHits  int
	// ParseFailures counts unparseable JSON-envelope actions.
	// In native tool-calling mode this is always 0 because the provider
	// returns parsed tool_calls directly.
	ParseFailures int
	// ParseRepairs counts envelope deviations healed in place instead of
	// being bounced back for a regeneration. A rising count with a flat
	// ParseFailures means tolerance is absorbing a real prompt problem.
	ParseRepairs int
	// ParseFailKind labels the first parse failure of the turn:
	// "envelope" or "truncated_args". Empty on turns with no parse failure.
	ParseFailKind string
	// ParseFailSample is the redacted head (<=4 KB) of the first unparseable
	// model output this turn. Empty alongside ParseFailKind.
	ParseFailSample string
	// StreamRecoveries counts turns continued from a partial response after
	// the provider stream failed mid-flight (e.g. an undecodable SSE chunk).
	// A non-zero value means the turn survived an error that used to end it.
	StreamRecoveries int
	HardStalls       int
	Outcome          string
	// SalvageReason is non-empty when Outcome is "salvaged". Reasons are the
	// finalizeReason literals (see finalize.go): "exhausted",
	// "overhead_exhausted", "stalled", "repeat_failure", "malformed",
	// "empty", or "unverified".
	SalvageReason string
	// FailedRepeatStreak is the longest run of identical failed calls seen in
	// this turn; HighestFailedRepeatTier is how far the failure ladder
	// escalated for it (0 none, 2 nudge, 3 injected retry correction, 4 hard
	// stall). Together they make a recovery visible: without them, a nudge at
	// 2 that the model heeded is indistinguishable from a turn whose calls
	// never failed twice, which is exactly the feedback tuning the 2/3/4
	// thresholds requires.
	FailedRepeatStreak      int
	HighestFailedRepeatTier int
	PromptTokens            int
	CompletionTokens        int
	ReasoningTokens         int
	CacheReadTokens         int
	CacheWriteTokens        int
	// EstimatedCostCents is the estimated cost in hundredths of a cent
	// (1/10000 of a dollar), computed from the token counts and the
	// pricing table at metrics-emission time. 0 for local/unpriced models.
	EstimatedCostCents int64
}

// turnStats is the mutable per-turn collector behind TurnMetrics. It has no
// mutex of its own: Runner guards every access with statsMu (mirroring the
// tracker/trackerMu pattern), because executeActions mutates counters from
// worker goroutines.
type turnStats struct {
	m               TurnMetrics
	parseFailSample parseSample
}

// noteFailedRepeatTelemetry records how far the failure ladder escalated this
// turn. It keeps the maxima so a turn that recovers after a nudge still shows
// the tier it reached, and ignores tier 0 (no intervention was warranted) so
// an untouched turn reports zero.
func (r *Runner) noteFailedRepeatTelemetry(tier, streak int) {
	if tier <= 0 {
		return
	}
	r.withStats(func(s *turnStats) {
		if tier > s.m.HighestFailedRepeatTier {
			s.m.HighestFailedRepeatTier = tier
		}
		if streak > s.m.FailedRepeatStreak {
			s.m.FailedRepeatStreak = streak
		}
	})
}

// parseSample is the redacted head of the first unparseable model output
// this turn, kept so post-turn diagnosis can see what broke without
// re-running the model.
type parseSample struct {
	Kind string
	Text string
}

// parseFailSampleCap bounds the captured sample so a model rant cannot
// bloat the DB row.
const parseFailSampleCap = 4096

// truncateForSample clips raw to parseFailSampleCap bytes without ever
// emitting a partial rune: when the byte cap falls inside a multi-byte
// codepoint, the boundary backs off to the nearest preceding rune start. The
// result is always well-formed UTF-8 and never longer than parseFailSampleCap
// bytes. When the byte at the cap already begins a rune, behaviour is the
// plain byte clip.
func truncateForSample(raw string) string {
	if len(raw) <= parseFailSampleCap {
		return raw
	}
	n := parseFailSampleCap
	for n > 0 && !utf8.RuneStart(raw[n]) {
		n--
	}
	return raw[:n]
}

// noteParseFailure increments the ParseFailures counter and, on the first
// failure of the turn, captures a bounded redacted sample of the offending
// output. kind is "envelope" for ParseAction failures or "truncated_args"
// for the output-token-limit guard.
func (s *turnStats) noteParseFailure(kind, raw string) {
	s.m.ParseFailures++
	if s.parseFailSample.Kind == "" {
		s.parseFailSample = parseSample{Kind: kind, Text: redact.Secrets(truncateForSample(raw))}
	}
}

// outcomeFor maps a finished task to the metrics outcome vocabulary. Any
// status other than completed (failed, or executing after an interrupt)
// counts as failed.
func outcomeFor(task *Task) string {
	switch {
	case task.Status == TaskStatusCompleted && task.SalvagedReason == "":
		return "answered"
	case task.Status == TaskStatusCompleted:
		return "salvaged"
	default:
		return "failed"
	}
}

// withStats runs f with the current turn's collector under statsMu. It is a
// no-op before the first RunTask (stats nil), so direct calls to chatOnce or
// executeToolCall in tests never panic.
func (r *Runner) withStats(f func(*turnStats)) {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	if r.stats != nil {
		f(r.stats)
	}
}

// turnUsageLine formats the provider-reported token usage accumulated this
// turn for display in the session export. Empty when the provider reported
// no usage (e.g. local providers without usage fields).
func (r *Runner) turnUsageLine() string {
	line := ""
	r.withStats(func(s *turnStats) {
		p, c := s.m.PromptTokens, s.m.CompletionTokens
		if p == 0 && c == 0 {
			return
		}
		line = fmt.Sprintf("%s prompt + %s completion tokens", strutil.CompactTokens(p), strutil.CompactTokens(c))
	})
	return line
}

// countToolCall records one tool message fed back to the model.
func (r *Runner) countToolCall(errored, cached bool) {
	r.withStats(func(s *turnStats) {
		s.m.ToolCalls++
		if errored {
			s.m.ToolErrors++
		}
		if cached {
			s.m.CacheHits++
		}
	})
}

// noteInvalidArgs increments the per-round counter of tool calls rejected
// for schema violations. Cleared by RunTask at the start of each round.
func (r *Runner) noteInvalidArgs() {
	r.trackerMu.Lock()
	r.invalidArgsThisRound++
	r.trackerMu.Unlock()
}

// invalidArgsCount returns the count of schema-violation rejections for
// the current executeNativeToolCalls pass.
func (r *Runner) invalidArgsCount() int {
	r.trackerMu.Lock()
	defer r.trackerMu.Unlock()
	return r.invalidArgsThisRound
}

// emitMetrics finalizes the turn's metrics from the finished task and hands
// them to MetricsObserver. Called exactly once per RunTask via defer.
func (r *Runner) emitMetrics(task *Task) {
	if r.MetricsObserver == nil {
		return
	}
	r.statsMu.Lock()
	m := r.stats.m
	sample := r.stats.parseFailSample
	r.statsMu.Unlock()
	m.DurationMs = r.Now().Sub(m.StartedAt).Milliseconds()
	m.Class = string(task.Class)
	m.Outcome = outcomeFor(task)
	m.SalvageReason = task.SalvagedReason
	m.EstimatedCostCents = pricing.EstimateCostCents(schema.TokenUsage{
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		ReasoningTokens:  m.ReasoningTokens,
		CacheReadTokens:  m.CacheReadTokens,
		CacheWriteTokens: m.CacheWriteTokens,
	}, r.Pricing)
	m.ParseFailKind = sample.Kind
	m.ParseFailSample = sample.Text
	r.MetricsObserver(m)
}
