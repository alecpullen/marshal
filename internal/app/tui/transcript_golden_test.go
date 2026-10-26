package tui

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/app/tui/theme"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// updateGoldens rewrites the transcript goldens instead of comparing them.
var updateGoldens = flag.Bool("update", false, "rewrite the transcript goldens")

// goldenT0 is when every scene starts. It lies in the past, so no time a
// scene stamps is later than the wall clock.
var goldenT0 = time.Unix(1_700_000_000, 0)

// goldenWidth is the terminal width every scene is rendered at. The
// transcript viewport and the tray's column are both this wide.
const goldenWidth = 100

// goldenClock is the one clock a scene's state and model share. A scene moves
// it on explicitly.
type goldenClock struct{ t time.Time }

func (c *goldenClock) now() time.Time { return c.t }

func (c *goldenClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// goldenScene is one fixture. build fills the state and the model; expand,
// when true, presses Ctrl+G through Update after build; view renders what the
// golden holds (nil: the transcript); width is the column count that view is
// drawn into (nil: the transcript viewport); marks are text the golden must
// show once ANSI is stripped.
type goldenScene struct {
	name   string
	build  func(t *testing.T, clk *goldenClock, st *session.State, m *Model)
	expand bool
	view   func(m *Model) string
	width  func(m *Model) int
	marks  []string
}

// goldenState is a scene's state: session.New over root at goldenT0, on
// clk, with the session ID "golden", and a workspace whose project and
// active roots are root.
func goldenState(t *testing.T, clk *goldenClock, root string) *session.State {
	t.Helper()
	st := session.New(config.Default(), root, goldenT0, session.Persistence{SessionID: "golden"}, session.WithClock(clk.now))
	st.SetWorkspace(session.Workspace{ProjectRoot: root, ActiveRoot: root})
	return st
}

// renderScene builds sc twice, each time on a fresh clock, state, root and
// model, renders both, and fails unless the two renders match. It returns the
// render and the column count it was drawn into.
func renderScene(t *testing.T, sc goldenScene) (string, int) {
	t.Helper()
	render := func() (string, int) {
		t.Setenv("NO_COLOR", "1")
		t.Setenv("TERM", "xterm-256color")
		prev := theme.Current()
		t.Cleanup(func() { theme.Reload(prev) })
		theme.Reload(theme.LoadWithConfig("", "", theme.DepthFlat, nil))

		clk := &goldenClock{t: goldenT0}
		root := t.TempDir()
		st := goldenState(t, clk, root)
		m := testModel(st)
		m.now = clk.now
		updated, _ := m.Update(tea.WindowSizeMsg{Width: goldenWidth, Height: 40})
		m = updated.(Model)

		sc.build(t, clk, st, &m)
		if sc.expand {
			// The expanded scenes go through the key route, so the golden pins
			// Ctrl+G's handling as well as the render it produces.
			m = sendKey(m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
			if !m.expandAll {
				t.Fatalf("scene %s: ctrl+g did not turn expand-all on", sc.name)
			}
		}
		m.invalidateTranscript()
		m.refreshViewport()

		var out string
		if sc.view != nil {
			out = sc.view(&m)
		} else {
			out = strings.Join(m.contentLines, "\n") + "\n"
		}
		// Subagent IDs come from a process-wide counter, so the scene's ID
		// depends on what ran before it. The ID is three digits wide, so
		// replacing it with a fixed one keeps the layout.
		for _, v := range st.Subagents() {
			out = strings.ReplaceAll(out, "#"+strconv.FormatInt(v.ID, 10), "#100")
		}

		bound := m.viewport.Width()
		if sc.width != nil {
			bound = sc.width(&m)
		}
		return out, bound
	}

	first, firstBound := render()
	second, secondBound := render()
	if first != second {
		a := strings.Split(first, "\n")
		b := strings.Split(second, "\n")
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] != b[i] {
				t.Fatalf("scene %s is not deterministic: line %d differs:\nfirst: %q\nsecond: %q", sc.name, i+1, a[i], b[i])
			}
		}
		t.Fatalf("scene %s is not deterministic: %d lines, then %d", sc.name, len(a), len(b))
	}
	if firstBound != secondBound {
		t.Fatalf("scene %s is not deterministic: %d-cell column, then %d", sc.name, firstBound, secondBound)
	}
	return first, firstBound
}

// checkGolden compares got with testdata/transcript/<name>.golden, or
// rewrites the file under -update.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "transcript", name+".golden")
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to write the goldens)", path, err)
	}
	if string(want) == got {
		return
	}
	a := strings.Split(string(want), "\n")
	b := strings.Split(got, "\n")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			t.Fatalf("%s: line %d differs:\nwant: %q\n got: %q\n\nfull render:\n%s", path, i+1, a[i], b[i], ansi.Strip(got))
		}
	}
	t.Fatalf("%s: %d lines, want %d\n\nfull render:\n%s", path, len(b), len(a), ansi.Strip(got))
}

// burnSubagentIDs registers throwaway subagents on a scratch state until the
// next subagent ID is at least 100, so the scene's subagent has a three-digit
// ID. It fails the test when the counter is already past 999.
//
// The counter is process-wide, so the golden tests must keep running
// sequentially: no t.Parallel in this file.
func burnSubagentIDs(t *testing.T) {
	t.Helper()
	scratch := session.New(config.Default(), t.TempDir(), goldenT0, session.Persistence{}, session.WithClock(func() time.Time { return goldenT0 }))
	for {
		v := scratch.RegisterSubagent("burn", nil)
		if v.ID > 999 {
			t.Fatalf("subagent ID counter is past 999 (got %d): the golden's #100 substitution would change the layout", v.ID)
		}
		if v.ID >= 99 {
			return
		}
	}
}

// TestTranscriptGoldens pins what the TUI draws today for every kind of
// transcript row, collapsed and expanded, and for the tray.
func TestTranscriptGoldens(t *testing.T) {
	for _, sc := range goldenScenes() {
		t.Run(sc.name, func(t *testing.T) {
			got, bound := renderScene(t, sc)
			for i, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
				if n := ansi.StringWidth(line); n > bound {
					t.Errorf("line %d is %d cells, wider than the %d-cell column: %q", i, n, bound, line)
				}
			}
			stripped := ansi.Strip(got)
			for _, mark := range sc.marks {
				if !strings.Contains(stripped, mark) {
					t.Errorf("scene %s no longer shows %q", sc.name, mark)
				}
			}
			checkGolden(t, sc.name, got)
		})
	}
}

// goldenScenes is the fixture set. Each scene has a collapsed and an expanded
// golden apart from the tray; the expanded scene reuses the collapsed one's
// build.
func goldenScenes() []goldenScene {
	return []goldenScene{
		{
			name:  "beats",
			build: buildBeats,
			marks: []string{
				"▸ 2/4", "thought 2s", "22s", "×2", "Edit the config", "+1 −1",
				"exit 1 · 3s", "want qwen3-coder", "hook rewrote",
				"command killed: timeout", "job-1 started", "#3 started",
				"no such file", "2 earlier calls", "reviewer #1", "12k tok",
			},
		},
		{
			name:   "beats-expanded",
			build:  buildBeats,
			expand: true,
			marks: []string{
				"The config lives in evals/config.toml.", `model = "llama3"`,
				"error: no such file: evals/old.toml", `args: {"path":"evals/old.toml"}`,
				"1 match", "evals/e1.toml",
			},
		},
		{
			name:  "messages",
			build: buildMessages,
			marks: []string{
				"Model switched to qwen3-coder.", "skill.load: tdd",
				"Compacted 40 earlier messages.", "also check the provider",
				"Read the notes", "3 files changed", "func main",
				"skills: tdd, go-style +1", "thought for 4s", "Keeping the",
				"salvaged",
			},
		},
		{
			name:   "messages-expanded",
			build:  buildMessages,
			expand: true,
			marks:  []string{"skills auto-loaded", "review", "Weighing which notes to keep."},
		},
		{
			name:  "run-events",
			build: buildRunEvents,
			marks: []string{
				"go test ./... failed — exit 1", "line 12", "2 more lines",
				"verification skipped", "review clean", "missing error check",
				"abc1234", "attempt 2", "concern — flaky test", "done — 3 commits",
			},
		},
		{
			name:   "run-events-expanded",
			build:  buildRunEvents,
			expand: true,
			marks:  []string{"line 14"},
		},
		{
			name:  "completions",
			build: buildCompletions,
			marks: []string{
				"job-1", "job-2", "exit 1", "--- FAIL: TestA",
				"review the config change", "#100",
			},
		},
		{
			name:   "completions-expanded",
			build:  buildCompletions,
			expand: true,
			marks:  []string{"outcome", "a_test.go:9: boom", "The config change is"},
		},
		{
			name:  "live",
			build: buildLive,
			marks: []string{
				"Running the eval suite now.", "evals/a8", "sandbox: restricted",
				"→ ⧉ #7", "$ make lint", "lint: 2 warnings", "thinking",
				"Checking whether the lint warnings matter.",
			},
		},
		{
			name:  "live-expanded",
			build: buildLiveExpanded,
			marks: []string{"evals/a1"},
		},
		{
			name:  "tray",
			build: buildTray,
			view: func(m *Model) string {
				return livebox.Render(livebox.Plan(m.boxInput()), m.leftWidth)
			},
			// The tray is drawn into the left column, not the transcript
			// viewport, so that column is its width bound.
			width: func(m *Model) int { return m.leftWidth },
			marks: []string{
				"Edit the config", "go build ./...", "exit 1",
				"--- FAIL: TestB", "k2", "p2-3a",
			},
		},
	}
}

// jsonArgs marshals v into the raw JSON an audit event carries.
func jsonArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// intPtr is the pointer an audit event's exit code needs.
func intPtr(n int) *int { return &n }

// mustTodos sets the state's todo list, failing the test on error.
func mustTodos(t *testing.T, st *session.State, items ...db.TodoItem) {
	t.Helper()
	if err := st.SetTodos(items); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
}

// buildBeats is one settled turn of five beats, with a final answer.
func buildBeats(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	root := st.Workspace().ActiveRoot

	st.AddMessage(session.RoleUser, "Point the eval config at qwen3-coder", session.ContentTypePlain)
	mustTodos(t, st,
		db.TodoItem{ID: "t1", Content: "Read the config", Status: "in_progress"},
		db.TodoItem{ID: "t2", Content: "Edit the config", Status: "pending"},
		db.TodoItem{ID: "t3", Content: "Run the evals", Status: "pending"},
		db.TodoItem{ID: "t4", Content: "Report back", Status: "pending"},
	)

	clk.advance(time.Second)
	s1 := st.BeginStep(session.Actor{})
	st.AddNarration(s1, "Reading the eval config first. It names the model.")
	st.LogThinking(session.ThinkingEntry{
		Text:      "The config lives in evals/config.toml.",
		Duration:  2 * time.Second,
		StartedAt: clk.now(),
		StepID:    s1,
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		ToolCallID:    "c1",
		StepID:        int64(s1),
		Args:          jsonArgs(t, map[string]string{"path": "evals/config.toml"}),
		ResultSummary: "18 lines",
		ResultContent: "model = \"llama3\"\n",
		Duration:      10 * time.Millisecond,
	})

	clk.advance(time.Second)
	st.EndStep(s1)
	mustTodos(t, st,
		db.TodoItem{ID: "t1", Content: "Read the config", Status: "completed"},
		db.TodoItem{ID: "t2", Content: "Edit the config", Status: "in_progress"},
		db.TodoItem{ID: "t3", Content: "Run the evals", Status: "pending"},
		db.TodoItem{ID: "t4", Content: "Report back", Status: "pending"},
	)

	clk.advance(time.Second)
	s2 := st.BeginStep(session.Actor{})
	st.AddNarration(s2, "Pointing the eval config at qwen3-coder.")

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ToolCallID:    "c2",
		StepID:        int64(s2),
		Args:          jsonArgs(t, map[string]string{"path": "evals/config.toml"}),
		FilesChanged:  []string{"evals/config.toml"},
		ResultSummary: "patched evals/config.toml",
		ResultContent: "--- a/evals/config.toml\n+++ b/evals/config.toml\n@@ -1 +1 @@\n-model = \"llama3\"\n+model = \"qwen3-coder\"\n",
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:   "todo.write",
		ToolCallID: "c3",
		StepID:     int64(s2),
		Args: jsonArgs(t, map[string]any{"todos": []map[string]string{
			{"id": "t1", "content": "Read the config", "status": "completed"},
			{"id": "t2", "content": "Edit the config", "status": "in_progress"},
			{"id": "t3", "content": "Run the evals", "status": "pending"},
			{"id": "t4", "content": "Report back", "status": "pending"},
		}}),
		ResultSummary: "tasks updated · 4 items",
	})

	clk.advance(20 * time.Second)
	st.EndStep(s2)

	clk.advance(time.Second)
	s3 := st.BeginStep(session.Actor{Role: "explorer", Label: "explorer"})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "repo.search",
		ToolCallID:    "c4",
		StepID:        int64(s3),
		Args:          jsonArgs(t, map[string]string{"query": "model ="}),
		ResultSummary: "3 matches",
	})
	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "repo.search",
		ToolCallID:    "c5",
		StepID:        int64(s3),
		Args:          jsonArgs(t, map[string]string{"query": "provider ="}),
		ResultSummary: "1 match",
	})

	clk.advance(time.Second)
	st.EndStep(s3)

	clk.advance(time.Second)
	s4 := st.BeginStep(session.Actor{Role: "reviewer", Label: "reviewer #1", Model: "qwen3-coder", Provider: "local"})
	st.AddNarration(s4, "Running the evals and a background build.")

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:        "shell.run",
		ToolCallID:      "c6",
		StepID:          int64(s4),
		Args:            jsonArgs(t, map[string]string{"command": "cd " + root + " && go test ./evals/..."}),
		CommandExitCode: intPtr(1),
		Duration:        3 * time.Second,
		ResultSummary:   "exit 1",
		ResultContent:   "=== RUN   TestEval\n--- FAIL: TestEval (0.01s)\n    eval_test.go:12: want qwen3-coder\nFAIL\n",
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:        "shell.run",
		ToolCallID:      "c7",
		StepID:          int64(s4),
		Args:            jsonArgs(t, map[string]string{"command": "go vet ./..."}),
		CommandExitCode: intPtr(0),
		Duration:        time.Second,
		ResultSummary:   "exit 0",
		Hooks:           []registry.HookMetadata{{Event: "PreToolUse", Rewrote: true}},
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:        "shell.run",
		ToolCallID:      "c8",
		StepID:          int64(s4),
		Args:            jsonArgs(t, map[string]string{"command": "sleep 600"}),
		CommandExitCode: intPtr(137),
		Duration:        30 * time.Second,
		ResultSummary:   "command killed: timeout",
		Sandbox:         registry.SandboxMeta{Enabled: true, Backend: "restricted", KilledReason: "timeout"},
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ToolCallID:    "c9",
		StepID:        int64(s4),
		Args:          jsonArgs(t, map[string]string{"command": "go build ./..."}),
		ResultSummary: "started background job job-1",
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ToolCallID:    "c10",
		StepID:        int64(s4),
		Args:          jsonArgs(t, map[string]string{"description": "review the config change", "role": "reviewer"}),
		ResultSummary: "started as subagent 3 — review the config change",
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:   "file.read",
		ToolCallID: "c11",
		StepID:     int64(s4),
		Args:       jsonArgs(t, map[string]string{"path": "evals/old.toml"}),
		Error:      "no such file: evals/old.toml",
	})

	clk.advance(time.Second)
	st.EndStep(s4)

	clk.advance(time.Second)
	s5 := st.BeginStep(session.Actor{})
	st.AddNarration(s5, "Reading every eval file.")

	for n := 1; n <= 10; n++ {
		clk.advance(time.Second)
		call := fmt.Sprintf("c%d", n+11)
		if n%2 == 1 {
			st.LogToolCall(registry.AuditEvent{
				ToolName:      "file.read",
				ToolCallID:    call,
				StepID:        int64(s5),
				Args:          jsonArgs(t, map[string]string{"path": fmt.Sprintf("evals/e%d.toml", n)}),
				ResultSummary: "4 lines",
			})
			continue
		}
		st.LogToolCall(registry.AuditEvent{
			ToolName:      "repo.search",
			ToolCallID:    call,
			StepID:        int64(s5),
			Args:          jsonArgs(t, map[string]string{"query": fmt.Sprintf("e%d", n)}),
			ResultSummary: "2 hits",
		})
	}

	clk.advance(time.Second)
	st.EndStep(s5)

	clk.advance(time.Second)
	st.AddMessageFinalWithUsage(session.RoleAssistant, "The eval config now points at **qwen3-coder**.", session.ContentTypeMarkdown, 21, "12k tok")
}

// buildMessages is one turn of standalone messages, ending in a salvaged
// answer.
func buildMessages(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	st.AddMessage(session.RoleUser, "Tidy the notes", session.ContentTypePlain)

	clk.advance(time.Second)
	st.AddMessage(session.RoleSystem, "Model switched to qwen3-coder.", session.ContentTypePlain)

	clk.advance(time.Second)
	st.AddMessage(session.RoleSystem, "tdd", session.ContentTypeSkill)

	clk.advance(time.Second)
	st.AddMessage(session.RoleSystem, "Compacted 40 earlier messages.", session.ContentTypeCompaction)

	clk.advance(time.Second)
	st.AddMessage(session.RoleUser, "also check the provider", session.ContentTypeSteering)

	clk.advance(time.Second)
	st.AddMessage(session.RoleAssistant, "1. Read the notes\n2. Tidy them", session.ContentTypePlan)

	clk.advance(time.Second)
	st.AddMessage(session.RoleAssistant, "3 files changed", session.ContentTypeToolResult)

	clk.advance(time.Second)
	st.AddMessage(session.RoleAssistant, "func main() {}", session.ContentTypeCode)

	clk.advance(time.Second)
	st.AddMessage(session.RoleSystem, "tdd\ngo-style\nreview", session.ContentTypeSkillAuto)

	clk.advance(time.Second)
	st.BeginStreaming()
	st.AppendThinking("Weighing which notes to keep.")
	clk.advance(4 * time.Second)
	st.AddMessage(session.RoleAssistant, "Keeping the **recent** notes.", session.ContentTypeMarkdown)

	clk.advance(time.Second)
	st.AddMessageSalvaged(session.RoleAssistant, "Partial answer.", session.ContentTypeMarkdown, "cut off")
}

// buildRunEvents is one turn of plan-run events.
func buildRunEvents(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	st.AddMessage(session.RoleUser, "/sdd run plan.md", session.ContentTypePlain)

	lines := make([]string, 14)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %02d", i+1)
	}
	body := strings.Join(lines, "\n")

	events := []session.RunEvent{
		{Kind: session.RunEventVerifyFailed, TaskN: 2, Title: "go test ./...", Detail: "exit 1", Body: body},
		{Kind: session.RunEventGateSkipped, TaskN: 3, Title: "no test command"},
		{Kind: session.RunEventReview, TaskN: 3, Detail: "clean"},
		{Kind: session.RunEventReview, TaskN: 4, Severity: "high", Title: "missing error check", Detail: "issues"},
		{Kind: session.RunEventCommit, TaskN: 4, Title: "abc1234", Detail: "wire the client"},
		{Kind: session.RunEventRetry, TaskN: 5, Title: "attempt 2", Detail: "verify failed"},
		{Kind: session.RunEventConcern, TaskN: 5, Title: "flaky test"},
		{Kind: session.RunEventTaskDone, TaskN: 5, Detail: "3 commits"},
	}
	for _, ev := range events {
		clk.advance(time.Second)
		st.AddRunEvent(ev)
	}
}

// buildCompletions is two background jobs and a subagent, finished.
func buildCompletions(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	root := st.Workspace().ActiveRoot

	st.AddMessage(session.RoleUser, "Build and review", session.ContentTypePlain)

	clk.advance(time.Second)
	s1 := st.BeginStep(session.Actor{})
	st.AddNarration(s1, "Starting the build and a review.")

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ToolCallID:    "c1",
		StepID:        int64(s1),
		Args:          jsonArgs(t, map[string]string{"command": "go build ./..."}),
		ResultSummary: "started background job job-1",
	})

	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ToolCallID:    "c2",
		StepID:        int64(s1),
		Args:          jsonArgs(t, map[string]string{"command": "go test ./..."}),
		ResultSummary: "started background job job-2",
	})

	clk.advance(time.Second)
	burnSubagentIDs(t)
	child := session.New(config.Default(), root, clk.now(), session.Persistence{}, session.WithClock(clk.now))
	v := st.RegisterSubagent("review the config change", child)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "agent.run",
		ToolCallID:    "c3",
		StepID:        int64(s1),
		Args:          jsonArgs(t, map[string]string{"description": "review the config change"}),
		ResultSummary: "started as subagent " + strconv.FormatInt(v.ID, 10) + " — review the config change",
	})

	clk.advance(time.Second)
	st.EndStep(s1)

	zero, one := 0, 1
	clk.advance(12 * time.Second)
	if err := st.AddJobExit(session.JobExit{
		ID: "job-1", Command: "go build ./...", Exit: &zero,
		Duration: 12 * time.Second, Output: "ok\n", Cwd: "evals",
	}); err != nil {
		t.Fatalf("AddJobExit(job-1): %v", err)
	}

	clk.advance(30 * time.Second)
	if err := st.AddJobExit(session.JobExit{
		ID: "job-2", Command: "go test ./...", ExitCode: 1, Exit: &one,
		Duration: 41 * time.Second, Cwd: "evals",
		Output: "=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    a_test.go:9: boom\nFAIL\nexit status 1\n",
	}); err != nil {
		t.Fatalf("AddJobExit(job-2): %v", err)
	}

	clk.advance(5 * time.Second)
	st.FinishSubagent(v.ID, "## Review\n\nThe config change is **correct**.\n\n- the model name matches", nil)
}

// buildLive is a turn still running.
func buildLive(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	m.busy = true
	m.turnStartedAt = goldenT0

	st.AddMessage(session.RoleUser, "Run the evals", session.ContentTypePlain)

	clk.advance(time.Second)
	s1 := st.BeginStep(session.Actor{})
	st.AddNarration(s1, "Running the eval suite now.")

	st.SetSandboxInfo(session.SandboxInfo{Backend: "restricted", NetworkIsolation: true})

	clk.advance(time.Second)
	st.SetActiveToolCall(session.ActiveToolCall{
		Name: "shell.run", Args: "go test ./evals/...", StepID: s1,
		ToolCallID: "c1", StartedAt: clk.now(),
	})
	for n := 1; n <= 8; n++ {
		st.AppendActiveToolCallOutput("c1", fmt.Sprintf("ok  evals/a%d 0.0Ns\n", n))
	}

	clk.advance(time.Second)
	st.SetActiveToolCall(session.ActiveToolCall{
		Name: "agent.run", Args: "review the evals", StepID: s1,
		ToolCallID: "c2", StartedAt: clk.now(),
	})
	m.workSnapshot = workSnapOf(viewmodel.WorkItem{
		TaskKey:   "k1",
		Kind:      "agent",
		SourceID:  "7",
		DisplayID: "#7",
		Label:     "review the evals",
		State:     "running",
		StartedAt: clk.now(),
		Origin:    viewmodel.WorkOrigin{ToolCallID: "c2"},
	})

	clk.advance(time.Second)
	st.SetActiveToolCall(session.ActiveToolCall{
		Name: "shell.run", Args: "make lint",
		ToolCallID: "c3", StartedAt: clk.now(),
	})
	st.AppendActiveToolCallOutput("c3", "lint: 2 warnings\n")

	st.BeginStreaming()
	st.AppendThinking("Checking whether the lint warnings matter.")

	clk.advance(5 * time.Second)
}

// buildLiveExpanded is buildLive with the two running calls opened. A running
// call stays collapsed unless it is opened itself, so this scene does not
// press Ctrl+G.
func buildLiveExpanded(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	buildLive(t, clk, st, m)
	for _, atc := range st.ActiveToolCalls() {
		if atc.ToolCallID == "c1" || atc.ToolCallID == "c3" {
			m.toggleExpanded(viewmodel.ActiveToolID(atc))
		}
	}
}

// buildTray is the tray's plan, held and running rows and its plan-run row.
func buildTray(t *testing.T, clk *goldenClock, st *session.State, m *Model) {
	t.Helper()
	st.AddMessage(session.RoleUser, "Edit and build", session.ContentTypePlain)
	mustTodos(t, st,
		db.TodoItem{ID: "t1", Content: "Read the config", Status: "completed"},
		db.TodoItem{ID: "t2", Content: "Edit the config", Status: "in_progress"},
		db.TodoItem{ID: "t3", Content: "Run the evals", Status: "pending"},
		db.TodoItem{ID: "t4", Content: "Report back", Status: "pending"},
	)

	clk.advance(time.Second)
	s1 := st.BeginStep(session.Actor{})
	st.AddNarration(s1, "Building in the background.")
	clk.advance(time.Second)
	st.LogToolCall(registry.AuditEvent{
		ToolName:      "shell.run",
		ToolCallID:    "c1",
		StepID:        int64(s1),
		Args:          jsonArgs(t, map[string]string{"command": "go build ./..."}),
		ResultSummary: "started background job job-1",
	})
	clk.advance(time.Second)
	st.EndStep(s1)

	one := 1
	clk.advance(10 * time.Second)
	if err := st.AddJobExit(session.JobExit{
		ID: "job-1", Command: "go build ./...", ExitCode: 1, Exit: &one,
		Duration: 11 * time.Second, ExecutionID: "e1", ConversationID: "conv",
		SourceEpoch: "ep1",
		Output:      "--- FAIL: TestB\nFAIL\n",
	}); err != nil {
		t.Fatalf("AddJobExit(job-1): %v", err)
	}

	m.heldSince = goldenT0
	m.workSnapshot = workSnapOf(
		workRunningCard("k2", clk.now().Add(-5*time.Second)),
		viewmodel.WorkItem{
			TaskKey:   viewmodel.WorkKey("conv", "ep1", "command", "e1"),
			Kind:      "command",
			DisplayID: "job-1",
			Label:     "go build ./...",
			State:     "failed",
			StartedAt: clk.now().Add(-11 * time.Second),
			EndedAt:   clk.now(),
		},
	)

	m.busy = true
	m.turnStartedAt = goldenT0
	st.SetSDDProgress(session.SDDProgress{
		Active: true, PlanName: "p2-3a", TotalTasks: 7, DoneTasks: 3,
		CurrentTask: 4, Phase: "verifying",
	})
}
