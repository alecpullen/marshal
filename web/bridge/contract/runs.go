package contract

import "encoding/json"

// The plan runs' wire types (F6 decision 11): the run digest `GET
// /api/runs/{agentId}` answers with, the entry `GET /api/runs` lists, and the
// two requests a client posts.
//
// The digest's declarations are the agent's own (`SDDRun` as `session/run`
// reports it); they are declared here so both clients are generated from one
// source. `web/bridge`'s list entry aliases `RunListEntry` below.
//
// Every stage state, task status and lane is a plain string: the bridge treats
// them as strings and a second vocabulary would have to be kept in step with
// it. A value this build does not know is drawn by its own name, never hidden.

// RunStage is one stage of one task: the implementer's work, the verify gate,
// the reviewer's reading and the commit.
type RunStage struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// RunCommit is the range a task's commit covers: the base it started from and
// the head it reached.
type RunCommit struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// RunTask is one task of a plan run, with what each of its stages reported.
type RunTask struct {
	N          int         `json:"n"`
	Title      string      `json:"title"`
	DependsOn  []int       `json:"dependsOn"`
	Status     string      `json:"status"`
	StartedAt  int64       `json:"startedAt,omitempty"`
	EndedAt    int64       `json:"endedAt,omitempty"`
	ExecType   string      `json:"execType,omitempty"`
	Commit     *RunCommit  `json:"commit,omitempty"`
	FixRounds  int         `json:"fixRounds"`
	Stages     []RunStage  `json:"stages"`
}

// RunGate is the question a run stops on, when it stops on one.
type RunGate struct {
	TaskN    int    `json:"taskN"`
	Question string `json:"question"`
}

// SDDRun is a plan run as `session/run` reports it: the whole digest, so a
// client that missed a delta can draw the run from one read.
type SDDRun struct {
	Active         bool       `json:"active"`
	PlanName       string     `json:"planName,omitempty"`
	PlanPath       string     `json:"planPath,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	TotalTasks     int        `json:"totalTasks"`
	DoneTasks      int        `json:"doneTasks"`
	CurrentTask    int        `json:"currentTask"`
	Phase          string     `json:"phase,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	FixRound       int        `json:"fixRound"`
	MaxFixRounds   int        `json:"maxFixRounds"`
	TokensUsed     int        `json:"tokensUsed"`
	TokensMax      int        `json:"tokensMax"`
	Finished       bool       `json:"finished"`
	Succeeded      bool       `json:"succeeded"`
	BaseRef        string     `json:"baseRef,omitempty"`
	StartedAt      int64      `json:"startedAt,omitempty"`
	EndedAt        int64      `json:"endedAt,omitempty"`
	PhaseStartedAt int64      `json:"phaseStartedAt,omitempty"`
	Error          string     `json:"error,omitempty"`
	Tasks          []RunTask  `json:"tasks"`
	Gate           *RunGate   `json:"gate,omitempty"`
}

// RunListEntry is one run as `GET /api/runs` lists it: the agent it runs on,
// the digest it last sent, and where it came from.
type RunListEntry struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name,omitempty"`
	Project string `json:"project"`
	// Run is the agent's last run digest, as it sent it.
	Run   json.RawMessage `json:"run"`
	// At is when the digest arrived, in Unix milliseconds, like the run delta
	// and the usage rows.
	At    int64  `json:"at"`
	Error string `json:"error,omitempty"`
}
