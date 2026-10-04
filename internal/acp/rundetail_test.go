package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/session"
)

const runTestPlan = "## Task 1: First\n\nbody\n\n## Task 2: Second\n\nDepends on: 1\n\nbody\n\n## Task 3: Third\n\nbody\n"

// seedSDDRun records a run on task 2 in the verify phase, with task 1 done in
// the ledger and one failed verification on task 2.
func seedSDDRun(t *testing.T, st *session.State) {
	t.Helper()
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	ledger := filepath.Join(dir, "ledger.md")
	if err := os.WriteFile(plan, []byte(runTestPlan), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte("Task 1: complete (commits abc1234..def5678, review clean)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st.SetSDDProgress(session.SDDProgress{
		Active: true, PlanName: "plan", PlanPath: plan, LedgerPath: ledger,
		Tasks: []string{"First", "Second", "Third"}, TotalTasks: 3, DoneTasks: 1,
		CurrentTask: 2, Phase: "verifying", FixRound: 1, MaxFixRounds: 3,
		TaskTimings: []session.TaskTiming{{StartedAt: now.Add(-time.Minute), EndedAt: now.Add(-30 * time.Second)}, {StartedAt: now.Add(-30 * time.Second)}, {}},
	})
	st.AddRunEvent(session.RunEvent{Kind: session.RunEventVerifyFailed, TaskN: 2, Title: "go test failed"})
}

func callRun(t *testing.T, m *TurnManager) RunDetail {
	t.Helper()
	v, err := m.Run(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	return v.(RunDetail)
}

func TestRunDetailSDD(t *testing.T) {
	m, st, _ := newStackTestManager(t)
	seedSDDRun(t, st)
	d := callRun(t, m)
	if d.Kind != "sdd" || d.SDD == nil {
		t.Fatalf("detail = %+v", d)
	}
	tasks := d.SDD.Tasks
	if len(tasks) != 3 {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Status != "done" || tasks[0].Commit == nil || tasks[0].Commit.Head != "def5678" || tasks[0].Commit.Base != "abc1234" {
		t.Fatalf("task 1 = %+v", tasks[0])
	}
	if got := tasks[1].DependsOn; len(got) != 1 || got[0] != 1 {
		t.Fatalf("task 2 dependsOn = %v", got)
	}
	t2 := tasks[1]
	if t2.Status != "active" || t2.FixRounds != 1 {
		t.Fatalf("task 2 = %+v", t2)
	}
	if t2.Stages[1].Name != "Verify" || t2.Stages[1].State != "active" {
		t.Fatalf("task 2 verify stage = %+v", t2.Stages[1])
	}
	if t2.Stages[0].State != "pending" {
		t.Fatalf("task 2 implement stage = %+v", t2.Stages[0])
	}
	if tasks[2].Status != "pending" {
		t.Fatalf("task 3 = %+v", tasks[2])
	}
	data, _ := json.Marshal(d)
	var flat struct {
		SDD map[string]any `json:"sdd"`
	}
	if err := json.Unmarshal(data, &flat); err != nil {
		t.Fatal(err)
	}
	if flat.SDD["currentTask"] != float64(2) || flat.SDD["phase"] != "verifying" {
		t.Fatalf("sdd json not flat: %s", data)
	}
}

func TestRunDetailNone(t *testing.T) {
	m, _, _ := newStackTestManager(t)
	if d := callRun(t, m); d.Kind != "none" {
		t.Fatalf("detail = %+v", d)
	}
}

func TestRunDetailSwarmSurvivesClear(t *testing.T) {
	m, st, _ := newStackTestManager(t)
	st.SetSwarmProgress(session.SwarmProgress{Goal: "g", Active: true, Roles: []session.SwarmRole{{Name: "planner", Status: session.SwarmRoleActive}}})
	if d := callRun(t, m); d.Kind != "swarm" || !d.Swarm.Active {
		t.Fatalf("live detail = %+v", d)
	}
	if _, err := m.SwarmStatus(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	st.ClearSwarmProgress()
	d := callRun(t, m)
	if d.Kind != "swarm" || d.Swarm.Active || len(d.Swarm.Roles) != 1 || d.Swarm.Goal != "g" {
		t.Fatalf("after clear = %+v", d)
	}
}

func TestRunDetailUnknownSession(t *testing.T) {
	m, _, _ := newStackTestManager(t)
	if _, err := m.Run(context.Background(), json.RawMessage(`{"sessionId":"nope"}`)); err == nil {
		t.Fatal("want error")
	}
}

func runProgressNotices(notices []stackNotice) int {
	n := 0
	for _, no := range notices {
		if no.params.Update["kind"] == "run_progress" {
			n++
		}
	}
	return n
}

func TestRunProgressEmitsOnChange(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	seedSDDRun(t, st)
	callRun(t, m)
	m.flushStack("s1", st, false)
	if runProgressNotices(*notices) != 0 {
		t.Fatalf("unchanged run emitted: %v", *notices)
	}
	st.UpdateSDDProgress(func(p *session.SDDProgress) { p.Phase = "reviewing" })
	m.flushStack("s1", st, false)
	m.flushStack("s1", st, false)
	if got := runProgressNotices(*notices); got != 1 {
		t.Fatalf("run_progress notices = %d", got)
	}
}

func TestRunProgressSilentBeforeActivation(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	seedSDDRun(t, st)
	m.flushStack("s1", st, false)
	m.flushDirtyStack("s1", st)
	if runProgressNotices(*notices) != 0 {
		t.Fatalf("emitted before activation: %v", *notices)
	}
}

func TestRunProgressActivatedByStack(t *testing.T) {
	m, st, notices := newStackTestManager(t)
	takeStack(t, m)
	seedSDDRun(t, st)
	m.flushStack("s1", st, false)
	m.flushStack("s1", st, false)
	if got := runProgressNotices(*notices); got != 1 {
		t.Fatalf("run_progress notices = %d", got)
	}
}
