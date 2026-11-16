package fleet

import (
	"testing"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// TestTransitions is a table over transitions: every kind, the epoch rule and
// the not-loaded rule.
func TestTransitions(t *testing.T) {
	base := func() acpclient.FleetState {
		return acpclient.FleetState{
			Server: "local",
			Conn:   acpclient.FleetConnected,
			Loaded: true,
			Epoch:  7,
		}
	}

	cases := []struct {
		name string
		prev acpclient.FleetState
		next acpclient.FleetState
		want []transition
	}{
		{
			name: "an agent that entered needs you on an approval",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{
					{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval, Pending: &contract.PendingRequest{Kind: contract.PendingApproval}},
				}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				return s
			}(),
			want: []transition{
				{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha", Pending: contract.PendingApproval},
			},
		},
		{
			name: "an agent that entered needs you on a question",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{
					{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingQuestion, Pending: &contract.PendingRequest{Kind: contract.PendingQuestion}},
				}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				return s
			}(),
			want: []transition{
				{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha", Pending: contract.PendingQuestion},
			},
		},
		{
			name: "a needs-you row with no pending request",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval}}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				return s
			}(),
			want: []transition{
				{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha"},
			},
		},
		{
			name: "an agent with no name falls back to its shortened ID",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "0123456789abcdef", Status: contract.AgentAwaitingApproval}}
				s.Inbox.Groups.NeedsYou = []string{"0123456789abcdef"}
				return s
			}(),
			want: []transition{
				{Kind: transitionNeedsYou, AgentID: "0123456789abcdef", Name: "01234567"},
			},
		},
		{
			name: "an agent that entered ready",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "b1", Name: "Bravo", Status: contract.AgentIdle}}
				s.Inbox.Groups.Ready = []string{"b1"}
				return s
			}(),
			want: []transition{
				{Kind: transitionReady, AgentID: "b1", Name: "Bravo"},
			},
		},
		{
			name: "an agent whose status became error",
			prev: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "c1", Name: "Charlie", Status: contract.AgentRunning}}
				return s
			}(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "c1", Name: "Charlie", Status: contract.AgentError}}
				return s
			}(),
			want: []transition{
				{Kind: transitionFailed, AgentID: "c1", Name: "Charlie"},
			},
		},
		{
			name: "a submission that entered the queue",
			prev: func() acpclient.FleetState {
				s := base()
				s.Inbox.Queue = []contract.InboxItem{
					{Kind: contract.InboxItemIntake, Submission: &contract.PendingSubmission{ID: "s1", Title: "First"}},
				}
				return s
			}(),
			next: func() acpclient.FleetState {
				s := base()
				s.Inbox.Queue = []contract.InboxItem{
					{Kind: contract.InboxItemIntake, Submission: &contract.PendingSubmission{ID: "s1", Title: "First"}},
					{Kind: contract.InboxItemIntake, Submission: &contract.PendingSubmission{ID: "s2", Title: "Ship the release"}},
				}
				return s
			}(),
			want: []transition{
				{Kind: transitionNeedsYou, SubmissionID: "s2", Name: "Ship the release"},
			},
		},
		{
			name: "an agent already in a group is not a change",
			prev: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{
					{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval},
					{ID: "b1", Name: "Bravo", Status: contract.AgentError},
				}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				s.Inbox.Groups.Ready = []string{"a1"}
				return s
			}(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{
					{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval},
					{ID: "b1", Name: "Bravo", Status: contract.AgentError},
				}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				s.Inbox.Groups.Ready = []string{"a1"}
				return s
			}(),
			want: nil,
		},
		{
			name: "a different epoch gives none",
			prev: func() acpclient.FleetState {
				s := base()
				s.Epoch = 6
				return s
			}(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{
					{ID: "a1", Name: "Alpha", Status: contract.AgentAwaitingApproval, Pending: &contract.PendingRequest{Kind: contract.PendingApproval}},
					{ID: "b1", Name: "Bravo", Status: contract.AgentError},
				}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				s.Inbox.Groups.Ready = []string{"a1"}
				return s
			}(),
			want: nil,
		},
		{
			name: "a previous state that is not loaded gives none",
			prev: func() acpclient.FleetState {
				s := base()
				s.Loaded = false
				return s
			}(),
			next: func() acpclient.FleetState {
				s := base()
				s.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Status: contract.AgentError}}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				return s
			}(),
			want: nil,
		},
		{
			name: "a next state that is not loaded gives none",
			prev: func() acpclient.FleetState { return base() }(),
			next: func() acpclient.FleetState {
				s := base()
				s.Loaded = false
				s.Agents = []contract.AgentStatus{{ID: "a1", Name: "Alpha", Status: contract.AgentError}}
				s.Inbox.Groups.NeedsYou = []string{"a1"}
				return s
			}(),
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transitions(tc.prev, tc.next)
			if len(got) != len(tc.want) {
				t.Fatalf("transitions = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("transition %d = %+v, want %+v (all: %+v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestTransitionText is a table over the Events line each transition carries.
func TestTransitionText(t *testing.T) {
	cases := []struct {
		name string
		in   transition
		want string
	}{
		{"approval", transition{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha", Pending: contract.PendingApproval}, "Alpha needs you: approval"},
		{"question", transition{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha", Pending: contract.PendingQuestion}, "Alpha needs you: question"},
		{"no pending kind", transition{Kind: transitionNeedsYou, AgentID: "a1", Name: "Alpha"}, "Alpha needs you"},
		{"intake", transition{Kind: transitionNeedsYou, Name: "Ship the release"}, "intake: Ship the release"},
		{"ready", transition{Kind: transitionReady, AgentID: "b1", Name: "Bravo"}, "Bravo is ready"},
		{"failed", transition{Kind: transitionFailed, AgentID: "c1", Name: "Charlie"}, "Charlie failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transitionText(tc.in); got != tc.want {
				t.Fatalf("transitionText = %q, want %q", got, tc.want)
			}
		})
	}
}
