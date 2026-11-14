package fleetclient

import (
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// onlyRow is the state's single row.
func onlyRow(t *testing.T, s acpclient.FleetState) contract.AgentStatus {
	t.Helper()
	if len(s.Agents) != 1 {
		t.Fatalf("rows = %d, want 1", len(s.Agents))
	}
	return s.Agents[0]
}

func TestDeltasUpdateTheRows(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	gate := contract.GateRecord{
		Result: &contract.GateResult{OK: true, Output: "ok"},
		At:     fixtureUpdatedAt,
	}

	// newClient is one client with the base row, whose access starts at
	// revision 5 and mode act.
	newClient := func() *Client {
		c := New(Transport{Server: "test"}, WithNow(func() time.Time { return now }))
		access := contract.AccessState{Mode: "act", Auto: true, Revision: 5}
		c.state.Agents = []contract.AgentStatus{{
			ID:           "a1",
			Project:      "/repo/a",
			Status:       contract.AgentRunning,
			Activity:     "before",
			ContextPct:   10,
			ChangedFiles: 1,
			Access:       &access,
			UpdatedAt:    fixtureUpdatedAt,
		}}
		c.state.Projects = []contract.ProjectStatus{{Root: "/repo/a"}, {Root: "/repo/b"}}
		return c
	}

	tests := []struct {
		name    string
		setup   func(c *Client)
		payload string
		changed bool
		refetch refetchKind
		check   func(t *testing.T, s acpclient.FleetState)
	}{
		{
			name:    "activity",
			payload: `{"kind":"activity","sessionId":"a1","activity":"now reading"}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Activity != "now reading" {
					t.Errorf("Activity = %q, want %q", row.Activity, "now reading")
				}
				if row.ContextPct != 10 || row.ChangedFiles != 1 {
					t.Errorf("telemetry moved: %d%% %d files, want 10%% 1", row.ContextPct, row.ChangedFiles)
				}
				if !row.UpdatedAt.Equal(now) {
					t.Errorf("UpdatedAt = %v, want the client's clock %v", row.UpdatedAt, now)
				}
			},
		},
		{
			name:    "telemetry both fields",
			payload: `{"kind":"telemetry","sessionId":"a1","contextPct":42,"changedFiles":7}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.ContextPct != 42 || row.ChangedFiles != 7 {
					t.Errorf("telemetry = %d%% %d files, want 42%% 7", row.ContextPct, row.ChangedFiles)
				}
				if !row.UpdatedAt.Equal(now) {
					t.Errorf("UpdatedAt = %v, want the client's clock %v", row.UpdatedAt, now)
				}
			},
		},
		{
			name:    "telemetry context only",
			payload: `{"kind":"telemetry","sessionId":"a1","contextPct":42}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.ContextPct != 42 {
					t.Errorf("ContextPct = %d, want 42", row.ContextPct)
				}
				if row.ChangedFiles != 1 {
					t.Errorf("ChangedFiles = %d, want 1: the delta had none", row.ChangedFiles)
				}
			},
		},
		{
			name:    "telemetry changed files only",
			payload: `{"kind":"telemetry","sessionId":"a1","changedFiles":9}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.ChangedFiles != 9 {
					t.Errorf("ChangedFiles = %d, want 9", row.ChangedFiles)
				}
				if row.ContextPct != 10 {
					t.Errorf("ContextPct = %d, want 10: the delta had none", row.ContextPct)
				}
			},
		},
		{
			name:    "mode takes a newer revision",
			payload: `{"kind":"mode","sessionId":"a1","access":{"mode":"plan","auto":false,"system_access":true,"access_revision":6}}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				want := contract.AccessState{Mode: "plan", Auto: false, System: true, Revision: 6}
				if row.Access == nil || *row.Access != want {
					t.Errorf("Access = %+v, want %+v", row.Access, want)
				}
				if !row.UpdatedAt.Equal(now) {
					t.Errorf("UpdatedAt = %v, want the client's clock %v", row.UpdatedAt, now)
				}
			},
		},
		{
			name:    "mode takes an equal revision",
			payload: `{"kind":"mode","sessionId":"a1","access":{"mode":"plan","auto":false,"system_access":false,"access_revision":5}}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Access == nil || row.Access.Mode != "plan" {
					t.Errorf("Access = %+v, want mode plan", row.Access)
				}
			},
		},
		{
			name:    "mode drops an older revision",
			payload: `{"kind":"mode","sessionId":"a1","access":{"mode":"plan","auto":false,"system_access":true,"access_revision":4}}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Access == nil || row.Access.Mode != "act" || row.Access.Revision != 5 {
					t.Errorf("Access = %+v, want the row's own act@5", row.Access)
				}
				if !row.UpdatedAt.Equal(fixtureUpdatedAt) {
					t.Errorf("UpdatedAt = %v, want it untouched at %v", row.UpdatedAt, fixtureUpdatedAt)
				}
			},
		},
		{
			name:    "mode without access changes nothing",
			payload: `{"kind":"mode","sessionId":"a1"}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Access == nil || row.Access.Mode != "act" || row.Access.Revision != 5 {
					t.Errorf("Access = %+v, want the row's own act@5", row.Access)
				}
			},
		},
		{
			name: "mode replaces an absent access",
			setup: func(c *Client) {
				c.state.Agents[0].Access = nil
			},
			payload: `{"kind":"mode","sessionId":"a1","access":{"mode":"act","auto":true,"system_access":true,"access_revision":1}}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Access == nil || row.Access.System != true || row.Access.Revision != 1 {
					t.Errorf("Access = %+v, want the delta's act@1", row.Access)
				}
			},
		},
		{
			name:    "hold held",
			payload: `{"kind":"hold","sessionId":"a1","held":true}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if !row.Held {
					t.Error("Held = false, want true")
				}
				if !row.UpdatedAt.Equal(fixtureUpdatedAt) {
					t.Errorf("UpdatedAt = %v, want it untouched: hold is not one of the four kinds", row.UpdatedAt)
				}
			},
		},
		{
			name: "hold released",
			setup: func(c *Client) {
				c.state.Agents[0].Held = true
			},
			payload: `{"kind":"hold","sessionId":"a1","held":false}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				if onlyRow(t, s).Held {
					t.Error("Held = true, want false")
				}
			},
		},
		{
			name:    "hold without a value changes nothing",
			payload: `{"kind":"hold","sessionId":"a1"}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				if onlyRow(t, s).Held {
					t.Error("Held = true, want the row's false")
				}
			},
		},
		{
			name:    "gate",
			payload: `{"kind":"gate","sessionId":"a1","gate":{"result":{"ok":true,"output":"ok"},"at":"2026-11-14T10:00:00Z"}}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				got, ok := s.Gates["a1"]
				if !ok {
					t.Fatalf("Gates = %+v, want an entry for a1", s.Gates)
				}
				if !reflect.DeepEqual(got, gate) {
					t.Errorf("Gates[a1] = %+v, want %+v", got, gate)
				}
				if !onlyRow(t, s).UpdatedAt.Equal(fixtureUpdatedAt) {
					t.Error("a gate delta moved the row's UpdatedAt")
				}
			},
		},
		{
			name:    "gate without a record changes nothing",
			payload: `{"kind":"gate","sessionId":"a1"}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				if len(s.Gates) != 0 {
					t.Errorf("Gates = %+v, want empty", s.Gates)
				}
			},
		},
		{
			name:    "run moves only UpdatedAt",
			payload: `{"kind":"run","sessionId":"a1","run":{"step":"thinking"},"at":123}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if !row.UpdatedAt.Equal(now) {
					t.Errorf("UpdatedAt = %v, want the client's clock %v", row.UpdatedAt, now)
				}
				if row.Activity != "before" || row.ContextPct != 10 || row.ChangedFiles != 1 || row.Status != contract.AgentRunning {
					t.Errorf("row = %+v, want every other field untouched", row)
				}
			},
		},
		{
			name:    "pending approval",
			payload: `{"kind":"pending","sessionId":"a1","pendingKind":"approval"}`,
			changed: true,
			refetch: refetchAgents,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Status != contract.AgentAwaitingApproval {
					t.Errorf("Status = %q, want %q", row.Status, contract.AgentAwaitingApproval)
				}
				if !row.UpdatedAt.Equal(fixtureUpdatedAt) {
					t.Error("a pending delta moved the row's UpdatedAt")
				}
			},
		},
		{
			name:    "pending question",
			payload: `{"kind":"pending","sessionId":"a1","pendingKind":"question"}`,
			changed: true,
			refetch: refetchAgents,
			check: func(t *testing.T, s acpclient.FleetState) {
				if got := onlyRow(t, s).Status; got != contract.AgentAwaitingQuestion {
					t.Errorf("Status = %q, want %q", got, contract.AgentAwaitingQuestion)
				}
			},
		},
		{
			name:    "pending of another kind leaves the status",
			payload: `{"kind":"pending","sessionId":"a1","pendingKind":"other"}`,
			changed: false,
			refetch: refetchAgents,
			check: func(t *testing.T, s acpclient.FleetState) {
				if got := onlyRow(t, s).Status; got != contract.AgentRunning {
					t.Errorf("Status = %q, want the row's %q", got, contract.AgentRunning)
				}
			},
		},
		{
			name:    "project removed drops the project and its rows",
			payload: `{"kind":"project_removed","project":"/repo/a"}`,
			changed: true,
			check: func(t *testing.T, s acpclient.FleetState) {
				if len(s.Agents) != 0 {
					t.Errorf("rows = %d, want the project's rows dropped", len(s.Agents))
				}
				if len(s.Projects) != 1 || s.Projects[0].Root != "/repo/b" {
					t.Errorf("Projects = %+v, want only /repo/b", s.Projects)
				}
			},
		},
		{
			name:    "project removed for an unknown project changes nothing",
			payload: `{"kind":"project_removed","project":"/repo/elsewhere"}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				if len(s.Agents) != 1 || len(s.Projects) != 2 {
					t.Errorf("state = %d rows, %d projects, want them all kept", len(s.Agents), len(s.Projects))
				}
			},
		},
		{
			name:    "a row delta for an agent with no row changes nothing",
			payload: `{"kind":"activity","sessionId":"nobody","activity":"ignored"}`,
			changed: false,
			check: func(t *testing.T, s acpclient.FleetState) {
				row := onlyRow(t, s)
				if row.Activity != "before" || !row.UpdatedAt.Equal(fixtureUpdatedAt) {
					t.Errorf("row = %+v, want it untouched", row)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient()
			if tt.setup != nil {
				tt.setup(c)
			}

			got := c.applyDelta([]byte(tt.payload))
			if got.changed != tt.changed {
				t.Errorf("changed = %v, want %v", got.changed, tt.changed)
			}
			if got.refetch != tt.refetch {
				t.Errorf("refetch = %v, want %v", got.refetch, tt.refetch)
			}
			tt.check(t, c.State())
		})
	}
}

// TestTheDeltaKindsAreReadFirst checks the payload's own shape picks the decode,
// so a kind with a different body still lands.
func TestTheDeltaKindsAreReadFirst(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	c := New(Transport{Server: "test"}, WithNow(func() time.Time { return now }))

	// An inbox delta's body is not a FleetDelta: its kind must be read first.
	got := c.applyDelta([]byte(`{"kind":"inbox","inbox":{"groups":{"ready":["a1"]},"queue":[]}}`))
	if !got.changed {
		t.Fatal("the inbox delta did not change the state")
	}
	if !sameStrings(c.State().Inbox.Groups.Ready, []string{"a1"}) {
		t.Errorf("Inbox.Groups.Ready = %v, want [a1]", c.State().Inbox.Groups.Ready)
	}

	// The overflow nudge names no kind at all.
	got = c.applyDelta([]byte(`{"type":"replay_overflow"}`))
	if got.refetch != refetchEverything {
		t.Errorf("refetch = %v, want refetchEverything", got.refetch)
	}
	if got.changed {
		t.Error("the overflow reported a change of its own")
	}

	// A payload that is not JSON changes nothing.
	if got := c.applyDelta([]byte("not json")); got.changed || got.refetch != refetchNone {
		t.Errorf("outcome = %+v, want nothing", got)
	}
}

// TestTheBudgetAndInboxDeltasAreFleetWide checks the two refetch outcomes.
func TestTheBudgetAndInboxDeltasAreFleetWide(t *testing.T) {
	c := New(Transport{Server: "test"})

	if got := c.applyDelta([]byte(`{"kind":"budget","scope":"agent","agentId":"a1","spentUsd":5,"capUsd":5,"action":"pause"}`)); got.refetch != refetchBudgets {
		t.Errorf("budget refetch = %v, want refetchBudgets", got.refetch)
	}

	// Rows a1 and a2 exist; an inbox naming only them moves no group and names
	// no unknown agent, so nothing is reread.
	c.state.Agents = []contract.AgentStatus{{ID: "a1"}, {ID: "a2"}}
	if got := c.applyDelta([]byte(`{"kind":"inbox","inbox":{"groups":{"running":["a1"],"earlier":["a2"]},"queue":[]}}`)); got.refetch != refetchNone {
		t.Errorf("inbox refetch = %v, want none", got.refetch)
	}

	// An intake names no agent, so it cannot ask for a reread either.
	if got := c.applyDelta([]byte(`{"kind":"inbox","inbox":{"groups":{},"queue":[{"kind":"intake","submission":{}}]}}`)); got.refetch != refetchNone {
		t.Errorf("intake refetch = %v, want none", got.refetch)
	}
}
