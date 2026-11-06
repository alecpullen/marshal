package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// registerApproval publishes one ordinary approval and returns its key and the
// channel its decision arrives on.
func registerApproval(t *testing.T, state *session.State, call *session.PendingToolCall) (session.PromptKey, chan session.UserApprovalDecision) {
	t.Helper()
	ch := approvalChan()
	call.ResponseChan = ch
	key, err := state.RegisterApproval(context.Background(), call)
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}
	return key, ch
}

// approvalDecision reads one decision off a settled approval's channel.
func approvalDecision(t *testing.T, ch chan session.UserApprovalDecision) session.UserApprovalDecision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the approval was never settled; the tool would block forever")
		return session.UserApprovalDecision{}
	}
}

// TestAnswerApprovalOnceAndDeny pins the two exact decisions: once approves,
// deny does not, and each settles the request.
func TestAnswerApprovalOnceAndDeny(t *testing.T) {
	for _, tc := range []struct {
		decision acpwire.ApprovalDecision
		approved bool
	}{
		{decision: acpwire.ApprovalOnce, approved: true},
		{decision: acpwire.ApprovalDeny, approved: false},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			state := newState(t, "s1")
			_, sess := New(Deps{State: state})
			key, ch := registerApproval(t, state, &session.PendingToolCall{
				ID:      "call-1",
				Name:    "shell.run",
				Command: "go test ./...",
				Risk:    "command",
			})

			if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: tc.decision}); err != nil {
				t.Fatalf("AnswerApproval: %v", err)
			}
			if got := approvalDecision(t, ch); got.Approved != tc.approved {
				t.Errorf("delivered decision = %+v, want Approved %v", got, tc.approved)
			}
			if got := sess.Pending().Approvals; len(got) != 0 {
				t.Errorf("Pending().Approvals = %+v after the answer, want none", got)
			}
		})
	}
}

// TestAnswerApprovalCarriesTheEdit pins that a once answer's edited command
// reaches the handler.
func TestAnswerApprovalCarriesTheEdit(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	key, ch := registerApproval(t, state, &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
	})

	const edited = "go test ./internal/..."
	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{
		Decision: acpwire.ApprovalOnce,
		Edited:   edited,
	}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	got := approvalDecision(t, ch)
	if !got.Approved || got.Edited != edited {
		t.Errorf("delivered decision = %+v, want an approval carrying %q", got, edited)
	}
}

// TestAStandingAnswerToAForcedApprovalDenies pins decision 5: neither session
// nor always may stand for a prompt that only an exact decision answers.
func TestAStandingAnswerToAForcedApprovalDenies(t *testing.T) {
	for _, decision := range []acpwire.ApprovalDecision{acpwire.ApprovalSession, acpwire.ApprovalAlways} {
		t.Run(string(decision), func(t *testing.T) {
			state := newState(t, "s1")
			_, sess := New(Deps{State: state})
			key, ch := registerApproval(t, state, &session.PendingToolCall{
				ID:                   "call-1",
				Name:                 "shell.run",
				Command:              "git push",
				ExactCommandApproval: true,
			})

			if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: decision}); err != nil {
				t.Fatalf("AnswerApproval: %v", err)
			}
			if got := approvalDecision(t, ch); got.Approved {
				t.Errorf("delivered decision = %+v, want a deny", got)
			}
			if rules := state.SessionRules(); len(rules) != 0 {
				t.Errorf("SessionRules() = %v, want none added by a forced deny", rules)
			}
			path := filepath.Join(userConfigDir(), "config.toml")
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "git push") {
				t.Errorf("the user config at %s carries a rule for a forced deny:\n%s", path, data)
			}
		})
	}
}

// TestASessionAnswerAddsASessionRule pins the session answer: it approves and
// records the command for the rest of the session.
func TestASessionAnswerAddsASessionRule(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	key, ch := registerApproval(t, state, &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go build ./...",
		Risk:    "command",
	})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalSession}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := approvalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}
	if got := state.SessionRules(); len(got) != 1 || got[0] != "go build ./..." {
		t.Errorf("SessionRules() = %v, want the answered command", got)
	}
}

// TestAChildsApprovalSettlesOnTheChild pins decision 6: the settlement goes to
// the state that raised the call, and the rule lands on the session.
func TestAChildsApprovalSettlesOnTheChild(t *testing.T) {
	parent := newState(t, "s1")
	_, sess := New(Deps{State: parent})

	child := newState(t, "child")
	parent.RegisterSubagent("reviewer", child)
	key, ch := registerApproval(t, child, &session.PendingToolCall{
		ID:      "call-c",
		Name:    "shell.run",
		Command: "go vet ./...",
		Risk:    "command",
	})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalSession}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := approvalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}
	if got := parent.SessionRules(); len(got) != 1 || got[0] != "go vet ./..." {
		t.Errorf("the parent's SessionRules() = %v, want the child's command", got)
	}
	if got := child.SessionRules(); len(got) != 0 {
		t.Errorf("the child's SessionRules() = %v, want none", got)
	}
}

// TestAnAnswerToASettledApprovalIsRequestSettled pins the key of an answer to
// a request that is no longer open.
func TestAnAnswerToASettledApprovalIsRequestSettled(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	key, ch := registerApproval(t, state, &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
	})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	approvalDecision(t, ch)

	err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalDeny})
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerApproval = %v, want a request-settled code", err)
	}
}

// TestAnUnregisteredApprovalIsAnsweredOnItsChannel pins the fallback a legacy
// fixture keeps: no registry key, its own channel, and an emptied slot.
func TestAnUnregisteredApprovalIsAnsweredOnItsChannel(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	ch := approvalChan()
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		ResponseChan: ch,
	})
	approvals := sess.Pending().Approvals
	if len(approvals) != 1 {
		t.Fatalf("Pending().Approvals = %+v, want the one call", approvals)
	}

	if err := sess.AnswerApproval(context.Background(), approvals[0].Key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := approvalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}
	if got := state.PendingApproval(); got != nil {
		t.Errorf("PendingApproval() = %+v after the answer, want the slot empty", got)
	}
}

// TestAnAccessApprovalIsNotAnsweredAsAnApproval pins that a typed access
// request borrows the approval slot but settles only through AnswerAccess.
func TestAnAccessApprovalIsNotAnsweredAsAnApproval(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	if _, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"}); err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}
	approvals := sess.Pending().Approvals
	if len(approvals) != 1 {
		t.Fatalf("Pending().Approvals = %+v, want the access request", approvals)
	}

	if err := sess.AnswerApproval(context.Background(), approvals[0].Key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err == nil {
		t.Fatal("AnswerApproval settled a typed access request")
	}
	if _, ok := state.AccessRequest(); !ok {
		t.Error("the access request left the slot; AnswerApproval settled it")
	}
}

// TestAnAlwaysAnswerSavesAndAppliesTheRule pins the always answer: the rule is
// saved to the user config and applied to the session's.
func TestAnAlwaysAnswerSavesAndAppliesTheRule(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	call := &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
	}
	key, ch := registerApproval(t, state, call)

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalAlways}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := approvalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}

	pattern := "go test ./..."
	path := filepath.Join(userConfigDir(), "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the saved user config at %s: %v", path, err)
	}
	if !strings.Contains(string(data), pattern) {
		t.Errorf("the saved user config does not carry the pattern %q:\n%s", pattern, data)
	}

	var found bool
	for _, rule := range state.Config.Permissions.Rules {
		if rule.Permission == "shell" && rule.Pattern == pattern && rule.Action == "allow" {
			found = true
		}
	}
	if !found {
		t.Errorf("state.Config.Permissions.Rules = %+v, want a shell allow rule for %q",
			state.Config.Permissions.Rules, pattern)
	}
}

// TestAnAlwaysAnswerFallsBackToASessionRule pins the always answer whose save
// fails: the session rule is the fallback and the config gains nothing.
func TestAnAlwaysAnswerFallsBackToASessionRule(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	// A regular file where the config directory belongs makes every save fail.
	cfgDir := filepath.Join(home, ".config", "marshal")
	if err := os.RemoveAll(cfgDir); err != nil {
		t.Fatalf("clear the config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgDir), 0o755); err != nil {
		t.Fatalf("create the config parent: %v", err)
	}
	if err := os.WriteFile(cfgDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("block the config dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(cfgDir) })

	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	before := len(state.Config.Permissions.Rules)
	key, ch := registerApproval(t, state, &session.PendingToolCall{
		ID:      "call-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
	})

	if err := sess.AnswerApproval(context.Background(), key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalAlways}); err != nil {
		t.Fatalf("AnswerApproval: %v", err)
	}
	if got := approvalDecision(t, ch); !got.Approved {
		t.Errorf("delivered decision = %+v, want an approval", got)
	}
	if got := state.SessionRules(); len(got) != 1 || got[0] != "go test ./..." {
		t.Errorf("SessionRules() = %v, want the answered command", got)
	}
	if got := len(state.Config.Permissions.Rules); got != before {
		t.Errorf("state.Config.Permissions.Rules grew from %d to %d rules after a failed save", before, got)
	}
}

// TestAnswerQuestionDeliversTheAnswers pins the question answer: one answer
// per question, in order, and a second answer to the settled question.
func TestAnswerQuestionDeliversTheAnswers(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	ch := make(chan []session.Answer, 1)
	key, err := state.RegisterQuestion(context.Background(), &session.PendingQuestion{
		Questions: []session.Question{
			{Question: "Which file?"},
			{Question: "Which mode?"},
		},
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}

	answers := []acpwire.Answer{
		{Question: "Which file?", Answer: "app.go"},
		{Question: "Which mode?", Answer: "act"},
	}
	if err := sess.AnswerQuestion(context.Background(), key.ID, answers); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	select {
	case got := <-ch:
		want := []session.Answer{
			{Question: "Which file?", Answer: "app.go"},
			{Question: "Which mode?", Answer: "act"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("delivered answers = %+v, want %+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was never answered; the tool would block forever")
	}

	err = sess.AnswerQuestion(context.Background(), key.ID, answers)
	if !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerQuestion = %v, want a request-settled code", err)
	}
}

// TestAnswerSkillGateRecordsTheChoice pins each skill-gate answer's delivery
// and the sticky record the allow choices leave behind. The row that names no
// choice settles nothing.
func TestAnswerSkillGateRecordsTheChoice(t *testing.T) {
	for _, tc := range []struct {
		choice acpwire.SkillGateChoice
		mapped session.SkillGateChoice
	}{
		{choice: acpwire.SkillGateAllowOnce, mapped: session.SkillGateAllowOnce},
		{choice: acpwire.SkillGateAllowSkill, mapped: session.SkillGateAllowSkill},
		{choice: acpwire.SkillGateAllowAll, mapped: session.SkillGateAllowAll},
		{choice: acpwire.SkillGateDeny, mapped: session.SkillGateDeny},
	} {
		t.Run(string(tc.choice), func(t *testing.T) {
			state := newState(t, "s1")
			_, sess := New(Deps{State: state})
			ch := make(chan session.SkillGateChoice, 1)
			key, err := state.RegisterSkillGate(context.Background(), &session.PendingSkillGate{
				Skill:        "tdd",
				Description:  "Test first",
				Reason:       "Loading skills consumes context.",
				ResponseChan: ch,
			})
			if err != nil {
				t.Fatalf("RegisterSkillGate: %v", err)
			}

			if err := sess.AnswerSkillGate(context.Background(), key.ID, tc.choice); err != nil {
				t.Fatalf("AnswerSkillGate: %v", err)
			}
			select {
			case got := <-ch:
				if got != tc.mapped {
					t.Errorf("delivered choice = %v, want %v", got, tc.mapped)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the gate was never answered; the runner would block forever")
			}

			switch tc.choice {
			case acpwire.SkillGateAllowSkill:
				got := state.SkillGateDecisions()
				if len(got) != 1 || got[0].Skill != "tdd" || !got[0].Allowed {
					t.Errorf("SkillGateDecisions() = %+v, want tdd allowed", got)
				}
			case acpwire.SkillGateAllowAll:
				if state.SkillGateEnabled() {
					t.Error("SkillGateEnabled() = true after allow_all, want the gate off")
				}
			default:
				if got := state.SkillGateDecisions(); len(got) != 0 {
					t.Errorf("SkillGateDecisions() = %+v, want none recorded", got)
				}
				if !state.SkillGateEnabled() {
					t.Error("SkillGateEnabled() = false, want the gate untouched")
				}
			}

			if err := sess.AnswerSkillGate(context.Background(), key.ID, tc.choice); !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
				t.Errorf("second AnswerSkillGate = %v, want a request-settled code", err)
			}
		})
	}

	t.Run("unknown choice", func(t *testing.T) {
		state := newState(t, "s1")
		_, sess := New(Deps{State: state})
		key, err := state.RegisterSkillGate(context.Background(), &session.PendingSkillGate{
			Skill:        "tdd",
			ResponseChan: make(chan session.SkillGateChoice, 1),
		})
		if err != nil {
			t.Fatalf("RegisterSkillGate: %v", err)
		}
		if err := sess.AnswerSkillGate(context.Background(), key.ID, acpwire.SkillGateChoice("maybe")); err == nil {
			t.Fatal("AnswerSkillGate accepted an unknown choice")
		}
		if sess.Pending().SkillGate == nil {
			t.Error("the gate settled on an unknown choice, want it still pending")
		}
	})
}

// TestAnswerSudoHandsThePasswordOver pins that the handler receives the very
// slice the person answered with.
func TestAnswerSudoHandsThePasswordOver(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	ch := make(chan session.SudoAuthDecision, 1)
	key, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
		Command:      "apt install jq",
		Reason:       "needs root",
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}

	password := []byte("hunter2")
	if err := sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: true, Password: password}); err != nil {
		t.Fatalf("AnswerSudo: %v", err)
	}
	select {
	case got := <-ch:
		if !got.OK {
			t.Fatalf("delivered decision = %+v, want OK", got)
		}
		if len(got.Password) == 0 || &got.Password[0] != &password[0] {
			t.Errorf("delivered password %q is not the slice the person answered with", got.Password)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sudo prompt was never answered; the tool would block forever")
	}

	if err := sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: false}); !acpclient.IsCode(err, acpwire.CodeRequestSettled) {
		t.Errorf("second AnswerSudo = %v, want a request-settled code", err)
	}
}

// TestASudoPasswordIsInNoRead pins T7: the password appears in no read, in no
// log, and in no base64 encoding of either.
func TestASudoPasswordIsInNoRead(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	ch := make(chan session.SudoAuthDecision, 1)
	key, err := state.RegisterSudoAuth(context.Background(), &session.PendingSudoAuth{
		Command:      "apt install jq",
		Reason:       "needs root",
		ResponseChan: ch,
	})
	if err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}
	if err := sess.AnswerSudo(context.Background(), key.ID, acpwire.SudoAnswer{OK: true, Password: []byte("hunter2")}); err != nil {
		t.Fatalf("AnswerSudo: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("the sudo prompt was never answered")
	}

	statusJSON, err := json.Marshal(sess.Status())
	if err != nil {
		t.Fatalf("marshal Status: %v", err)
	}
	pendingJSON, err := json.Marshal(sess.Pending())
	if err != nil {
		t.Fatalf("marshal Pending: %v", err)
	}
	secrets := []string{"hunter2", base64.StdEncoding.EncodeToString([]byte("hunter2"))}
	reads := map[string]string{
		"Status()":  string(statusJSON),
		"Pending()": string(pendingJSON),
		"the logs":  buf.String(),
	}
	for where, read := range reads {
		for _, secret := range secrets {
			if strings.Contains(read, secret) {
				t.Errorf("%s carries the password %q: %s", where, secret, read)
			}
		}
	}
}

// TestReconcileAccessReadsTheRuntime pins that the request comes from the
// runtime's seam, in the wire form Pending reports.
func TestReconcileAccessReadsTheRuntime(t *testing.T) {
	state := newState(t, "s1")
	calls := 0
	_, sess := New(Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			calls++
			return state.AccessRequest()
		},
		ResolveAccess: func(session.AccessChoice) (session.AccessResult, error) {
			return session.AccessResult{}, nil
		},
	})
	req, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}

	got, ok := sess.ReconcileAccess(context.Background())
	if !ok {
		t.Fatal("ReconcileAccess reported no request, want the registered one")
	}
	if got.RequestID != req.RequestID {
		t.Errorf("RequestID = %q, want %q", got.RequestID, req.RequestID)
	}
	if got.SessionID != state.SessionID() {
		t.Errorf("SessionID = %q, want %q", got.SessionID, state.SessionID())
	}
	if got.Reason != req.Reason || !got.MissingSystem || got.MissingEditing {
		t.Errorf("reconciled request = %+v, want the registered request's reason and flags", got)
	}
	if calls != 1 {
		t.Errorf("the runtime's getter was called %d times, want once", calls)
	}
}

// TestReconcileAccessSkipsAnotherSessionsRequest pins that a request another
// session owns is neither presented nor settled.
func TestReconcileAccessSkipsAnotherSessionsRequest(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			req, ok := state.AccessRequest()
			req.SessionID = "other"
			return req, ok
		},
		ResolveAccess: func(session.AccessChoice) (session.AccessResult, error) {
			return session.AccessResult{}, nil
		},
	})
	if _, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"}); err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}

	if _, ok := sess.ReconcileAccess(context.Background()); ok {
		t.Error("ReconcileAccess presented a request another session owns")
	}
	if _, ok := state.AccessRequest(); !ok {
		t.Error("the request left the slot; ReconcileAccess settled another session's request")
	}
}

// TestReconcileAccessNeedsBothCallbacks pins the fail-closed rule: without the
// whole seam, ReconcileAccess settles the request as failed rather than
// presenting one nobody can answer.
func TestReconcileAccessNeedsBothCallbacks(t *testing.T) {
	for _, tc := range []struct {
		name          string
		accessRequest func() (session.AccessRequest, bool)
		resolveAccess func(session.AccessChoice) (session.AccessResult, error)
	}{
		{name: "no callbacks"},
		{name: "getter only", accessRequest: func() (session.AccessRequest, bool) {
			return session.AccessRequest{}, false
		}},
		{name: "resolver only", resolveAccess: func(session.AccessChoice) (session.AccessResult, error) {
			return session.AccessResult{}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newState(t, "s1")
			_, sess := New(Deps{State: state, AccessRequest: tc.accessRequest, ResolveAccess: tc.resolveAccess})
			call := &session.PendingToolCall{
				ID:           "call-1",
				Name:         "mode.request",
				ResponseChan: approvalChan(),
			}
			if _, waiting, err := state.RegisterAccess(context.Background(), call,
				session.AccessIntent{System: true, Reason: "read outside"}); err != nil || !waiting {
				t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
			}

			if _, ok := sess.ReconcileAccess(context.Background()); ok {
				t.Error("a session without the whole seam presented a request")
			}
			select {
			case d := <-call.ResponseChan:
				if d.Approved {
					t.Fatalf("delivered decision = %+v, want a failure", d)
				}
				if d.Access == nil || d.Access.Status != session.AccessFailed {
					t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the request was never settled; the tool would block forever")
			}
		})
	}
}

// TestAnswerAccessResolvesThroughTheRuntime pins the answer's round trip: the
// choice reaches the runtime converted, and the result comes back in wire form.
func TestAnswerAccessResolvesThroughTheRuntime(t *testing.T) {
	state := newState(t, "s1")
	var recorded session.AccessChoice
	_, sess := New(Deps{
		State: state,
		AccessRequest: func() (session.AccessRequest, bool) {
			return state.AccessRequest()
		},
		ResolveAccess: func(choice session.AccessChoice) (session.AccessResult, error) {
			recorded = choice
			return session.AccessResult{
				RequestID: choice.RequestID,
				SessionID: choice.SessionID,
				Status:    session.AccessGranted,
				Effective: session.AccessState{Mode: "act", System: true},
				Message:   "access granted",
			}, nil
		},
	})
	req, waiting, err := state.RegisterAccess(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}, session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}

	choice := acpwire.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current: acpwire.AccessState{
			Mode: req.Current.Mode, Auto: req.Current.Auto,
			System: req.Current.System, Revision: req.Current.Revision,
		},
		Approved: true,
	}
	result, err := sess.AnswerAccess(context.Background(), choice)
	if err != nil {
		t.Fatalf("AnswerAccess: %v", err)
	}
	if result.Status != acpwire.AccessGranted {
		t.Errorf("result.Status = %q, want %q", result.Status, acpwire.AccessGranted)
	}
	if got := result.Effective; got.Mode != "act" || !got.System {
		t.Errorf("result.Effective = %+v, want act with system access", got)
	}
	want := session.AccessChoice{
		RequestID: choice.RequestID,
		SessionID: choice.SessionID,
		Current: session.AccessState{
			Mode: choice.Current.Mode, Auto: choice.Current.Auto,
			System: choice.Current.System, Revision: choice.Current.Revision,
		},
		Approved: choice.Approved,
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Errorf("the runtime recorded %+v, want the wire choice converted %+v", recorded, want)
	}
}

// TestAnswerAccessWithoutAResolverFailsClosed pins that an answer the session
// cannot resolve settles the request as failed and reports why.
func TestAnswerAccessWithoutAResolverFailsClosed(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	call := &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		ResponseChan: approvalChan(),
	}
	req, waiting, err := state.RegisterAccess(context.Background(), call,
		session.AccessIntent{System: true, Reason: "read outside"})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%v, %v), want a published request", waiting, err)
	}

	if _, err := sess.AnswerAccess(context.Background(), acpwire.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Approved:  true,
	}); err == nil {
		t.Fatal("AnswerAccess without a resolver reported success")
	}
	select {
	case d := <-call.ResponseChan:
		if d.Approved {
			t.Fatalf("delivered decision = %+v, want a failure", d)
		}
		if d.Access == nil || d.Access.Status != session.AccessFailed {
			t.Fatalf("delivered result = %+v, want a typed failure", d.Access)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request was never settled; the tool would block forever")
	}
}

// TestRollbackRestoresAndLogs pins the two rollback paths: the restore and its
// audit row, whether the restore succeeded or failed.
func TestRollbackRestoresAndLogs(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	state.StoreBackup([]session.BackupFile{{Path: "app.go", Content: "original", Exists: true, Mode: 0o644}})
	path := filepath.Join(state.WorkingDir, "app.go")
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatalf("write app.go: %v", err)
	}
	if err := sess.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("app.go = %q after the rollback, want the backed-up content", data)
	}
	log := state.AuditLog()
	if len(log) == 0 {
		t.Fatal("the rollback logged no audit row")
	}
	last := log[len(log)-1]
	if last.ToolName != "rollback" || last.ResultSummary != "Rollback applied successfully" {
		t.Errorf("last audit row = %+v, want a successful rollback", last)
	}

	// A path this session cannot write makes the restore fail, and the failure
	// reaches the audit trail rather than being hidden.
	if err := os.WriteFile(filepath.Join(state.WorkingDir, "blocker"), []byte("regular file"), 0o644); err != nil {
		t.Fatalf("write the blocker: %v", err)
	}
	state.StoreBackup([]session.BackupFile{{Path: "blocker/x.go", Content: "restored", Exists: true, Mode: 0o644}})
	if err := sess.Rollback(context.Background()); err == nil {
		t.Fatal("Rollback reported success for a restore that could not succeed")
	}
	log = state.AuditLog()
	last = log[len(log)-1]
	if last.ToolName != "rollback" || last.ResultSummary != "Rollback failed" || last.Error == "" {
		t.Errorf("last audit row = %+v, want a failed rollback carrying its error", last)
	}
}

// TestAClosedSessionAnswersNothing pins the closed-session check every answer
// call makes before anything else.
func TestAClosedSessionAnswersNothing(t *testing.T) {
	state := newState(t, "s1")
	host, old := New(Deps{State: state})
	key, err := state.RegisterApproval(context.Background(), &session.PendingToolCall{
		ID:           "call-1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		ResponseChan: approvalChan(),
	})
	if err != nil {
		t.Fatalf("RegisterApproval: %v", err)
	}

	host.swap(newState(t, "s2"))

	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "AnswerApproval", call: func() error {
			return old.AnswerApproval(ctx, key.ID, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce})
		}},
		{name: "AnswerQuestion", call: func() error {
			return old.AnswerQuestion(ctx, key.ID, []acpwire.Answer{{Question: "q", Answer: "a"}})
		}},
		{name: "AnswerSkillGate", call: func() error {
			return old.AnswerSkillGate(ctx, key.ID, acpwire.SkillGateAllowOnce)
		}},
		{name: "AnswerSudo", call: func() error {
			return old.AnswerSudo(ctx, key.ID, acpwire.SudoAnswer{OK: true})
		}},
		{name: "AnswerAccess", call: func() error {
			_, err := old.AnswerAccess(ctx, acpwire.AccessChoice{RequestID: "r1", SessionID: "s1"})
			return err
		}},
		{name: "Rollback", call: func() error {
			return old.Rollback(ctx)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
				t.Errorf("%s on a closed session = %v, want a session-closed code", tc.name, err)
			}
		})
	}
	if _, ok := old.ReconcileAccess(ctx); ok {
		t.Error("a closed session reconciled an access request")
	}
	if state.PendingApproval() == nil {
		t.Error("the closed session's approval left the state; an answer settled it")
	}
}
