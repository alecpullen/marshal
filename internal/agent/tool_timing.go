package agent

import (
	"context"
	"log/slog"
	"time"

	"marshal/internal/strutil"
)

// Durations belong to one invocation, including when workers execute in
// parallel. A missing phase differs from a measured zero-duration phase.
type toolPhaseTime struct {
	elapsed  time.Duration
	measured bool
}

type toolTiming struct {
	now                                                  func() time.Time
	started                                              time.Time
	tool                                                 string
	callID                                               string
	hold, policyApproval, hooks, snapshot, gate, handler toolPhaseTime
	questions                                            toolPhaseTime
	cacheHit, handlerError                               bool
}

func (t *toolTiming) measure(phase *toolPhaseTime) func() {
	start := t.now()
	phase.measured = true
	return func() { phase.elapsed += max(time.Duration(0), t.now().Sub(start)) }
}

// emit runs after the call's cleanup, so total includes gate release, audit
// persistence and result processing. Existing audit/process timers are left
// intact. No prompts, arguments, output or error text enter this record.
func (t *toolTiming) emit(logger *slog.Logger, returnedErr error) {
	total := max(time.Duration(0), t.now().Sub(t.started))
	if !logger.Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	attrs := []slog.Attr{
		slog.String("tool", strutil.Truncate(t.tool, 128, false)),
		slog.String("call_id", strutil.Truncate(t.callID, 128, false)),
		slog.Int64("total_ns", int64(total)),
		slog.Bool("cache_hit", t.cacheHit),
		slog.Bool("returned_error", returnedErr != nil),
		slog.Bool("handler_error", t.handlerError),
	}
	for _, p := range []struct {
		name  string
		phase toolPhaseTime
	}{
		{"hold_ns", t.hold}, {"policy_approval_ns", t.policyApproval},
		{"hooks_ns", t.hooks}, {"snapshot_ns", t.snapshot},
		{"workspace_gate_ns", t.gate}, {"handler_ns", t.handler},
		{"question_wait_ns", t.questions},
	} {
		if p.phase.measured {
			attrs = append(attrs, slog.Int64(p.name, int64(p.phase.elapsed)))
		}
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "tool timing", attrs...)
}
