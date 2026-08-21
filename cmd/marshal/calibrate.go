package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"marshal/internal/db"
)

// runCalibrateTokens implements `marshal calibrate-tokens`, a read-only
// report on EstimatorCounter accuracy. With --from-db it aggregates the
// token_calibration rows already recorded for a project (by the per-turn
// calibration observer when [session.rollover.calibration] enabled = true).
// Without --from-db it prints a usage hint.
func runCalibrateTokens(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("calibrate-tokens", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fromDB := fs.Bool("from-db", false, "summarize recorded token_calibration rows for the project")
	projectDir := fs.String("project", "", "project directory (default: cwd)")
	session := fs.String("session", "", "limit to one session id")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*fromDB {
		fmt.Fprintln(stdout, "calibrate-tokens: pass --from-db to summarize recorded samples.")
		fmt.Fprintln(stdout, "To record samples, set [session.rollover] enabled = true and [session.rollover.calibration] enabled = true, then run sessions.")
		fmt.Fprintln(stdout, "Scope note: EstimatorCounter counts only message Content runes / 4; it does not count tool-call JSON, the system prompt, or role tokens. The ratio is estimator-vs-prompt_tokens, not estimator-vs-full-prompt.")
		return nil
	}

	dir := *projectDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve project dir: %w", err)
		}
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		return fmt.Errorf("open project database: %w", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	pid, err := database.GetOrCreateProject(dir, "")
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}

	sum, err := database.CalibrationSummary(pid, *session)
	if err != nil {
		return fmt.Errorf("calibration summary: %w", err)
	}
	if sum.Samples == 0 {
		fmt.Fprintln(stdout, "No calibration samples recorded for this project.")
		fmt.Fprintln(stdout, "Enable [session.rollover.calibration] and run sessions to record samples.")
		return nil
	}

	fmt.Fprintf(stdout, "EstimatorCounter calibration report\n")
	fmt.Fprintf(stdout, "  project dir: %s\n", dir)
	if *session != "" {
		fmt.Fprintf(stdout, "  session:     %s\n", *session)
	}
	fmt.Fprintf(stdout, "  samples:     %d\n", sum.Samples)
	fmt.Fprintf(stdout, "  mean estimator tokens: %.1f\n", sum.MeanEstimator)
	fmt.Fprintf(stdout, "  mean provider tokens:   %.1f\n", sum.MeanProvider)
	fmt.Fprintf(stdout, "  estimator:provider ratio (mean): %.3f\n", sum.MeanRatio)
	fmt.Fprintf(stdout, "  estimator:provider ratio (min):  %.3f\n", sum.MinRatio)
	fmt.Fprintf(stdout, "  estimator:provider ratio (max):  %.3f\n", sum.MaxRatio)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Interpretation: ratio < 1.0 means the estimator under-counts (safe for rollover,")
	fmt.Fprintln(stdout, "since UsageCounter takes the larger of the two). ratio > 1.0 means the estimator")
	fmt.Fprintln(stdout, "over-counts, which would roll over early but never overflow.")
	fmt.Fprintln(stdout, "Scope: estimator counts message Content runes / 4 only — not tool-call JSON,")
	fmt.Fprintln(stdout, "system prompt, or role tokens — so this is estimator-vs-prompt_tokens, not")
	fmt.Fprintln(stdout, "estimator-vs-full-prompt. A ratio near 1.0 means the heuristic tracks the")
	fmt.Fprintln(stdout, "provider's own count well for this workload.")
	return nil
}
