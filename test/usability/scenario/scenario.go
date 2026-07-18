package scenario

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// SuccessCriterion describes how to judge a scenario. For LLM scenarios the judge is separate.
type SuccessCriterion struct {
	ScreenContains string // for scripted assertions, handled by the actor
}

// Scenario is one usability test.
type Scenario struct {
	Name    string
	Actor   actor.Actor
	WorkDir string
	Success SuccessCriterion
}

// Run executes the scenario and returns the result.
func (r *Runner) Run(ctx context.Context, sc Scenario) (report.ScenarioResult, error) {
	start := time.Now()
	result := report.ScenarioResult{Name: sc.Name, Actor: actorName(sc.Actor)}

	workDir := sc.WorkDir
	if workDir == "" {
		workDir = r.cfg.WorkDir
	}

	sess, err := harness.New(harness.Config{
		BinaryPath: r.cfg.BinaryPath,
		Width:      r.cfg.Width,
		Height:     r.cfg.Height,
		WorkDir:    workDir,
	})
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer sess.Close()

	r.rep.Record("turn_started", map[string]any{"scenario": sc.Name, "work_dir": workDir})

	// Give Marshal a moment to render initial UI.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = sess.WaitFor(waitCtx, func(snap harness.Snapshot) bool {
		scr, _ := screen.Parse(snap)
		return scr.Content != ""
	})

	maxDuration := 2 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		maxDuration = time.Until(deadline)
	}
	scenarioCtx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	keystrokes := 0
	for {
		select {
		case <-scenarioCtx.Done():
			result.Error = "scenario timeout"
			result.Duration = time.Since(start)
			r.rep.AddResult(result)
			return result, scenarioCtx.Err()
		default:
		}

		scr, _ := screen.Parse(sess.Snapshot())
		act, err := sc.Actor.Act(scenarioCtx, scr)
		if err != nil {
			result.Error = err.Error()
			result.Duration = time.Since(start)
			r.rep.AddResult(result)
			return result, err
		}

		r.rep.Record("actor_action", map[string]any{"scenario": sc.Name, "action": act})

		switch act.Type {
		case actor.ActionDone:
			result.Success = act.Success
			result.Duration = time.Since(start)
			result.Keystrokes = keystrokes
			r.rep.AddResult(result)
			return result, nil
		case actor.ActionNoOp:
			// Wait a bit and re-observe.
			select {
			case <-scenarioCtx.Done():
				result.Error = "scenario timeout during noop"
				result.Duration = time.Since(start)
				r.rep.AddResult(result)
				return result, scenarioCtx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		case actor.ActionType:
			if err := sess.Send(act.Text); err != nil {
				result.Error = err.Error()
				return result, err
			}
			keystrokes += len(act.Text)
		case actor.ActionKey:
			if err := sess.SendKey(act.Key); err != nil {
				result.Error = err.Error()
				return result, err
			}
			keystrokes++
		default:
			result.Error = fmt.Sprintf("unknown action type %q", act.Type)
			return result, fmt.Errorf("unknown action type %q", act.Type)
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

func actorName(a actor.Actor) string {
	switch a.(type) {
	default:
		return fmt.Sprintf("%T", a)
	}
}
