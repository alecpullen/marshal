package scenario

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"marshal/test/usability/actor"
	"marshal/test/usability/actor/scripted"
	"marshal/test/usability/screen"
)

// writeHelper writes an executable /bin/sh script that runs body.
func writeHelper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return path
}

func strPtr(s string) *string { return &s }

// pasteResizeHelper acknowledges a bracketed paste by echoing the exact bytes
// it received as hex, and reports the window size it observes whenever a line
// arrives. COUNT is replaced with the framing length before the script runs.
const pasteResizeHelper = `
stty raw -echo
printf 'READY\r\n'
printf 'SIZE:%s\r\n' "$(stty size)"
pasted=$(dd bs=1 count=COUNT 2>/dev/null | od -An -v -tx1 | tr -d ' \n')
printf 'PASTE:%s\r\n' "$pasted"
stty sane
stty -echo
while read -r line; do
  printf 'SIZE:%s\r\n' "$(stty size)"
done
`

// TestRunnerPasteAndResize drives the runner's paste and resize dispatch. The
// script waits for both the exact bracketed-paste acknowledgement and the
// observed window size before it succeeds.
func TestRunnerPasteAndResize(t *testing.T) {
	t.Run("PasteAndResize", func(t *testing.T) {
		const payload = "héllo ☃"
		framed := "\x1b[200~" + payload + "\x1b[201~"
		wantAck := "PASTE:" + hex.EncodeToString([]byte(framed))

		bin := writeHelper(t, strings.ReplaceAll(pasteResizeHelper, "COUNT", strconv.Itoa(len(framed))))

		scr := &scripted.Scripted{
			Name: "paste and resize",
			Steps: []scripted.Step{
				{Paste: strPtr(payload), WaitFor: scripted.WaitFor{ScreenContains: "READY"}},
				{WaitOnly: true, WaitFor: scripted.WaitFor{ScreenContains: wantAck}},
				{Resize: &actor.Size{Width: 100, Height: 30}, WaitFor: scripted.WaitFor{ScreenContains: "PASTE:"}},
				{SendKey: "enter", WaitFor: scripted.WaitFor{ScreenContains: "SIZE:24 80"}},
				{WaitOnly: true, WaitFor: scripted.WaitFor{ScreenContains: "SIZE:30 100"}},
			},
		}

		r := NewRunner(RunnerConfig{BinaryPath: bin, Width: 80, Height: 24, WorkDir: t.TempDir()})
		res, err := r.Run(context.Background(), Scenario{Name: "paste-resize", ActorType: "scripted", Actor: scr})
		if err != nil {
			t.Fatalf("Run: %v; result = %+v", err, res)
		}
		if !res.Success {
			t.Fatalf("Success = false, want true; result = %+v", res)
		}
		// The paste counts by payload bytes, the enter by one; resize and
		// wait-only steps add nothing.
		if want := len(payload) + 1; res.Keystrokes != want {
			t.Fatalf("Keystrokes = %d, want %d", res.Keystrokes, want)
		}
	})

	t.Run("InvalidSizeFails", func(t *testing.T) {
		bin := writeHelper(t, "\nprintf 'READY\\r\\n'\nsleep 5\n")
		act := &stubActor{actions: []actor.Action{
			{Type: actor.ActionResize, Width: 0, Height: 24},
		}}

		r := NewRunner(RunnerConfig{BinaryPath: bin, Width: 80, Height: 24, WorkDir: t.TempDir()})
		res, err := r.Run(context.Background(), Scenario{Name: "bad-resize", Actor: act})
		if err == nil {
			t.Fatalf("Run error = nil, want the harness resize rejection; result = %+v", res)
		}
		if res.Success {
			t.Fatalf("Success = true, want false when the resize is rejected")
		}
		if res.Error == "" {
			t.Fatalf("result.Error is empty, want the resize rejection recorded")
		}
		if !strings.Contains(res.Error, "65535") {
			t.Fatalf("result.Error = %q, want the out-of-range resize error", res.Error)
		}
	})
}

type stubActor struct {
	actions []actor.Action
	pos     int
}

func (s *stubActor) Act(ctx context.Context, sc screen.Screen) (actor.Action, error) {
	if s.pos >= len(s.actions) {
		return actor.Action{Type: actor.ActionDone, Success: true}, nil
	}
	a := s.actions[s.pos]
	s.pos++
	return a, nil
}

func TestRunnerCountsKeystrokes(t *testing.T) {
	act := &stubActor{
		actions: []actor.Action{
			{Type: actor.ActionType, Text: "hi"},
			{Type: actor.ActionKey, Key: "enter"},
		},
	}
	sc := Scenario{
		Name:  "stub",
		Actor: act,
	}

	r := NewRunner(RunnerConfig{BinaryPath: "cat", WorkDir: t.TempDir()})
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Keystrokes != 3 { // h, i, enter
		t.Fatalf("Keystrokes = %d, want 3", res.Keystrokes)
	}
	if !res.Success {
		t.Fatalf("Success = false, want true")
	}
}

// TestReportDirRequiresARunnerDirectory proves WriteReport never falls back to
// a shared default directory: a runner built without ReportDir reports the
// omission instead of writing artifacts somewhere everyone can overwrite.
func TestReportDirRequiresARunnerDirectory(t *testing.T) {
	r := NewRunner(RunnerConfig{BinaryPath: "cat", WorkDir: t.TempDir()})
	err := r.WriteReport()
	if err == nil {
		t.Fatalf("WriteReport error = nil, want the missing ReportDir error")
	}
	if !strings.Contains(err.Error(), "ReportDir") {
		t.Fatalf("WriteReport error = %q, want it to name ReportDir", err)
	}
}

// TestReportDirsAreDistinctPerRun proves two concurrent runs cannot overwrite
// each other's artifacts: each runner writes into its own directory, and each
// directory carries only its own scenario's result.
func TestReportDirsAreDistinctPerRun(t *testing.T) {
	parent := t.TempDir()
	run := func(name string) (dir string, rep []byte) {
		t.Helper()
		dir, err := os.MkdirTemp(parent, "run-*")
		if err != nil {
			t.Fatalf("create report dir for %s: %v", name, err)
		}
		r := NewRunner(RunnerConfig{BinaryPath: "cat", WorkDir: t.TempDir(), ReportDir: dir})
		if _, err := r.Run(context.Background(), Scenario{Name: name, Actor: &stubActor{}}); err != nil {
			t.Fatalf("Run(%s): %v", name, err)
		}
		if err := r.WriteReport(); err != nil {
			t.Fatalf("WriteReport(%s): %v", name, err)
		}
		rep, err = os.ReadFile(filepath.Join(dir, "usability-report.json"))
		if err != nil {
			t.Fatalf("read report for %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "friction-log.md")); err != nil {
			t.Fatalf("friction log missing for %s: %v", name, err)
		}
		return dir, rep
	}

	firstDir, firstReport := run("first-run")
	secondDir, secondReport := run("second-run")

	if firstDir == secondDir {
		t.Fatalf("both runs wrote to the same directory %q", firstDir)
	}
	if !strings.Contains(string(firstReport), "first-run") {
		t.Fatalf("first report does not contain its own scenario name")
	}
	if strings.Contains(string(firstReport), "second-run") {
		t.Fatalf("first report contains the other run's scenario name")
	}
	if !strings.Contains(string(secondReport), "second-run") {
		t.Fatalf("second report does not contain its own scenario name")
	}
	if strings.Contains(string(secondReport), "first-run") {
		t.Fatalf("second report contains the other run's scenario name")
	}
}

func TestRunnerFailsOnTerminalModelError(t *testing.T) {
	// The child emits a screen-changing control the model rejects (DECSTBM).
	// Even though the actor would report success, the runner must fail: an
	// untrustworthy screen can never produce a passing scenario.
	bin := filepath.Join(t.TempDir(), "helper.sh")
	body := "#!/bin/sh\nprintf 'ok\\033[2;5r'\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	r := NewRunner(RunnerConfig{BinaryPath: bin, WorkDir: t.TempDir()})
	res, err := r.Run(context.Background(), Scenario{Name: "decode-error", Actor: &stubActor{}})
	if err == nil {
		t.Fatalf("Run error = nil, want the terminal model error; result = %+v", res)
	}
	if !strings.Contains(err.Error(), "terminal model") {
		t.Fatalf("Run error = %v, want the terminal model error", err)
	}
	if res.Success {
		t.Fatalf("Success = true, want false when the screen cannot be decoded")
	}
	if res.Error == "" {
		t.Fatalf("result.Error is empty, want the decoding error recorded")
	}
}
