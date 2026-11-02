package inproc

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/repo/gitinfo"
)

// newState builds a state the way the TUI tests do.
func newState(t *testing.T, id string) *session.State {
	t.Helper()
	return session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: id})
}

func TestSessionIDIsTheStateID(t *testing.T) {
	_, sess := New(Deps{State: newState(t, "s1")})
	if got := sess.ID(); got != "s1" {
		t.Fatalf("ID() = %q, want s1", got)
	}
}

func TestSwapClosesTheOldSession(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	fresh := h.Swap(newState(t, "s2"))

	if got := fresh.ID(); got != "s2" {
		t.Fatalf("new session ID() = %q, want s2", got)
	}
	if h.current != fresh {
		t.Fatalf("host current is not the session Swap returned")
	}
	if !old.closed.Load() {
		t.Errorf("old session is not closed after Swap")
	}
	if fresh.closed.Load() {
		t.Errorf("new session is closed after Swap")
	}
	if got := old.ID(); got != "s1" {
		t.Errorf("closed session ID() = %q, want its final value s1", got)
	}
}

func TestHostSharesTheFilesystem(t *testing.T) {
	h, _ := New(Deps{State: newState(t, "s1")})
	if !h.SharedFilesystem() {
		t.Fatalf("SharedFilesystem() = false, want true for the in-process adapter")
	}
}

// TestStatusOfAFreshState pins what a session that has done nothing reports:
// no turn route, no quota, and an activity with no start.
func TestStatusOfAFreshState(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

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

// TestStatusMirrorsTheState sets every field through its setter and compares
// the status with the value it should carry.
func TestStatusMirrorsTheState(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{
		State: state,
		GitInfo: func(string) gitinfo.Info {
			return gitinfo.Info{Branch: "feature", Worktree: "wt", InRepo: true}
		},
	})

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
		Mode: ptr(interaction.ModeAct),
		Auto: ptr(true),
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
	child := newState(t, "child")
	view := state.RegisterSubagent("scout", child)

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
				ActiveRoot:  child.Workspace().ActiveRoot,
				ActiveRoute: routeOf(child.ActiveRoute()),
				Activity:    activityWire(child.Activity()),
				SessionID:   child.SessionID(),
			},
		}},
		Git: acpwire.Git{Branch: "feature", Worktree: "wt", InRepo: true},
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

// TestStatusOfAClosedSessionStillReads pins C4: a closed session keeps
// answering with its own state's values.
func TestStatusOfAClosedSessionStillReads(t *testing.T) {
	oldState := newState(t, "s1")
	h, old := New(Deps{State: oldState})
	h.Swap(newState(t, "s2"))

	if got, want := old.Status().WorkingDir, oldState.WorkingDir; got != want {
		t.Errorf("closed session Status().WorkingDir = %q, want its own state's %q", got, want)
	}
}

// ptr is the address of v, for the interaction patch's optional fields.
func ptr[T any](v T) *T { return &v }

// TestAdapterDoesNotImportApp keeps the adapter out of the app package: TUI
// tests import this package, and app imports tui, so an import back into app
// would be a cycle.
func TestAdapterDoesNotImportApp(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, perr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Fatalf("unquote import in %s: %v", name, uerr)
			}
			if imp == "marshal/internal/app" || imp == "marshal/internal/app/tui" ||
				strings.HasPrefix(imp, "marshal/internal/app/tui/") {
				t.Errorf("%s imports %q: the adapter must not import the app or the TUI", name, imp)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no non-test Go files in the package directory")
	}
}
