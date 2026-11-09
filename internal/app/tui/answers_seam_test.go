package tui

import (
	"context"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acp/acptest"
	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// answerRecorder is a session whose answer calls are recorded before the
// wrapped session answers them. Every other method is the wrapped session's.
type answerRecorder struct {
	acpclient.Session
	approvals []recordedApproval
	questions []recordedQuestion
	skills    []recordedSkill
	sudo      []recordedSudo
	rollbacks int
}

// Unwrap exposes the adapter this recorder wraps, so stateOf reaches the
// state behind it.
func (r *answerRecorder) Unwrap() acpclient.Session { return r.Session }

type recordedApproval struct {
	key    string
	answer acpwire.ApprovalAnswer
}

type recordedQuestion struct {
	key     string
	answers []acpwire.Answer
}

type recordedSkill struct {
	key    string
	choice acpwire.SkillGateChoice
}

type recordedSudo struct {
	key    string
	answer acpwire.SudoAnswer
}

func (r *answerRecorder) AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error {
	r.approvals = append(r.approvals, recordedApproval{key: key, answer: answer})
	return r.Session.AnswerApproval(ctx, key, answer)
}

func (r *answerRecorder) AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error {
	r.questions = append(r.questions, recordedQuestion{key: key, answers: answers})
	return r.Session.AnswerQuestion(ctx, key, answers)
}

func (r *answerRecorder) AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error {
	r.skills = append(r.skills, recordedSkill{key: key, choice: choice})
	return r.Session.AnswerSkillGate(ctx, key, choice)
}

func (r *answerRecorder) AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error {
	// Keep a copy of the password: the wire clears the caller's buffer after
	// sending, so a shared slice reads as NULs by assertion time.
	recorded := answer
	recorded.Password = append([]byte(nil), answer.Password...)
	r.sudo = append(r.sudo, recordedSudo{key: key, answer: recorded})
	return r.Session.AnswerSudo(ctx, key, answer)
}

func (r *answerRecorder) Rollback(ctx context.Context) error {
	r.rollbacks++
	return r.Session.Rollback(ctx)
}

// testModelRecording builds a model over state whose session is rec, wrapped
// around the test agent's session, as testModelStub builds one.
func testModelRecording(t testing.TB, state *session.State, rec *answerRecorder, opts ...Option) Model {
	t.Helper()
	host, sess := acptest.Open(t, service.Deps{State: state})
	rec.Session = sess
	return New(host, rec, opts...)
}

// recordingModel builds a sized model whose session records its answers.
func recordingModel(t *testing.T, width, height int, rec *answerRecorder) Model {
	t.Helper()
	m := testModelRecording(t, seamState(t, "s1"), rec)
	m.resize(width, height)
	m.refreshViewport()
	return m
}

// TestTheApprovalFormAnswersThroughTheSession pins that the approval form, its
// standing choices and the edit sub-mode all answer through the session: every
// decision reaches AnswerApproval carrying the pending request's key, and the
// edit rides as a once answer's edited value.
func TestTheApprovalFormAnswersThroughTheSession(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	rec := &answerRecorder{}
	m := testModelRecording(t, state, rec)
	m.resize(80, 24)
	m.refreshViewport()

	// setApproval puts a fresh shell.run approval on the state and returns the
	// key the session gave it.
	setApproval := func(t *testing.T) string {
		t.Helper()
		state.SetPendingApproval(&session.PendingToolCall{
			Name:    "shell.run",
			Command: "go test ./...",
			Risk:    "command",
		})
		pending := m.sess.Pending().Approvals
		if len(pending) != 1 {
			t.Fatalf("pending approvals = %+v, want one", pending)
		}
		return pending[0].Key
	}

	// Approve: two Enters confirm the default selection.
	approveKey := setApproval(t)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// Always: the mnemonic selects the standing choice, then two Enters confirm.
	alwaysKey := setApproval(t)
	m = sendKey(m, tea.KeyPressMsg{Code: 'A', Text: "A"})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	// Edit: the edit sub-mode takes the typed value as a once answer's edit.
	editKey := setApproval(t)
	m = sendKey(m, tea.KeyPressMsg{Code: 'e', Text: "e"})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.approvalEdit == nil {
		t.Fatal("the edit sub-mode did not open")
	}
	m.approvalEdit.input.SetValue("go test ./internal/...")
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	want := []recordedApproval{
		{key: approveKey, answer: acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}},
		{key: alwaysKey, answer: acpwire.ApprovalAnswer{Decision: acpwire.ApprovalAlways}},
		{key: editKey, answer: acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce, Edited: "go test ./internal/..."}},
	}
	if !slices.Equal(rec.approvals, want) {
		t.Fatalf("recorded approvals = %+v, want %+v", rec.approvals, want)
	}
}

// TestTheQuestionFormAnswersThroughTheSession pins that the inline question
// form answers through the session: the recorder sees one question answer,
// carrying the registered request's key and the answers the form produced.
func TestTheQuestionFormAnswersThroughTheSession(t *testing.T) {
	rec := &answerRecorder{}
	m := recordingModel(t, 80, 24, rec)

	q := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Archive or delete?"}},
		ResponseChan: make(chan []session.Answer, 1),
	}
	if _, err := stateOf(m).RegisterQuestion(context.Background(), q); err != nil {
		t.Fatalf("RegisterQuestion: %v", err)
	}
	pending := m.sess.Pending().Question
	if pending == nil {
		t.Fatal("the registered question is not pending")
	}

	m = answerPendingQuestion(m)

	if len(rec.questions) != 1 {
		t.Fatalf("recorded %d question answers, want 1: %+v", len(rec.questions), rec.questions)
	}
	got := rec.questions[0]
	if got.key != pending.Key {
		t.Fatalf("recorded key = %q, want the question's key %q", got.key, pending.Key)
	}
	want := []acpwire.Answer{{Question: "Archive or delete?", Answer: "archive"}}
	if !slices.Equal(got.answers, want) {
		t.Fatalf("recorded answers = %+v, want %+v", got.answers, want)
	}
}

// TestTheSkillGateAnswersThroughTheSession pins that the skill-gate dialog
// answers through the session: the recorder sees one skill answer, carrying
// the registered gate's key and the wire choice, and the session records the
// sticky decision the adapter made.
func TestTheSkillGateAnswersThroughTheSession(t *testing.T) {
	rec := &answerRecorder{}
	m := recordingModel(t, 100, 30, rec)

	sg := &session.PendingSkillGate{Skill: "review", ResponseChan: make(chan session.SkillGateChoice, 1)}
	if _, err := stateOf(m).RegisterSkillGate(context.Background(), sg); err != nil {
		t.Fatalf("RegisterSkillGate: %v", err)
	}
	pending := m.sess.Pending().SkillGate
	if pending == nil {
		t.Fatal("the registered gate is not pending")
	}

	// As TestHandleSkillGateAllowOnceRespondsAndClears drives it: the first
	// message builds the dialog, "2" selects Allow always (this skill), and
	// Enter confirms.
	m = sendKey(m, skillGateKeyPress("enter"))
	m = sendKey(m, skillGateKeyPress("2"))
	m = sendKey(m, skillGateKeyPress("enter"))

	if len(rec.skills) != 1 {
		t.Fatalf("recorded %d skill answers, want 1: %+v", len(rec.skills), rec.skills)
	}
	got := rec.skills[0]
	if got.key != pending.Key {
		t.Fatalf("recorded key = %q, want the gate's key %q", got.key, pending.Key)
	}
	if got.choice != acpwire.SkillGateAllowSkill {
		t.Fatalf("recorded choice = %q, want %q", got.choice, acpwire.SkillGateAllowSkill)
	}
	decisions := stateOf(m).SkillGateDecisions()
	if len(decisions) != 1 || decisions[0].Skill != "review" || !decisions[0].Allowed {
		t.Fatalf("SkillGateDecisions() = %+v, want one allowed entry for review", decisions)
	}
}

// TestTheSudoPromptAnswersThroughTheSession pins that the masked privileged
// prompt answers through the session: the recorder sees one sudo answer,
// carrying the registered prompt's key, an OK decision and the typed bytes.
func TestTheSudoPromptAnswersThroughTheSession(t *testing.T) {
	rec := &answerRecorder{}
	m := recordingModel(t, 100, 30, rec)

	sa := &session.PendingSudoAuth{Command: "id", ResponseChan: make(chan session.SudoAuthDecision, 1)}
	if _, err := stateOf(m).RegisterSudoAuth(context.Background(), sa); err != nil {
		t.Fatalf("RegisterSudoAuth: %v", err)
	}
	pending := m.sess.Pending().Sudo
	if pending == nil {
		t.Fatal("the registered prompt is not pending")
	}

	m = primeSudoAuth(t, m)
	for _, r := range "pw" {
		m = sendKey(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = sendKey(m, sudoAuthKeyPress("enter"))

	if len(rec.sudo) != 1 {
		t.Fatalf("recorded %d sudo answers, want 1", len(rec.sudo))
	}
	got := rec.sudo[0]
	if got.key != pending.Key {
		t.Fatalf("recorded key = %q, want the prompt's key %q", got.key, pending.Key)
	}
	// The message never formats the password: a failure report is not a place
	// for a secret.
	if !got.answer.OK || string(got.answer.Password) != "pw" {
		t.Fatalf("the recorded answer is not OK with the typed bytes: ok=%t password=%q", got.answer.OK, string(got.answer.Password))
	}
}

// TestCtrlRRollsBackThroughTheSession pins that the second Ctrl+R rolls back
// through the session: the recorder counts one rollback, the backup is
// cleared, and the adapter's audit row is the last one.
func TestCtrlRRollsBackThroughTheSession(t *testing.T) {
	tmpDir := t.TempDir()
	state := session.New(config.Default(), tmpDir, time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	state.StoreBackup([]session.BackupFile{
		{Path: "app.go", Content: "original content", Exists: true},
	})
	rec := &answerRecorder{}
	m := testModelRecording(t, state, rec)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !state.HasBackup() {
		t.Fatal("the first Ctrl+R rolled back without confirmation")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)

	if rec.rollbacks != 1 {
		t.Fatalf("recorded %d rollbacks, want 1", rec.rollbacks)
	}
	if state.HasBackup() {
		t.Fatal("the backup survived the rollback")
	}
	log := state.AuditLog()
	if len(log) == 0 {
		t.Fatal("the rollback logged no audit row")
	}
	if last := log[len(log)-1]; last.ToolName != "rollback" || last.ResultSummary != "Rollback applied successfully" {
		t.Fatalf("last audit row = %+v, want the rollback row", last)
	}
}
