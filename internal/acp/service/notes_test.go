package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
)

// TestAddNoteAppendsASystemMessage pins the default: a note with no role is the
// agent's system message on the plain content type (C9).
func TestAddNoteAppendsASystemMessage(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	if err := sess.AddNote(context.Background(), acpwire.Note{Text: "heads up"}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	got := msgs[0]
	if got.Role != session.RoleSystem {
		t.Errorf("role = %q, want %q", got.Role, session.RoleSystem)
	}
	if got.ContentType != session.ContentTypePlain {
		t.Errorf("content type = %q, want %q", got.ContentType, session.ContentTypePlain)
	}
	if got.Content != "heads up" {
		t.Errorf("content = %q, want %q", got.Content, "heads up")
	}
}

// TestAddNoteRecordsTheUsersText checks a user note is stored as the user's
// message, so the next turn sees what the person typed.
func TestAddNoteRecordsTheUsersText(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	err := sess.AddNote(context.Background(), acpwire.Note{Text: "mine", Role: acpwire.RoleUser})
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != session.RoleUser {
		t.Errorf("role = %q, want %q", msgs[0].Role, session.RoleUser)
	}
}

// TestAddNoteRefusesOtherRoles checks a note cannot impersonate the assistant:
// only system and user are accepted.
func TestAddNoteRefusesOtherRoles(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	err := sess.AddNote(context.Background(), acpwire.Note{Text: "hi", Role: acpwire.RoleAssistant})
	if !errors.Is(err, errNoteRole) {
		t.Fatalf("AddNote(assistant) = %v, want errNoteRole", err)
	}
	if got := len(state.Messages()); got != 0 {
		t.Errorf("messages = %d, want 0: a refused note appends nothing", got)
	}
}

// TestNoticeCallsChangeTheBanner pins the four notice calls through the state
// they set: clearing is by category, and dismissing ignores the category.
func TestNoticeCallsChangeTheBanner(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	ctx := context.Background()

	if err := sess.SetNotice(ctx, acpwire.Notice{
		Category: acpwire.NoticeProvider,
		Severity: acpwire.NoticeWarn,
		Message:  "slow model",
	}); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}
	got := sess.Status().Notice
	if got == nil {
		t.Fatalf("Status().Notice = nil, want the notice just set")
	}
	if got.Category != acpwire.NoticeProvider {
		t.Errorf("category = %q, want %q", got.Category, acpwire.NoticeProvider)
	}
	if got.Severity != acpwire.NoticeWarn {
		t.Errorf("severity = %q, want %q", got.Severity, acpwire.NoticeWarn)
	}
	if got.Message != "slow model" {
		t.Errorf("message = %q, want %q", got.Message, "slow model")
	}
	if got.SetAt == 0 {
		t.Errorf("SetAt = 0, want the clock's stamp")
	}

	// A success in another subsystem leaves the provider's notice alone.
	if err := sess.ClearNotice(ctx, acpwire.NoticeConfig); err != nil {
		t.Fatalf("ClearNotice(config): %v", err)
	}
	if sess.Status().Notice == nil {
		t.Fatalf("Status().Notice = nil after clearing another category, want it kept")
	}
	if err := sess.ClearNotice(ctx, acpwire.NoticeProvider); err != nil {
		t.Fatalf("ClearNotice(provider): %v", err)
	}
	if got := sess.Status().Notice; got != nil {
		t.Errorf("Status().Notice = %+v after clearing its own category, want nil", got)
	}

	if err := sess.SetNotice(ctx, acpwire.Notice{
		Category: acpwire.NoticeTool,
		Message:  "tool failed",
	}); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}
	if err := sess.DismissNotice(ctx); err != nil {
		t.Fatalf("DismissNotice: %v", err)
	}
	if got := sess.Status().Notice; got != nil {
		t.Errorf("Status().Notice = %+v after DismissNotice, want nil", got)
	}
}

// TestStatusCarriesTheRuntimesNotice checks a notice the runtime set directly
// reaches the wire with every field, its time in milliseconds.
func TestStatusCarriesTheRuntimesNotice(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})
	state.SetNotice(session.Notice{
		Category: session.NoticeTool,
		Severity: session.SeverityWarn,
		Message:  "command timed out",
		Hint:     "try a narrower test",
		Source:   "shell.run",
		SetAt:    time.UnixMilli(1700000000000),
	})

	got := sess.Status().Notice
	if got == nil {
		t.Fatalf("Status().Notice = nil, want the runtime's notice")
	}
	want := acpwire.Notice{
		Category: acpwire.NoticeTool,
		Severity: acpwire.NoticeWarn,
		Message:  "command timed out",
		Hint:     "try a narrower test",
		Source:   "shell.run",
		SetAt:    1700000000000,
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("Status().Notice = %+v, want %+v", *got, want)
	}
}

// TestRunErrorCarriesTheNotice pins the run-error classifier (decision 4): a
// provider failure becomes a coded error carrying the banner its caller shows,
// and every other error comes back untouched.
func TestRunErrorCarriesTheNotice(t *testing.T) {
	const reviewHint = "Run /connect to review the provider, or /models to pick another model."

	plain := errors.New("plain failure")
	providerErr := &provider.ProviderError{Provider: "openai", StatusCode: 503, Body: "unavailable"}
	wrappedRequest := fmt.Errorf("turn 3: %w", &provider.RequestError{
		Provider: "openai",
		Op:       "chat request",
		Err:      errors.New("connection refused"),
	})
	authErr := &oauth.ErrAuthRequired{ServerName: "codex", Reason: "expired"}
	authWrapped := fmt.Errorf("provider call: %w", &oauth.ErrAuthRequired{ServerName: "codex", Reason: "expired"})
	namelessAuth := &oauth.ErrAuthRequired{Reason: "expired"}
	canceledProvider := &provider.RequestError{Provider: "openai", Op: "chat request", Err: context.Canceled}

	cases := []struct {
		name      string
		err       error
		code      acpwire.ErrorCode
		hint      string
		unchanged bool
	}{
		{name: "a provider error", err: providerErr, code: acpwire.CodeProviderError, hint: reviewHint},
		{name: "a wrapped request error", err: wrappedRequest, code: acpwire.CodeProviderError, hint: reviewHint},
		{name: "a named auth error", err: authErr, code: acpwire.CodeAuthRequired, hint: "Run /connect to sign in to codex."},
		{name: "a wrapped auth error", err: authWrapped, code: acpwire.CodeAuthRequired, hint: "Run /connect to sign in to codex."},
		{name: "a nameless auth error", err: namelessAuth, code: acpwire.CodeAuthRequired, hint: "Run /connect to sign in to this provider."},
		{name: "a plain error", err: plain, unchanged: true},
		{name: "a canceled provider error", err: canceledProvider, unchanged: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runError(tc.err)
			if tc.unchanged {
				if got != tc.err {
					t.Fatalf("runError(%v) = %v, want the same error value", tc.err, got)
				}
				return
			}
			code, ok := acpclient.CodeOf(got)
			if !ok {
				t.Fatalf("runError(%v) carries no code, want %q", tc.err, tc.code)
			}
			if code != tc.code {
				t.Errorf("code = %q, want %q", code, tc.code)
			}
			if got.Error() != tc.err.Error() {
				t.Errorf("message = %q, want the original's %q", got.Error(), tc.err.Error())
			}
			notice, ok := acpclient.NoticeOf(got)
			if !ok {
				t.Fatalf("NoticeOf(runError(%v)) reported no notice, want one", tc.err)
			}
			if notice.Category != acpwire.NoticeProvider {
				t.Errorf("notice category = %q, want %q", notice.Category, acpwire.NoticeProvider)
			}
			if notice.Severity != acpwire.NoticeError {
				t.Errorf("notice severity = %q, want %q", notice.Severity, acpwire.NoticeError)
			}
			if want := firstLine(tc.err.Error()); notice.Message != want {
				t.Errorf("notice message = %q, want the error's first line %q", notice.Message, want)
			}
			if notice.Hint != tc.hint {
				t.Errorf("notice hint = %q, want %q", notice.Hint, tc.hint)
			}
			if notice.Source != "" {
				t.Errorf("notice source = %q, want empty for the caller to stamp", notice.Source)
			}
		})
	}

	if got := runError(nil); got != nil {
		t.Errorf("runError(nil) = %v, want nil", got)
	}
}

// TestWireConstantsMatchTheSession keeps the wire's copies of the session's
// constants honest (decision 11): a rename on one side must not silently
// decode as something else.
func TestWireConstantsMatchTheSession(t *testing.T) {
	roles := []struct {
		name  string
		wire  string
		state session.Role
	}{
		{"system", acpwire.RoleSystem, session.RoleSystem},
		{"user", acpwire.RoleUser, session.RoleUser},
		{"assistant", acpwire.RoleAssistant, session.RoleAssistant},
	}
	for _, r := range roles {
		if r.wire != string(r.state) {
			t.Errorf("role %s: wire %q, session %q", r.name, r.wire, r.state)
		}
	}

	contentTypes := []struct {
		wire  string
		state session.ContentType
	}{
		{acpwire.ContentTypePlain, session.ContentTypePlain},
		{acpwire.ContentTypeMarkdown, session.ContentTypeMarkdown},
		{acpwire.ContentTypeCode, session.ContentTypeCode},
		{acpwire.ContentTypePlan, session.ContentTypePlan},
		{acpwire.ContentTypeDiff, session.ContentTypeDiff},
		{acpwire.ContentTypeToolResult, session.ContentTypeToolResult},
		{acpwire.ContentTypeSkill, session.ContentTypeSkill},
		{acpwire.ContentTypeSkillBody, session.ContentTypeSkillBody},
		{acpwire.ContentTypeNarration, session.ContentTypeNarration},
		{acpwire.ContentTypeCompaction, session.ContentTypeCompaction},
		{acpwire.ContentTypeSubagentReport, session.ContentTypeSubagentReport},
		{acpwire.ContentTypeSubagentNotice, session.ContentTypeSubagentNotice},
		{acpwire.ContentTypeWatchReport, session.ContentTypeWatchReport},
		{acpwire.ContentTypeSteering, session.ContentTypeSteering},
		{acpwire.ContentTypeSkillAuto, session.ContentTypeSkillAuto},
	}
	for _, c := range contentTypes {
		if c.wire != string(c.state) {
			t.Errorf("content type: wire %q, session %q", c.wire, c.state)
		}
	}

	activities := []struct {
		wire  string
		state session.ActivityKind
	}{
		{acpwire.ActivityIdle, session.ActivityIdle},
		{acpwire.ActivityThinking, session.ActivityThinking},
		{acpwire.ActivityTool, session.ActivityTool},
		{acpwire.ActivityApproval, session.ActivityApproval},
		{acpwire.ActivityQuestion, session.ActivityQuestion},
		{acpwire.ActivityReconnecting, session.ActivityReconnecting},
	}
	for _, a := range activities {
		if a.wire != string(a.state) {
			t.Errorf("activity: wire %q, session %q", a.wire, a.state)
		}
	}

	categories := []struct {
		wire  string
		state session.NoticeCategory
	}{
		{acpwire.NoticeProvider, session.NoticeProvider},
		{acpwire.NoticeConfig, session.NoticeConfig},
		{acpwire.NoticeTool, session.NoticeTool},
		{acpwire.NoticeInternal, session.NoticeInternal},
	}
	for _, c := range categories {
		if c.wire != c.state.String() {
			t.Errorf("notice category: wire %q, session %q", c.wire, c.state.String())
		}
	}
}

// TestAClosedSessionTakesNoNotes pins C4 for the calls this plan adds: a session
// a swap replaced refuses them, and writes nothing.
func TestAClosedSessionTakesNoNotes(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name:    "echo",
		Handler: func(*session.State, []string) commands.Result { return commands.Text("ok") },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	h, old := New(Deps{State: state, Commands: reg})
	if _, err := old.Command(ctx, "echo", nil); err != nil {
		t.Fatalf("Command before the swap: %v", err)
	}
	h.swap(newState(t, "s2"))

	calls := map[string]error{
		"AddNote":       old.AddNote(ctx, acpwire.Note{Text: "late"}),
		"SetNotice":     old.SetNotice(ctx, acpwire.Notice{Category: acpwire.NoticeProvider, Message: "late"}),
		"ClearNotice":   old.ClearNotice(ctx, acpwire.NoticeProvider),
		"DismissNotice": old.DismissNotice(ctx),
	}
	_, commandErr := old.Command(ctx, "echo", nil)
	calls["Command"] = commandErr
	_, actionErr := old.CommandAction(ctx, "a1")
	calls["CommandAction"] = actionErr

	for name, err := range calls {
		if !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
			t.Errorf("%s on a closed session = %v, want %q", name, err, acpwire.CodeSessionClosed)
		}
	}
	if got := len(state.Messages()); got != 0 {
		t.Errorf("messages = %d after the closed-session calls, want 0", got)
	}
	if notice, ok := state.Notice(); ok {
		t.Errorf("notice = %+v after the closed-session calls, want none", notice)
	}
}
