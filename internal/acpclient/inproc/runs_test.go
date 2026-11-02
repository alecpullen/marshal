package inproc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/repo/gitinfo"
)

// TestStatusCarriesTheSteeringQueueAndState pins the queue and the Send now
// snapshot the status line and the footer read.
func TestStatusCarriesTheSteeringQueueAndState(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.PushSteering("a")
	state.PushSteering("b")
	if got, want := sess.Status().Steering.Queue, []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Steering.Queue = %v, want %v", got, want)
	}

	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		return session.OperationStop{}, nil
	})
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	got := sess.Status().Steering.State
	if !got.Active || !got.Supported {
		t.Errorf("Steering.State = %+v, want Active and Supported", got)
	}
	if got.TurnID != turn {
		t.Errorf("Steering.State.TurnID = %q, want the turn's %q", got.TurnID, turn)
	}
}

// TestStatusCarriesTheRunningJobCount pins the count the status line's jobs
// segment reads.
func TestStatusCarriesTheRunningJobCount(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.SetRunningJobsCount(3)
	if got := sess.Status().RunningJobs; got != 3 {
		t.Fatalf("RunningJobs = %d, want 3", got)
	}
}

// TestStatusCarriesSubagentViews pins the tray's agent rows: the meta each
// view carries, the status, and the headline only a running child has.
func TestStatusCarriesSubagentViews(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	meta := session.SubagentMeta{
		Role:     routing.RoleImplementer,
		Provider: "provider-a",
		Model:    "model-a",
		Fallback: true,
	}
	running := newState(t, "child-1")
	running.AddMessage(session.RoleAssistant, "**Reading** the config. Then the tests.", session.ContentTypeNarration)
	first := state.RegisterSubagentWithMeta("scout", running, meta)
	finished := newState(t, "child-2")
	second := state.RegisterSubagentWithMeta("reviewer", finished, meta)
	state.FinishSubagent(second.ID, "done", nil)

	got := sess.Status().Subagents
	want := []acpwire.Subagent{
		{
			ID:       first.ID,
			Label:    "scout",
			Status:   acpwire.SubagentRunning,
			Role:     string(routing.RoleImplementer),
			Model:    "model-a",
			Provider: "provider-a",
			Fallback: true,
			Headline: "Reading the config.",
			Drill: &acpwire.SubagentDrill{
				ActiveRoot:  running.Workspace().ActiveRoot,
				ActiveRoute: routeOf(running.ActiveRoute()),
				Activity:    activityWire(running.Activity()),
			},
		},
		{
			ID:       second.ID,
			Label:    "reviewer",
			Status:   acpwire.SubagentDone,
			Role:     string(routing.RoleImplementer),
			Model:    "model-a",
			Provider: "provider-a",
			Fallback: true,
			Drill: &acpwire.SubagentDrill{
				ActiveRoot:  finished.Workspace().ActiveRoot,
				ActiveRoute: routeOf(finished.ActiveRoute()),
				Activity:    activityWire(finished.Activity()),
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Subagents =\n%+v\nwant\n%+v", got, want)
	}
}

// TestSubagentViewsCarryTheDrillOnlyWhileTheChildIsKept pins that a view
// carries a drill exactly when the session still holds the child's state: the
// child's workspace root, active route and activity, or nothing at all.
func TestSubagentViewsCarryTheDrillOnlyWhileTheChildIsKept(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	child := newState(t, "child-1")
	child.SetWorkspace(session.Workspace{ActiveRoot: "/repo/wt"})
	child.SetActiveRoute(session.RouteInfo{Provider: "p-child", Model: "m-child"})
	child.SetActivity(session.Activity{
		Kind:      session.ActivityReconnecting,
		Label:     "retrying",
		StartedAt: time.Unix(100, 0),
	})
	state.RegisterSubagent("scout", child)
	state.RegisterSubagent("card", nil)

	got := sess.Status().Subagents
	if len(got) != 2 {
		t.Fatalf("Subagents = %+v, want 2 views", got)
	}
	want := &acpwire.SubagentDrill{
		ActiveRoot:  "/repo/wt",
		ActiveRoute: acpwire.Route{Provider: "p-child", Model: "m-child"},
		Activity: acpwire.Activity{
			Kind:      string(session.ActivityReconnecting),
			Label:     "retrying",
			StartedAt: 100000,
		},
	}
	if !reflect.DeepEqual(got[0].Drill, want) {
		t.Errorf("Subagents[0].Drill = %+v, want %+v", got[0].Drill, want)
	}
	if got[1].Drill != nil {
		t.Errorf("Subagents[1].Drill = %+v, want nil for a nil child", got[1].Drill)
	}
}

// TestStatusCarriesSwarmAndSDDProgress pins the run panel's inputs, with every
// time converted to Unix milliseconds.
func TestStatusCarriesSwarmAndSDDProgress(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.SetSwarmProgress(session.SwarmProgress{
		Goal:   "ship it",
		Active: true,
		Roles: []session.SwarmRole{{
			Name:      "implementer",
			Status:    session.SwarmRoleActive,
			Detail:    "writing",
			Tokens:    120,
			StartedAt: time.Unix(100, 5_000_000),
		}},
		TokensUsed: 120,
		TokensMax:  900,
	})
	state.SetSDDProgress(session.SDDProgress{
		Active:         true,
		PlanName:       "plan",
		PlanPath:       "/plans/plan.md",
		Branch:         "pipeline/plan",
		BaseRef:        "main",
		LedgerPath:     "/ledger.json",
		ArtifactsDir:   "/artifacts",
		Tasks:          []string{"one", "two"},
		TaskTimings:    []session.TaskTiming{{StartedAt: time.Unix(102, 0), EndedAt: time.Unix(103, 0)}},
		TaskExecTypes:  []string{"agent", "deterministic"},
		TotalTasks:     2,
		DoneTasks:      1,
		CurrentTask:    2,
		Phase:          "implement",
		PhaseStartedAt: time.Unix(101, 0),
		Detail:         "task two",
		FixRound:       1,
		MaxFixRounds:   3,
		TokensUsed:     4000,
		TokensMax:      9000,
		StartedAt:      time.Unix(100, 0),
		Finished:       true,
		Succeeded:      true,
		EndedAt:        time.Unix(200, 0),
		Error:          "",
	})
	state.SetSDDGate(session.SDDGate{
		TaskN:     2,
		Question:  "which way?",
		TaskTitle: "task two",
		Report:    "the report",
	})

	got := sess.Status()
	wantSwarm := acpwire.SwarmProgress{
		Goal:   "ship it",
		Active: true,
		Roles: []acpwire.SwarmRole{{
			Name:      "implementer",
			Status:    acpwire.SwarmRoleActive,
			Detail:    "writing",
			Tokens:    120,
			StartedAt: 100005,
		}},
		TokensUsed: 120,
		TokensMax:  900,
	}
	if !reflect.DeepEqual(got.Swarm, wantSwarm) {
		t.Errorf("Swarm =\n%+v\nwant\n%+v", got.Swarm, wantSwarm)
	}

	wantSDD := acpwire.SDDProgress{
		Active:         true,
		PlanName:       "plan",
		PlanPath:       "/plans/plan.md",
		Branch:         "pipeline/plan",
		BaseRef:        "main",
		LedgerPath:     "/ledger.json",
		ArtifactsDir:   "/artifacts",
		Tasks:          []string{"one", "two"},
		TaskTimings:    []acpwire.TaskTiming{{StartedAt: 102000, EndedAt: 103000}},
		TaskExecTypes:  []string{"agent", "deterministic"},
		TotalTasks:     2,
		DoneTasks:      1,
		CurrentTask:    2,
		Phase:          "implement",
		PhaseStartedAt: 101000,
		Detail:         "task two",
		FixRound:       1,
		MaxFixRounds:   3,
		TokensUsed:     4000,
		TokensMax:      9000,
		StartedAt:      100000,
		Finished:       true,
		Succeeded:      true,
		EndedAt:        200000,
	}
	if !reflect.DeepEqual(got.SDD, wantSDD) {
		t.Errorf("SDD =\n%+v\nwant\n%+v", got.SDD, wantSDD)
	}

	wantGate := acpwire.SDDGate{
		TaskN:     2,
		Question:  "which way?",
		TaskTitle: "task two",
		Report:    "the report",
	}
	if got.SDDGate != wantGate {
		t.Errorf("SDDGate = %+v, want %+v", got.SDDGate, wantGate)
	}
}

// TestStatusReadsGitForTheActiveRoot pins that the git info is a fresh read of
// the workspace's active root, not a cached value.
func TestStatusReadsGitForTheActiveRoot(t *testing.T) {
	state := newState(t, "s1")
	var asked []string
	_, sess := New(Deps{
		State: state,
		GitInfo: func(dir string) gitinfo.Info {
			asked = append(asked, dir)
			return gitinfo.Info{Branch: "feature", Worktree: "wt", InRepo: true}
		},
	})

	state.SetWorkspace(session.Workspace{ActiveRoot: "/project/wt"})
	got := sess.Status()
	if want := (acpwire.Git{Branch: "feature", Worktree: "wt", InRepo: true}); got.Git != want {
		t.Errorf("Git = %+v, want %+v", got.Git, want)
	}
	if len(asked) != 1 || asked[0] != "/project/wt" {
		t.Errorf("GitInfo asked for %v, want [/project/wt]", asked)
	}
}
