package scenario

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"marshal/test/usability/actor"
	"marshal/test/usability/harness"
	"marshal/test/usability/report"
	"marshal/test/usability/screen"
)

// RunnerConfig configures the scenario runner.
type RunnerConfig struct {
	BinaryPath string
	Width      int
	Height     int
	ReportDir  string
	WorkDir    string
	Env        []string
}

// Runner executes one scenario against Marshal.
type Runner struct {
	cfg RunnerConfig
	rep *report.Reporter
}

// NewRunner creates a runner.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Width == 0 {
		cfg.Width = 120
	}
	if cfg.Height == 0 {
		cfg.Height = 40
	}
	return &Runner{cfg: cfg, rep: report.New()}
}

// Scenario is one usability test. Success is whatever the actor reports in
// its done action: a scripted actor's postconditions live in its steps, not in
// a separate criterion.
type Scenario struct {
	Name      string
	ActorType string // metadata describing the actor, e.g. "scripted" or "llm"
	Fixture   string // optional path to a fixture repo; informational/metadata only
	Goal      string // optional task goal; informational/metadata only
	Actor     actor.Actor
	WorkDir   string
}

// Run executes the scenario and returns the result.
func (r *Runner) Run(ctx context.Context, sc Scenario) (report.ScenarioResult, error) {
	start := time.Now()
	result := report.ScenarioResult{Name: sc.Name, Actor: actorName(sc)}

	finish := func(err error) (report.ScenarioResult, error) {
		result.Duration = time.Since(start)
		r.rep.AddResult(result)
		return result, err
	}

	workDir := sc.WorkDir
	if workDir == "" {
		workDir = r.cfg.WorkDir
	}

	sess, err := harness.New(harness.Config{
		BinaryPath: r.cfg.BinaryPath,
		Width:      r.cfg.Width,
		Height:     r.cfg.Height,
		WorkDir:    workDir,
		Env:        r.cfg.Env,
	})
	if err != nil {
		result.Error = err.Error()
		return finish(err)
	}
	defer sess.Close()

	r.rep.Record("turn_started", map[string]any{"scenario": sc.Name, "work_dir": workDir})

	// Give Marshal a moment to render its first frame. A viewport of blank
	// rows is not a rendered UI, and a decoder error must fail the run rather
	// than wait out the timeout on a screen we cannot trust.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var decodeErr error
	_ = sess.WaitFor(waitCtx, func(snap harness.Snapshot) bool {
		scr, err := screen.Parse(snap)
		if err != nil {
			// Return true so the wait ends on its stability tick; the error is
			// handled below instead of being swallowed by the timeout.
			decodeErr = err
			return true
		}
		return strings.TrimSpace(scr.Content) != ""
	})
	if decodeErr != nil {
		r.recordFailure(sc, sess, "initial render", decodeErr)
		result.Error = decodeErr.Error()
		return finish(decodeErr)
	}

	maxDuration := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		maxDuration = time.Until(deadline)
	}
	scenarioCtx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	keystrokes := 0
	var lastScreen string
	for {
		select {
		case <-scenarioCtx.Done():
			if lastScreen != "" {
				r.rep.Record("screen_on_timeout", map[string]any{
					"scenario": sc.Name,
					"screen":   lastScreen,
					"raw_tail": diagnosticTail(sess.Output()),
				})
			}
			result.Error = "scenario timeout"
			return finish(scenarioCtx.Err())
		default:
		}

		scr, parseErr := screen.Parse(sess.Snapshot())
		if parseErr != nil {
			// A decoder error means the screen cannot be trusted: fail the
			// scenario instead of acting on a partial frame.
			r.recordFailure(sc, sess, "screen parse", parseErr)
			result.Error = parseErr.Error()
			return finish(parseErr)
		}
		if scr.Content != "" {
			lastScreen = scr.Content
		}
		act, err := sc.Actor.Act(scenarioCtx, scr)
		if err != nil {
			result.Error = err.Error()
			return finish(err)
		}

		r.rep.Record("actor_action", map[string]any{"scenario": sc.Name, "action": act})

		switch act.Type {
		case actor.ActionDone:
			result.Success = act.Success
			result.Keystrokes = keystrokes
			return finish(nil)
		case actor.ActionNoOp:
			// Wait a bit and re-observe.
			select {
			case <-scenarioCtx.Done():
				if lastScreen != "" {
					r.rep.Record("screen_on_timeout", map[string]any{"scenario": sc.Name, "screen": lastScreen})
				}
				result.Error = "scenario timeout during noop"
				return finish(scenarioCtx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			continue
		case actor.ActionType:
			if err := sess.Send(act.Text); err != nil {
				result.Error = err.Error()
				return finish(err)
			}
			keystrokes += len(act.Text)
		case actor.ActionKey:
			if err := sess.SendKey(act.Key); err != nil {
				result.Error = err.Error()
				return finish(err)
			}
			keystrokes++
		default:
			result.Error = fmt.Sprintf("unknown action type %q", act.Type)
			return finish(fmt.Errorf("unknown action type %q", act.Type))
		}
	}
}

// WriteReport flushes reports to ReportDir or a temp dir.
func (r *Runner) WriteReport() error {
	dir := r.cfg.ReportDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "marshal-usability")
	}
	return r.rep.WriteReport(dir)
}

// recordFailure records a failure together with the visible screen and a
// bounded raw output tail. The raw bytes are diagnostic only; no assertion
// reads them.
func (r *Runner) recordFailure(sc Scenario, sess *harness.Session, cause string, causeErr error) {
	fields := map[string]any{
		"scenario": sc.Name,
		"cause":    cause,
		"raw_tail": diagnosticTail(sess.Output()),
	}
	if causeErr != nil {
		fields["error"] = causeErr.Error()
	}
	if snap := sess.Snapshot(); snap.Err == nil {
		fields["screen"] = string(snap.Content)
	}
	r.rep.Record("scenario_failure", fields)
}

// diagnosticTail keeps the tail of the raw PTY output small enough to be
// useful in a report.
func diagnosticTail(raw []byte) string {
	const limit = 4 << 10
	if len(raw) > limit {
		raw = raw[len(raw)-limit:]
	}
	return string(raw)
}

func actorName(sc Scenario) string {
	if sc.ActorType != "" {
		return sc.ActorType
	}
	return fmt.Sprintf("%T", sc.Actor)
}
