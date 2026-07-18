package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Event is a single observable action during a scenario.
type Event struct {
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload,omitempty"`
}

// ScenarioResult aggregates one scenario's outcome.
type ScenarioResult struct {
	Name       string        `json:"name"`
	Actor      string        `json:"actor"`
	Success    bool          `json:"success"`
	Duration   time.Duration `json:"duration"`
	Keystrokes int           `json:"keystrokes"`
	Error      string        `json:"error,omitempty"`
}

// Reporter collects events and results.
type Reporter struct {
	events  []Event
	results []ScenarioResult
	start   time.Time
}

// New creates a Reporter.
func New() *Reporter {
	return &Reporter{start: time.Now()}
}

// Record adds an event.
func (r *Reporter) Record(kind string, payload map[string]any) {
	r.events = append(r.events, Event{
		Time:    time.Now(),
		Kind:    kind,
		Payload: payload,
	})
}

// AddResult adds a scenario outcome.
func (r *Reporter) AddResult(res ScenarioResult) {
	r.results = append(r.results, res)
}

// WriteReport writes JSON and Markdown artifacts to dir.
func (r *Reporter) WriteReport(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	report := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"events":       r.events,
		"results":      r.results,
	}
	if err := writeJSON(filepath.Join(dir, "usability-report.json"), report); err != nil {
		return err
	}

	bench := r.buildBenchmark()
	if err := writeJSON(filepath.Join(dir, "usability-benchmark.json"), bench); err != nil {
		return err
	}

	return writeFrictionLog(filepath.Join(dir, "friction-log.md"), r.results, r.events)
}

func (r *Reporter) buildBenchmark() map[string]any {
	total := len(r.results)
	if total == 0 {
		return map[string]any{"total": 0, "success_rate": 0}
	}
	passed := 0
	var totalDuration time.Duration
	for _, res := range r.results {
		if res.Success {
			passed++
		}
		totalDuration += res.Duration
	}
	return map[string]any{
		"total":            total,
		"passed":           passed,
		"failed":           total - passed,
		"success_rate":     float64(passed) / float64(total),
		"mean_duration_ms": totalDuration.Milliseconds() / int64(total),
	}
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeFrictionLog(path string, results []ScenarioResult, events []Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# Usability Friction Log\n\n")
	fmt.Fprintf(f, "Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, res := range results {
		status := "PASS"
		if !res.Success {
			status = "FAIL"
		}
		fmt.Fprintf(f, "## %s — %s\n\n", res.Name, status)
		fmt.Fprintf(f, "- Actor: %s\n- Duration: %s\n- Keystrokes: %d\n", res.Actor, res.Duration, res.Keystrokes)
		if res.Error != "" {
			fmt.Fprintf(f, "- Error: %s\n", res.Error)
		}
		fmt.Fprintln(f)
		fmt.Fprintf(f, "### Events\n\n")
		for _, ev := range events {
			if name, ok := ev.Payload["scenario"].(string); ok && name == res.Name {
				fmt.Fprintf(f, "- %s %s: %v\n", ev.Time.Format("15:04:05"), ev.Kind, ev.Payload)
			}
		}
		fmt.Fprintln(f)
	}
	return nil
}
