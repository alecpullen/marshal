package acpwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestRequestsJSON pins the bytes Session.Pending returns and the answers it
// takes: the full set of open requests, the empty set and each answer must
// encode exactly as the contract spells them.
func TestRequestsJSON(t *testing.T) {
	full := PendingRequests{
		Approvals: []ApprovalRequest{
			{
				Key:        "K1",
				ToolCallID: "call-1",
				Tool:       "shell.run",
				ToolLabel:  "Run command",
				Command:    "go test ./...",
				Risk:       "command",
				Reason:     "policy: ask",
				Actor:      "implementer",
				Why:        "Running the tests.",
			},
			{
				Key:        "K2",
				SubagentID: 3,
				Subagent:   "scout",
				Tool:       "file.edit",
				ToolLabel:  "Edit file",
				Args:       `{"path":"a.go"}`,
				Diff:       "@@ -1 +1 @@",
				Schema:     "Edit a file",
				Forced:     true,
			},
		},
		Question: &QuestionRequest{
			Key: "K3",
			Questions: []Question{{
				Question: "Which?",
				Options: []QuestionOption{
					{Label: "A", Description: "first"},
					{Label: "B"},
				},
				Multi: true,
			}},
		},
		SkillGate: &SkillGateRequest{
			Key:         "K4",
			Skill:       "tdd",
			Description: "Test first",
			Reason:      "Loading skills consumes context.",
		},
		Sudo: &SudoRequest{
			Key:             "K5",
			Command:         "apt install jq",
			Reason:          "needs root",
			StepID:          7,
			AllowUnlock:     true,
			Error:           "Sorry, try again.",
			CommandApproval: true,
		},
		Access: &AccessRequest{
			RequestID:     "R1",
			SessionID:     "s1",
			Actor:         "orchestrator",
			StepID:        2,
			Reason:        "read outside",
			Current:       AccessState{Mode: "ask", Revision: 4},
			MissingSystem: true,
		},
	}

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "every request open",
			value: full,
			want:  `{"approvals":[{"key":"K1","toolCallId":"call-1","tool":"shell.run","toolLabel":"Run command","command":"go test ./...","risk":"command","reason":"policy: ask","actor":"implementer","why":"Running the tests."},{"key":"K2","subagentId":3,"subagent":"scout","tool":"file.edit","toolLabel":"Edit file","args":"{\"path\":\"a.go\"}","diff":"@@ -1 +1 @@","schema":"Edit a file","forced":true}],"question":{"key":"K3","questions":[{"question":"Which?","options":[{"label":"A","description":"first"},{"label":"B"}],"multi":true}]},"skillGate":{"key":"K4","skill":"tdd","description":"Test first","reason":"Loading skills consumes context."},"sudo":{"key":"K5","command":"apt install jq","reason":"needs root","stepId":7,"allowUnlock":true,"error":"Sorry, try again.","commandApproval":true},"access":{"requestId":"R1","sessionId":"s1","actor":"orchestrator","stepId":2,"reason":"read outside","current":{"mode":"ask","revision":4},"missingEditing":false,"missingSystem":true}}`,
		},
		{
			name:  "no requests open",
			value: PendingRequests{},
			want:  `{}`,
		},
		{
			name:  "sudo answer",
			value: SudoAnswer{OK: true, Password: []byte("hunter2")},
			want:  `{"ok":true,"password":"aHVudGVyMg=="}`,
		},
		{
			name:  "approval answer",
			value: ApprovalAnswer{Decision: ApprovalOnce, Edited: "go test ./internal/..."},
			want:  `{"decision":"once","edited":"go test ./internal/..."}`,
		},
		{
			name:  "sandbox",
			value: Sandbox{Backend: "container", NetworkIsolation: true},
			want:  `{"backend":"container","networkIsolation":true}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if string(got) != tc.want {
				t.Errorf("%s JSON:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
		})
	}
}

// TestPendingUpdateRoundTrips pins the pending_requests update: the whole
// state survives the wire, and the kind it carries is the one the client
// dispatches on.
func TestPendingUpdateRoundTrips(t *testing.T) {
	want := PendingUpdate{
		Kind: UpdatePending,
		Pending: PendingState{
			Requests: PendingRequests{
				Approvals: []ApprovalRequest{{
					Key:        "K2",
					SubagentID: 3,
					Subagent:   "scout",
					Tool:       "file.edit",
					ToolLabel:  "Edit file",
					Forced:     true,
					Actor:      "scout",
					Why:        "Editing the parser.",
				}},
				Question: &QuestionRequest{
					Key: "K3",
					Questions: []Question{{
						Question: "Which?",
						Options: []QuestionOption{
							{Label: "A", Description: "first"},
							{Label: "B"},
						},
						Multi: true,
					}},
				},
				SkillGate: &SkillGateRequest{
					Key:         "K4",
					Skill:       "tdd",
					Description: "Test first",
					Reason:      "Loading skills consumes context.",
				},
				Sudo: &SudoRequest{
					Key:     "K5",
					Command: "apt install jq",
					Reason:  "needs root",
					StepID:  7,
				},
				Access: &AccessRequest{
					RequestID:     "R1",
					SessionID:     "s1",
					Reason:        "read outside",
					Current:       AccessState{Mode: "ask", Revision: 4},
					MissingSystem: true,
				},
			},
			Access: &AccessRequest{
				RequestID:      "R2",
				SessionID:      "s1",
				Actor:          "orchestrator",
				StepID:         2,
				Reason:         "edit the plan",
				Current:        AccessState{Mode: "ask", Revision: 5},
				MissingEditing: true,
			},
		},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal pending update: %v", err)
	}
	if !strings.Contains(string(raw), `"kind":"pending_requests"`) {
		t.Errorf("pending update kind:\n got: %s\nwant: %q", raw, `"kind":"pending_requests"`)
	}

	var got PendingUpdate
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal pending update: %v", err)
	}
	if got.Kind != UpdatePending {
		t.Errorf("kind: got %q, want %q", got.Kind, UpdatePending)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pending update round trip:\n got: %+v\nwant: %+v", got, want)
	}
}

// TestAnEmptyPendingStateEncodesNoRequests pins the idle state: every list and
// pointer is omitted, so a settled session sends the smallest update it can.
func TestAnEmptyPendingStateEncodesNoRequests(t *testing.T) {
	raw, err := json.Marshal(PendingState{})
	if err != nil {
		t.Fatalf("marshal empty pending state: %v", err)
	}
	const want = `{"requests":{}}`
	if string(raw) != want {
		t.Errorf("empty pending state JSON:\n got: %s\nwant: %s", raw, want)
	}
}

// TestAnswerSudoParamsCarryThePasswordAsBytes pins the sudo answer's password
// bytes and the deny that carries none.
func TestAnswerSudoParamsCarryThePasswordAsBytes(t *testing.T) {
	granted := AnswerSudoParams{
		SessionID: "s1",
		Key:       "K5",
		Answer:    SudoAnswer{OK: true, Password: []byte("pw")},
	}
	raw, err := json.Marshal(granted)
	if err != nil {
		t.Fatalf("marshal sudo answer: %v", err)
	}
	var got AnswerSudoParams
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal sudo answer: %v", err)
	}
	if !reflect.DeepEqual(got, granted) {
		t.Errorf("sudo answer round trip:\n got: %+v\nwant: %+v", got, granted)
	}
	if !reflect.DeepEqual(got.Answer.Password, []byte("pw")) {
		t.Errorf("sudo password: got %q, want %q", got.Answer.Password, "pw")
	}

	denied := AnswerSudoParams{
		SessionID: "s1",
		Key:       "K5",
		Answer:    SudoAnswer{OK: false},
	}
	raw, err = json.Marshal(denied)
	if err != nil {
		t.Fatalf("marshal sudo deny: %v", err)
	}
	if strings.Contains(string(raw), `"password"`) {
		t.Errorf("sudo deny must carry no password key:\n got: %s", raw)
	}
}
