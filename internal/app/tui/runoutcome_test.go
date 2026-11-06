package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

func succeededRun() acpwire.SDDProgress {
	return acpwire.SDDProgress{
		Finished: true, Succeeded: true,
		PlanName: "add-retries", Branch: "pipeline/add-retries", BaseRef: "main",
		LedgerPath:   "/repo/.marshal/pipeline/add-retries/ledger.md",
		ArtifactsDir: "/repo/.marshal/pipeline/add-retries",
		TotalTasks:   4, DoneTasks: 4,
		StartedAt: acpwire.Millis(time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)),
		EndedAt:   acpwire.Millis(time.Date(2026, 8, 7, 10, 23, 41, 0, time.UTC)),
	}
}

func failedRun() acpwire.SDDProgress {
	p := succeededRun()
	p.Succeeded = false
	p.DoneTasks = 2
	p.Error = "task 3 still fails `go test ./...` after 3 fix rounds"
	return p
}

func rowTexts(doc acpwire.WireDoc) []string {
	var out []string
	var walk func(rows []acpwire.WireRow)
	walk = func(rows []acpwire.WireRow) {
		for _, r := range rows {
			out = append(out, r.Header+r.Text+r.Detail+r.ActionLabel)
			walk(r.Children)
		}
	}
	walk(doc.Rows)
	if doc.Footer != "" {
		out = append(out, doc.Footer)
	}
	return out
}

func TestOutcomeDocSuccessOffersTheBranchDiff(t *testing.T) {
	doc := runOutcomeDoc(succeededRun(), time.Now())
	joined := strings.Join(rowTexts(doc), "\n")

	for _, want := range []string{"pipeline/add-retries", "4/4", "diff", "ledger", "artifacts"} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Errorf("outcome doc missing %q — the user has no route to their work:\n%s", want, joined)
		}
	}
}

func TestOutcomeDocFailureKeepsTheReason(t *testing.T) {
	doc := runOutcomeDoc(failedRun(), time.Now())
	joined := strings.Join(rowTexts(doc), "\n")

	if !strings.Contains(joined, "go test ./...") {
		t.Errorf("a failed run must keep its reason reachable, not truncate it to 80 chars:\n%s", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "2/4") {
		t.Errorf("a failed run must show how far it got:\n%s", joined)
	}
}

func TestOutcomeDocFailureOffersResume(t *testing.T) {
	doc := runOutcomeDoc(failedRun(), time.Now())
	joined := strings.Join(rowTexts(doc), "\n")
	if !strings.Contains(joined, sddResumeHint) {
		t.Errorf("a failed run must say how to resume, in the shared wording:\n%s", joined)
	}
}

func TestOutcomeDocSuccessDoesNotOfferResume(t *testing.T) {
	doc := runOutcomeDoc(succeededRun(), time.Now())
	if strings.Contains(strings.Join(rowTexts(doc), "\n"), sddResumeHint) {
		t.Error("a completed run has nothing to resume")
	}
}

func TestOutcomeDocHandlesMissingBranchGracefully(t *testing.T) {
	p := succeededRun()
	p.Branch = ""
	doc := runOutcomeDoc(p, time.Now())
	if len(doc.Rows) == 0 {
		t.Error("a run with no branch must still render an outcome, not an empty panel")
	}
}

func TestOutcomeDocWithNoRunAtAll(t *testing.T) {
	doc := runOutcomeDoc(acpwire.SDDProgress{}, time.Now())
	joined := strings.Join(rowTexts(doc), "\n")
	if !strings.Contains(strings.ToLower(joined), "no plan run") {
		t.Errorf("with no run recorded the doc must say so plainly:\n%s", joined)
	}
}

func activeRun() (acpwire.SDDProgress, time.Time) {
	p := etaTestProgress()
	p.PlanName = "add-retries"
	p.Branch = "pipeline/add-retries"
	p.BaseRef = "main"
	p.LedgerPath = "/repo/.marshal/pipeline/add-retries/ledger.md"
	return p, etaTestNow(p)
}

func TestOutcomeDocMidRunSaysRunningNotStopped(t *testing.T) {
	p, now := activeRun()
	doc := runOutcomeDoc(p, now)
	joined := strings.Join(rowTexts(doc), "\n")

	if strings.Contains(joined, "stopped") {
		t.Errorf("an in-flight run must not be described as stopped:\n%s", joined)
	}
	if !strings.Contains(joined, "running") {
		t.Errorf("an in-flight run must say it is running:\n%s", joined)
	}
}

func TestOutcomeDocMidRunElapsedIsPositive(t *testing.T) {
	p, now := activeRun()
	doc := runOutcomeDoc(p, now)
	joined := strings.Join(rowTexts(doc), "\n")

	if strings.Contains(joined, "-") && strings.Contains(joined, "9223372036") {
		t.Errorf("elapsed computed from a zero EndedAt overflowed:\n%s", joined)
	}
	if !strings.Contains(joined, "16m") {
		t.Errorf("elapsed must be measured against now for a live run:\n%s", joined)
	}
}

func TestOutcomeDocMidRunListsThePlan(t *testing.T) {
	p, now := activeRun()
	p.Tasks = []string{"Add config field", "Wire the resolver", "Add retry helper",
		"Thread the timeout", "Add the metrics hook", "Wire the config flag", "Document the knob"}
	doc := runOutcomeDoc(p, now)
	joined := strings.Join(rowTexts(doc), "\n")

	for _, want := range []string{"Add config field", "Thread the timeout", "Document the knob"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan section must list every task; missing %q:\n%s", want, joined)
		}
	}
	// Completed tasks carry their duration; pending tasks do not.
	if !strings.Contains(joined, "2m 2s") {
		t.Errorf("a completed task must show its duration:\n%s", joined)
	}
}

func TestOutcomeDocMidRunShowsProgressAndETA(t *testing.T) {
	p, now := activeRun()
	doc := runOutcomeDoc(p, now)
	joined := strings.Join(rowTexts(doc), "\n")

	for _, want := range []string{"4/7", "43%", "~6–25m left"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress row missing %q:\n%s", want, joined)
		}
	}
}

func TestOutcomeDocFinishedPathUnchanged(t *testing.T) {
	doc := runOutcomeDoc(succeededRun(), time.Now())
	joined := strings.Join(rowTexts(doc), "\n")

	if !strings.Contains(joined, "completed") {
		t.Errorf("the finished path must still say completed:\n%s", joined)
	}
	if strings.Contains(joined, "left") {
		t.Errorf("a finished run has no remaining-time estimate:\n%s", joined)
	}
}

func TestRunOutcomeShowsExecutionType(t *testing.T) {
	p := succeededRun()
	p.Tasks = []string{"Add config field", "Wire the resolver", "Add retry helper"}
	p.TaskExecTypes = []string{"deterministic", "deterministic + agent", "deterministic"}
	doc := runOutcomeDoc(p, time.Now())
	found := false
	for _, row := range doc.Rows {
		if row.Detail != "" && strings.Contains(row.Detail, "deterministic") {
			found = true
		}
	}
	if !found {
		t.Errorf("runOutcomeDoc should show execution type in task detail:\n%s", doc.Title)
	}
}

// The outcome doc is built in the TUI, and both of its action rows read
// through the session: the ledger row's text is the file's contents.
func TestOutcomeActionsReadThroughTheSession(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, ".marshal", "pipeline", "add-retries", "progress.md")
	if err := os.MkdirAll(filepath.Dir(ledger), 0o755); err != nil {
		t.Fatalf("mkdir ledger dir: %v", err)
	}
	if err := os.WriteFile(ledger, []byte("ledger body"), 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
	_, sess := inproc.New(inproc.Deps{State: state})

	p := succeededRun()
	p.LedgerPath = ledger

	doc := runOutcomeDoc(p, time.Now())
	var found bool
	for _, row := range doc.Rows {
		if row.Text == "ledger" {
			if row.ActionID != outcomeLedgerAction {
				t.Fatalf("ledger row ActionID = %q, want %q", row.ActionID, outcomeLedgerAction)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("the outcome doc has no ledger row")
	}

	acts := runOutcomeActions(context.Background(), sess, p, dir, 100)
	run, ok := acts[outcomeLedgerAction]
	if !ok {
		t.Fatalf("no %q action in the map", outcomeLedgerAction)
	}
	if got := run().Text; got != "ledger body" {
		t.Fatalf("ledger action text = %q, want the file's contents", got)
	}

	// A ledger past the session's read cap says so instead of passing the cut
	// text off as the whole file.
	big := filepath.Join(dir, ".marshal", "pipeline", "add-retries", "big.md")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatalf("write big ledger: %v", err)
	}
	p.LedgerPath = big
	acts = runOutcomeActions(context.Background(), sess, p, dir, 100)
	run, ok = acts[outcomeLedgerAction]
	if !ok {
		t.Fatalf("no %q action in the map for the big ledger", outcomeLedgerAction)
	}
	bigText := run().Text
	if !strings.Contains(bigText, "(truncated: the ledger is larger than the session's read limit)") {
		t.Fatalf("big ledger text does not mention the truncation, got %d bytes", len(bigText))
	}
	if len(bigText) < 1<<20 {
		t.Fatalf("big ledger text is %d bytes, want the 1 MiB the session returns", len(bigText))
	}

	// A ledger outside the working directory is refused, not read.
	outside := filepath.Join(t.TempDir(), "ledger.md")
	if err := os.WriteFile(outside, []byte("elsewhere"), 0o600); err != nil {
		t.Fatalf("write outside ledger: %v", err)
	}
	p.LedgerPath = outside
	acts = runOutcomeActions(context.Background(), sess, p, dir, 100)
	run, ok = acts[outcomeLedgerAction]
	if !ok {
		t.Fatalf("no %q action in the map for the outside ledger", outcomeLedgerAction)
	}
	want := "Could not read " + outside + ": it is outside the working directory"
	if got := run().Text; got != want {
		t.Fatalf("outside ledger text = %q, want %q", got, want)
	}
}
