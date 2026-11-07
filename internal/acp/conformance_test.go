package acp

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/repo/gitinfo"
)

// seam is the part of acpclient.Session that both arms implement (spec V1).
// Each group's plan adds its methods; plan 8 replaces it with
// acpclient.Session.
type seam interface {
	ID() string
	Status() acpwire.SessionStatus
	SetNotice(ctx context.Context, notice acpwire.Notice) error
	ClearNotice(ctx context.Context, category string) error
	DismissNotice(ctx context.Context) error
}

// seamArm opens one implementation of the seam over d.
type seamArm struct {
	name string
	open func(t *testing.T, d service.Deps) seam
}

// seamArms returns the two implementations: the service itself, and the
// client's mirror of it over a pipe (V1).
func seamArms() []seamArm {
	return []seamArm{
		{name: "service", open: openServiceSeam},
		{name: "pipe", open: openPipeSeam},
	}
}

// openServiceSeam is the service arm: the session service.New builds for d.
func openServiceSeam(t *testing.T, d service.Deps) seam {
	t.Helper()
	_, sess := service.New(d)
	t.Cleanup(sess.Release)
	return sess
}

// openPipeSeam is the pipe arm: a server serving the status handlers over d's
// state, and a marshal client's session attached to it.
func openPipeSeam(t *testing.T, d service.Deps) seam {
	t.Helper()
	return openPipeSeamTB(t, d)
}

// openPipeSeamTB is openPipeSeam for a benchmark, whose *testing.B the arm's
// *testing.T does not accept. Both ends close with the test, as
// startTestAgent closes them.
func openPipeSeamTB(tb testing.TB, d service.Deps) seam {
	tb.Helper()

	_, served := service.New(d)
	// A state built without a persisted session ID — the 4a fixture's — has no
	// ID setter, so it is served under the fixture ID the stack tests use.
	// Every other state is served under its own, and the two must agree: a
	// case that attached an id the agent never had would pass for the wrong
	// reason.
	sessionID := served.ID()
	if sessionID == "" {
		sessionID = stackWireSession
	} else if want := d.State.SessionID(); sessionID != want {
		tb.Fatalf("the seam serves %q but the state's session ID is %q", sessionID, want)
	}

	client, agent := rpc.Pipe()
	srv := NewServer(agent, agent, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	// The agent's end sets its own cap; neither end inherits the default (P3 R2).
	srv.MaxLineBytes = rpc.MaxFrameBytes
	registerInitialize(srv)
	statuses := NewStatusManager(StatusManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == sessionID
		},
		Notify:  srv.Notify,
		Marshal: srv.marshalClient,
	})
	registerStatus(srv, statuses)
	srv.SetResponseBarrier(responseBarrier(nil, nil, nil, statuses))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	tb.Cleanup(func() {
		cancel()
		_ = agent.Close()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			tb.Error("the server did not stop")
		}
	})

	host, err := rpc.Dial(ctx, client, rpc.DialOptions{SharedFilesystem: true})
	if err != nil {
		tb.Fatalf("dial: %v", err)
	}
	tb.Cleanup(func() {
		statuses.Close()
		served.Release()
		_ = host.Close()
	})

	sess, err := host.Attach(context.Background(), sessionID)
	if err != nil {
		tb.Fatalf("attach %q: %v", sessionID, err)
	}
	return sess
}

// seamState builds a state the way the TUI tests do. It is the service's
// newState, copied here so the harness does not depend on its package.
func seamState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

// seamPtr is the address of v, for the interaction patch's optional fields.
func seamPtr[T any](v T) *T { return &v }

// TestStatusConformance runs every status case against both arms of the seam:
// the service itself, and the client's mirror of it over a pipe. The same
// answers from both is V1.
func TestStatusConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "a fresh state", run: statusOfAFreshState},
		{name: "the status mirrors the state", run: statusMirrorsTheState},
		{name: "the notice calls change the banner", run: noticeCallsChangeTheBanner},
		{name: "a notice category that matches nothing is refused", run: aNoticeCategoryThatMatchesNothingIsRefused},
		{name: "the runtime's notice reaches the status", run: statusCarriesTheRuntimesNotice},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// statusOfAFreshState pins what a session that has done nothing reports: no
// turn route, no quota, and an activity with no start.
func statusOfAFreshState(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})

	got := sess.Status()
	if got.WorkingDir != state.WorkingDir {
		t.Errorf("WorkingDir = %q, want the state's %q", got.WorkingDir, state.WorkingDir)
	}
	if got.TurnRoute != nil {
		t.Errorf("TurnRoute = %+v, want nil between turns", got.TurnRoute)
	}
	if got.Quota != nil {
		t.Errorf("Quota = %+v, want nil before the provider reports one", got.Quota)
	}
	if want := string(state.InteractionState().Mode); got.Interaction.Mode != want {
		t.Errorf("Interaction.Mode = %q, want %q", got.Interaction.Mode, want)
	}
	if got.Activity.StartedAt != 0 {
		t.Errorf("Activity.StartedAt = %d, want 0 when the activity has no start", got.Activity.StartedAt)
	}
}

// statusMirrorsTheState sets every field before the arm opens, and compares the
// status with the value it should carry. Every field is planted before open
// because the pipe arm's first value is read when it attaches: a change after
// that arrives only with the forwarder's next tick.
func statusMirrorsTheState(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	home := t.TempDir()

	active := session.RouteInfo{
		Role:             routing.RoleImplementer,
		Profile:          "profile-a",
		Preset:           "preset-a",
		Provider:         "provider-a",
		Model:            "model-a",
		LocalOnly:        true,
		Thinking:         "high",
		ReasoningCapable: true,
		Active:           true,
	}
	turn := session.RouteInfo{
		Role:             routing.RoleReviewer,
		Profile:          "profile-b",
		Preset:           "preset-b",
		Provider:         "provider-b",
		Model:            "model-b",
		LocalOnly:        true,
		Thinking:         "low",
		ReasoningCapable: true,
		Active:           true,
	}
	state.SetActiveRoute(active)
	state.SetTurnRoute(turn)
	state.SetTurnUsage(1200)
	state.SetTurnContextWindow(8000)
	state.SetTurnQuota(&schema.QuotaInfo{
		PlanType:                "plus",
		PrimaryUsedPercent:      12,
		PrimaryResetAfterSecs:   14845,
		PrimaryWindowMinutes:    300,
		SecondaryUsedPercent:    34,
		SecondaryResetAfterSecs: 601645,
		SecondaryWindowMinutes:  10080,
	})
	state.SetEffortOverride(session.EffortOverride{Level: "high", Provider: "provider-a", Model: "model-a"})
	if _, err := state.ApplyInteraction(interaction.Patch{
		Mode: seamPtr(interaction.ModeAct),
		Auto: seamPtr(true),
	}); err != nil {
		t.Fatalf("ApplyInteraction: %v", err)
	}
	state.SetSudoUnlockEnabled(true)
	state.SetTrusted(true)
	state.SetActivity(session.Activity{
		Kind:      session.ActivityTool,
		Label:     "reading",
		StartedAt: time.Unix(100, 5_000_000),
	})
	state.SetWorkspace(session.Workspace{
		ProjectRoot:  "/project",
		ActiveRoot:   "/project/worktree",
		Branch:       "feature",
		BaseSha:      "abc123",
		TargetBranch: "main",
	})
	state.SetBrowserInfo(session.BrowserInfo{
		Active:      true,
		ToolName:    "browser_navigate",
		URL:         "https://example.com",
		Title:       "Example",
		Mode:        "read",
		SessionOpen: true,
		UpdatedAt:   time.Unix(200, 0),
	})
	state.SetSandboxInfo(session.SandboxInfo{Backend: "container", NetworkIsolation: true})
	state.BeginGeneration("g", 2, "d")
	// Plant the identity, backup and subagent state the TUI reads: a fresh
	// state has leaf 0 and no branches, so without this the expectations below
	// could not tell a mirrored value from a hard-coded zero.
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "hi", session.ContentTypeMarkdown)
	state.StoreBackup([]session.BackupFile{{Path: "notes.txt", Content: "old"}})
	child := seamState(t, "child")
	view := state.RegisterSubagent("scout", child)

	sess := open(t, service.Deps{
		State:   state,
		HomeDir: home,
		GitInfo: func(string) gitinfo.Info {
			return gitinfo.Info{Branch: "feature", Worktree: "wt", InRepo: true}
		},
	})

	got := sess.Status()
	want := acpwire.SessionStatus{
		WorkingDir:    state.WorkingDir,
		GenerationSeq: 2,
		LeafID:        state.LeafID(),
		Branches:      state.Branches(),
		ActiveRoute: acpwire.Route{
			Role:             string(routing.RoleImplementer),
			Profile:          "profile-a",
			Preset:           "preset-a",
			Provider:         "provider-a",
			Model:            "model-a",
			LocalOnly:        true,
			Thinking:         "high",
			ReasoningCapable: true,
			Active:           true,
		},
		TurnRoute: &acpwire.Route{
			Role:             string(routing.RoleReviewer),
			Profile:          "profile-b",
			Preset:           "preset-b",
			Provider:         "provider-b",
			Model:            "model-b",
			LocalOnly:        true,
			Thinking:         "low",
			ReasoningCapable: true,
			Active:           true,
		},
		TurnUsed:   1200,
		TurnWindow: 8000,
		Quota: &acpwire.Quota{
			PlanType:              "plus",
			PrimaryUsedPercent:    12,
			PrimaryResetAfterMs:   14845000,
			PrimaryWindowMs:       18000000,
			SecondaryUsedPercent:  34,
			SecondaryResetAfterMs: 601645000,
			SecondaryWindowMs:     604800000,
		},
		Effort: acpwire.Effort{Level: "high", Provider: "provider-a", Model: "model-a"},
		Interaction: acpwire.Interaction{
			Mode: string(interaction.ModeAct),
			Auto: true,
			// Enabling Auto grants System access in the same transaction
			// (interaction.Apply).
			System:   true,
			Revision: state.InteractionState().Revision,
		},
		SudoUnlock: true,
		Trusted:    true,
		HasBackup:  true,

		SubagentRunning: true,
		Activity: acpwire.Activity{
			Kind:      string(session.ActivityTool),
			Label:     "reading",
			StartedAt: 100005,
		},
		Workspace: acpwire.Workspace{
			ProjectRoot:  "/project",
			ActiveRoot:   "/project/worktree",
			Branch:       "feature",
			BaseSha:      "abc123",
			TargetBranch: "main",
		},
		Browser: acpwire.Browser{
			Active:      true,
			ToolName:    "browser_navigate",
			URL:         "https://example.com",
			Title:       "Example",
			Mode:        "read",
			SessionOpen: true,
			UpdatedAt:   200000,
		},
		// config.Default() leaves tools.shell.allow_network false, so only the
		// backend and the isolation are planted here.
		Sandbox: acpwire.Sandbox{Backend: "container", NetworkIsolation: true},
		// The child has no narration, so its view carries no headline.
		Subagents: []acpwire.Subagent{{
			ID:     view.ID,
			Label:  "scout",
			Status: acpwire.SubagentRunning,
			Drill: &acpwire.SubagentDrill{
				ActiveRoot: child.Workspace().ActiveRoot,
				// The child is fresh: no route, and an activity with no start.
				ActiveRoute: acpwire.Route{},
				Activity:    acpwire.Activity{Kind: string(session.ActivityIdle)},
				SessionID:   child.SessionID(),
			},
		}},
		Git: acpwire.Git{Branch: "feature", Worktree: "wt", InRepo: true},
		// The gate is the state's session-scope switch and its sticky
		// decisions; a fresh state has it on with none recorded.
		SkillGate: acpwire.SkillGateState{Enabled: state.SkillGateEnabled()},
		// The two files a config write changes come from the session's Deps and
		// its state's working directory, not from the state's setters.
		UserConfigPath:    config.UserConfigPath(home),
		ProjectConfigPath: config.ProjectConfigPath(state.WorkingDir),
	}
	// LeafID and Branches are the only expectations the state itself supplies,
	// so pin that the planted values are non-trivial: a Status() that
	// hard-coded zero would otherwise compare equal to them.
	if got.LeafID == 0 {
		t.Errorf("LeafID = 0, want the non-zero leaf planted by AddMessage")
	}
	if len(got.Branches) == 0 {
		t.Errorf("Branches = %v, want the branch tips planted by AddMessage", got.Branches)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status() =\n%+v\nwant\n%+v", got, want)
	}
}

// noticeCallsChangeTheBanner pins the four notice calls through the state they
// set: clearing is by category, and dismissing ignores the category. The calls
// go through the seam, so a read after one is current on both arms: the
// barrier makes sure the call's update is written before its response.
func noticeCallsChangeTheBanner(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})
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

// aNoticeCategoryThatMatchesNothingIsRefused pins that clearing by a category
// the wire does not carry is an error, not a silent success: the banner is
// kept, and the caller learns the call dismissed nothing.
func aNoticeCategoryThatMatchesNothingIsRefused(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	sess := open(t, service.Deps{State: state})
	ctx := context.Background()

	if err := sess.SetNotice(ctx, acpwire.Notice{
		Category: acpwire.NoticeProvider,
		Severity: acpwire.NoticeError,
		Message:  "boom",
	}); err != nil {
		t.Fatalf("SetNotice: %v", err)
	}
	if err := sess.ClearNotice(ctx, "Provider "); err == nil {
		t.Fatalf("ClearNotice(%q) succeeded, want an error: it matches no banner", "Provider ")
	}
	if got := sess.Status().Notice; got == nil {
		t.Fatalf("Status().Notice = nil after a refused clear, want the provider's banner kept")
	}
	if err := sess.ClearNotice(ctx, acpwire.NoticeProvider); err != nil {
		t.Fatalf("ClearNotice(provider): %v", err)
	}
	if got := sess.Status().Notice; got != nil {
		t.Fatalf("Status().Notice = %+v after clearing the provider's, want nil", got)
	}
}

// statusCarriesTheRuntimesNotice checks a notice the runtime set directly
// reaches the status with every field, its time in milliseconds. The notice is
// set before open, so the pipe arm's first value carries it.
func statusCarriesTheRuntimesNotice(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, "s1")
	state.SetNotice(session.Notice{
		Category: session.NoticeTool,
		Severity: session.SeverityWarn,
		Message:  "command timed out",
		Hint:     "try a narrower test",
		Source:   "shell.run",
		SetAt:    time.UnixMilli(1700000000000),
	})
	sess := open(t, service.Deps{State: state})

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

// noticeCalls returns the pair the budget alternates between, so each call
// changes the status and its update is sent.
func noticeCalls() [2]acpwire.Notice {
	return [2]acpwire.Notice{
		{Category: acpwire.NoticeTool, Severity: acpwire.NoticeWarn, Message: "first"},
		{Category: acpwire.NoticeTool, Severity: acpwire.NoticeWarn, Message: "second"},
	}
}

// TestNoticeCallBudget holds R10's 1ms budget for a notice call over the pipe on
// the 2,000-call session: the call includes its barrier and its update (P3 plan
// 3b).
func TestNoticeCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	st := largeStackSession(t)
	logLargeStackShape(t, st)
	sess := openPipeSeamTB(t, service.Deps{State: st})

	const runs = 30
	notices := noticeCalls()
	ctx := context.Background()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := sess.SetNotice(ctx, notices[i%len(notices)]); err != nil {
			t.Fatalf("SetNotice: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("notice call: median %v over %d runs (budget 1ms)", median, runs)
	if median >= time.Millisecond {
		t.Fatalf("notice call median = %v, budget 1ms (R10): do not loosen it", median)
	}
}

// BenchmarkNoticeCall measures a notice call over the pipe on the large
// session, its update included, as R10's budget is stated.
func BenchmarkNoticeCall(b *testing.B) {
	st := largeStackSession(b)
	logLargeStackShape(b, st)
	sess := openPipeSeamTB(b, service.Deps{State: st})

	notices := noticeCalls()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sess.SetNotice(ctx, notices[i%len(notices)]); err != nil {
			b.Fatalf("SetNotice: %v", err)
		}
	}
}
