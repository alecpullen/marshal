package acp

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/pipeline"
)

// RunDetail is the session/run result and the body of a run_progress update.
type RunDetail struct {
	Kind  string    `json:"kind"` // "sdd", "swarm" or "none"
	SDD   *SDDRun   `json:"sdd,omitempty"`
	Swarm *SwarmRun `json:"swarm,omitempty"`
}

// RunStage is one stage of a task: implement, verify, review or commit.
type RunStage struct {
	Name   string `json:"name"`
	State  string `json:"state"` // pending, active, done, failed, skipped
	Detail string `json:"detail,omitempty"`
}

// RunCommit is the commit range a completed task landed.
type RunCommit struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// RunTask is one plan task inside an SDDRun. Times are Unix milliseconds.
type RunTask struct {
	N         int        `json:"n"`
	Title     string     `json:"title"`
	DependsOn []int      `json:"dependsOn"`
	Status    string     `json:"status"` // pending, active, done, failed
	StartedAt int64      `json:"startedAt,omitempty"`
	EndedAt   int64      `json:"endedAt,omitempty"`
	ExecType  string     `json:"execType,omitempty"`
	Commit    *RunCommit `json:"commit,omitempty"`
	FixRounds int        `json:"fixRounds"`
	Stages    []RunStage `json:"stages"`
}

// SDDRun is SDDStatusResult's fields (copied, so the JSON stays flat) plus
// the per-task detail.
type SDDRun struct {
	Active         bool         `json:"active"`
	PlanName       string       `json:"planName,omitempty"`
	PlanPath       string       `json:"planPath,omitempty"`
	Branch         string       `json:"branch,omitempty"`
	Total          int          `json:"totalTasks"`
	DoneTasks      int          `json:"doneTasks"`
	CurrentTask    int          `json:"currentTask"`
	Phase          string       `json:"phase,omitempty"`
	Detail         string       `json:"detail,omitempty"`
	FixRound       int          `json:"fixRound"`
	MaxFixRounds   int          `json:"maxFixRounds"`
	TokensUsed     int          `json:"tokensUsed"`
	TokensMax      int          `json:"tokensMax"`
	Finished       bool         `json:"finished"`
	Succeeded      bool         `json:"succeeded"`
	BaseRef        string       `json:"baseRef,omitempty"`
	LedgerPath     string       `json:"ledgerPath,omitempty"`
	StartedAt      int64        `json:"startedAt,omitempty"`
	EndedAt        int64        `json:"endedAt,omitempty"`
	PhaseStartedAt int64        `json:"phaseStartedAt,omitempty"`
	Error          string       `json:"error,omitempty"`
	Tasks          []RunTask    `json:"tasks"`
	Gate           *SDDGateInfo `json:"gate,omitempty"`
}

// SwarmRun is SwarmStatusResult's fields; Active is false once the run ended
// and the last recorded roles are being replayed.
type SwarmRun struct {
	Goal       string          `json:"goal,omitempty"`
	Active     bool            `json:"active"`
	Roles      []SwarmRoleInfo `json:"roles"`
	TokensUsed int             `json:"tokensUsed"`
	TokensMax  int             `json:"tokensMax"`
}

const (
	stageImplement = "Implement"
	stageVerify    = "Verify"
	stageReview    = "Review"
	stageCommit    = "Commit"
)

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// phaseStage maps a controller phase onto the stage it belongs to.
func phaseStage(phase string) string {
	p := strings.ToLower(phase)
	switch {
	case strings.HasPrefix(p, "implement"), strings.HasPrefix(p, "fix"),
		strings.HasPrefix(p, "appl"), strings.HasPrefix(p, "assert"), strings.HasPrefix(p, "agent"):
		return stageImplement
	case strings.HasPrefix(p, "verif"), strings.HasPrefix(p, "gate"):
		return stageVerify
	case strings.HasPrefix(p, "review"):
		return stageReview
	case strings.HasPrefix(p, "commit"):
		return stageCommit
	}
	return ""
}

func blockingSeverity(s string) bool {
	return s == string(pipeline.SeverityCritical) || s == string(pipeline.SeverityImportant)
}

// applyRunEvents folds one task's run events into its stages and fix rounds.
func applyRunEvents(t *RunTask, events []session.RunEvent) (taskDone bool) {
	set := func(name, state, detail string) {
		for i := range t.Stages {
			if t.Stages[i].Name == name {
				t.Stages[i].State = state
				if detail != "" {
					t.Stages[i].Detail = detail
				}
			}
		}
	}
	// settle marks every stage in names done, keeping skipped ones.
	settle := func(names ...string) {
		for _, name := range names {
			for i := range t.Stages {
				if t.Stages[i].Name == name && t.Stages[i].State != "skipped" {
					t.Stages[i].State = "done"
				}
			}
		}
	}
	for _, ev := range events {
		switch ev.Kind {
		case session.RunEventVerifyFailed:
			t.FixRounds++
			set(stageImplement, "pending", "")
			set(stageVerify, "failed", ev.Title)
		case session.RunEventGateSkipped:
			settle(stageImplement)
			set(stageVerify, "skipped", ev.Title)
		case session.RunEventReview:
			settle(stageImplement, stageVerify)
			if blockingSeverity(ev.Severity) {
				set(stageReview, "failed", ev.Title)
			} else {
				set(stageReview, "done", ev.Title)
			}
		case session.RunEventCommit:
			settle(stageImplement, stageVerify, stageReview, stageCommit)
			set(stageCommit, "done", ev.Title)
		case session.RunEventTaskDone:
			settle(stageImplement, stageVerify, stageReview, stageCommit)
			taskDone = true
		}
	}
	return taskDone
}

// buildSDDRun assembles the SDD run detail from the session's progress, its
// plan file, the ledger and the run events. It returns nil when no plan run
// has been recorded.
func buildSDDRun(st *session.State) *SDDRun {
	prog := st.SDDProgress()
	if prog.PlanPath == "" {
		return nil
	}
	run := &SDDRun{
		Active:         prog.Active,
		PlanName:       prog.PlanName,
		PlanPath:       prog.PlanPath,
		Branch:         prog.Branch,
		Total:          prog.TotalTasks,
		DoneTasks:      prog.DoneTasks,
		CurrentTask:    prog.CurrentTask,
		Phase:          prog.Phase,
		Detail:         prog.Detail,
		FixRound:       prog.FixRound,
		MaxFixRounds:   prog.MaxFixRounds,
		TokensUsed:     prog.TokensUsed,
		TokensMax:      prog.TokensMax,
		Finished:       prog.Finished,
		Succeeded:      prog.Succeeded,
		BaseRef:        prog.BaseRef,
		LedgerPath:     prog.LedgerPath,
		StartedAt:      unixMillis(prog.StartedAt),
		EndedAt:        unixMillis(prog.EndedAt),
		PhaseStartedAt: unixMillis(prog.PhaseStartedAt),
		Error:          prog.Error,
		Tasks:          []RunTask{},
	}
	if g := st.SDDGate(); g.Question != "" {
		run.Gate = &SDDGateInfo{TaskN: g.TaskN, Question: g.Question}
	}

	// Titles and dependencies come from the plan; when it no longer parses,
	// fall back to the titles the controller recorded.
	type planTask struct {
		n     int
		title string
		deps  []int
	}
	var tasks []planTask
	if plan, err := pipeline.ParsePlan(prog.PlanPath); err == nil {
		for _, t := range plan.Tasks {
			tasks = append(tasks, planTask{t.N, t.Title, t.DependsOn})
		}
	} else {
		for i, title := range prog.Tasks {
			tasks = append(tasks, planTask{i + 1, title, nil})
		}
	}

	ledger := pipeline.Ledger{Path: prog.LedgerPath}
	var completed map[int]bool
	if prog.LedgerPath != "" {
		completed, _ = ledger.CompletedTasks()
	}
	byTask := map[int][]session.RunEvent{}
	for _, ev := range st.RunEvents() {
		byTask[ev.TaskN] = append(byTask[ev.TaskN], ev)
	}

	for i, pt := range tasks {
		rt := RunTask{
			N: pt.n, Title: pt.title, DependsOn: pt.deps, Status: "pending",
			Stages: []RunStage{
				{Name: stageImplement, State: "pending"},
				{Name: stageVerify, State: "pending"},
				{Name: stageReview, State: "pending"},
				{Name: stageCommit, State: "pending"},
			},
		}
		if rt.DependsOn == nil {
			rt.DependsOn = []int{}
		}
		if i < len(prog.TaskTimings) {
			rt.StartedAt = unixMillis(prog.TaskTimings[i].StartedAt)
			rt.EndedAt = unixMillis(prog.TaskTimings[i].EndedAt)
		}
		if i < len(prog.TaskExecTypes) {
			rt.ExecType = prog.TaskExecTypes[i]
		}
		eventDone := applyRunEvents(&rt, byTask[pt.n])
		if completed[pt.n] || eventDone {
			rt.Status = "done"
			for s := range rt.Stages {
				if rt.Stages[s].State != "skipped" {
					rt.Stages[s].State = "done"
				}
			}
			if base, head, ok := ledger.TaskCommit(pt.n); ok && prog.LedgerPath != "" {
				rt.Commit = &RunCommit{Base: base, Head: head}
			}
		} else if pt.n == prog.CurrentTask {
			switch {
			case prog.Active:
				rt.Status = "active"
				if stage := phaseStage(prog.Phase); stage != "" {
					for s := range rt.Stages {
						if rt.Stages[s].Name == stage {
							rt.Stages[s].State = "active"
						}
					}
				}
			case prog.Finished && !prog.Succeeded:
				rt.Status = "failed"
			}
		}
		run.Tasks = append(run.Tasks, rt)
	}
	return run
}

func buildSwarmRun(prog session.SwarmProgress) *SwarmRun {
	run := &SwarmRun{
		Goal: prog.Goal, Active: prog.Active,
		TokensUsed: prog.TokensUsed, TokensMax: prog.TokensMax,
		Roles: []SwarmRoleInfo{},
	}
	for _, r := range prog.Roles {
		run.Roles = append(run.Roles, SwarmRoleInfo{
			Name: r.Name, Status: string(r.Status), Detail: r.Detail,
			Tokens: r.Tokens, StartedAt: r.StartedAt,
		})
	}
	return run
}

// recordSwarm keeps the last non-empty swarm progress per session;
// ClearSwarmProgress wipes the live copy when a run ends.
func (m *TurnManager) recordSwarm(sessionID string, st *session.State) {
	prog := st.SwarmProgress()
	if len(prog.Roles) == 0 {
		return
	}
	m.runMu.Lock()
	m.lastSwarm[sessionID] = prog
	m.runMu.Unlock()
}

// buildRunDetail returns the live SDD run, else the live or last swarm run,
// else kind "none".
func (m *TurnManager) buildRunDetail(sessionID string, st *session.State) RunDetail {
	m.recordSwarm(sessionID, st)
	if sdd := buildSDDRun(st); sdd != nil {
		return RunDetail{Kind: "sdd", SDD: sdd}
	}
	m.runMu.Lock()
	prog, ok := m.lastSwarm[sessionID]
	m.runMu.Unlock()
	if live := st.SwarmProgress(); len(live.Roles) > 0 {
		prog, ok = live, true
	} else if ok {
		prog.Active = false
	}
	if ok {
		return RunDetail{Kind: "swarm", Swarm: buildSwarmRun(prog)}
	}
	return RunDetail{Kind: "none"}
}

func hashRunDetail(d RunDetail) uint64 {
	data, _ := json.Marshal(d)
	h := fnv.New64a()
	h.Write(data)
	return h.Sum64()
}

// Run handles session/run: the full SDD or swarm run detail.
func (m *TurnManager) Run(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionIDParams
	if err := decodeParams(params, &p, "session/run"); err != nil {
		return nil, err
	}
	if p.SessionID == "" {
		return nil, invalidParamsError("session/run requires sessionId")
	}
	rt, ok := m.lookup(p.SessionID)
	if !ok || rt.State == nil {
		return nil, serverErrorf("unknown session: %s", p.SessionID)
	}
	detail := m.buildRunDetail(p.SessionID, rt.State)
	m.runMu.Lock()
	m.runActive[p.SessionID] = true
	m.runHashes[p.SessionID] = hashRunDetail(detail)
	m.runMu.Unlock()
	// The idle loop also drives run_progress for a client that never opens
	// the stack.
	m.ensureStackIdle(p.SessionID, rt)
	return detail, nil
}

// flushRun pushes a run_progress update when the run detail changed since
// the last send. It does nothing until a client has called session/run or
// activated the stack.
func (m *TurnManager) flushRun(sessionID string, st *session.State) {
	m.runMu.Lock()
	active := m.runActive[sessionID]
	m.runMu.Unlock()
	if !active {
		return
	}
	detail := m.buildRunDetail(sessionID, st)
	hash := hashRunDetail(detail)
	m.runMu.Lock()
	prev, seen := m.runHashes[sessionID]
	if seen && prev == hash {
		m.runMu.Unlock()
		return
	}
	m.runHashes[sessionID] = hash
	m.runMu.Unlock()
	if err := m.notify("session/update", SessionUpdateParams{
		SessionID: sessionID,
		Update:    map[string]any{"kind": "run_progress", "run": detail},
	}); err != nil {
		slog.Default().Warn("acp: run_progress notify failed", "session_id", sessionID, "error", err)
	}
}

// dropRun forgets a session's run state.
func (m *TurnManager) dropRun(sessionID string) {
	m.runMu.Lock()
	delete(m.runActive, sessionID)
	delete(m.runHashes, sessionID)
	delete(m.lastSwarm, sessionID)
	m.runMu.Unlock()
	m.usageMu.Lock()
	delete(m.usageHW, sessionID)
	m.usageMu.Unlock()
}

// watchRun marks a session's run as watched, recording the current detail as
// already known so only later changes are pushed.
func (m *TurnManager) watchRun(sessionID string, st *session.State) {
	m.runMu.Lock()
	watched := m.runActive[sessionID]
	m.runMu.Unlock()
	if watched {
		return
	}
	hash := hashRunDetail(m.buildRunDetail(sessionID, st))
	m.runMu.Lock()
	m.runActive[sessionID] = true
	m.runHashes[sessionID] = hash
	m.runMu.Unlock()
}
