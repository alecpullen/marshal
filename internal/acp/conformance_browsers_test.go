package acp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/plugins"
	"marshal/internal/skills"
	"marshal/internal/worktree"
)

// serveBrowsers registers the browser handlers on the pipe arm's server over
// the served service. It adds nothing to the barrier: the manager runs no
// forwarders (P3 plan 7b).
func serveBrowsers(tb testing.TB, srv *Server, served *service.Session, sessionID string) {
	tb.Helper()
	browsers := NewBrowserManager(BrowserManagerConfig{
		Lookup: func(id string) (*service.Session, bool) {
			return served, id == sessionID
		},
	})
	// A nil others map is what the pipe arm wants: a call from a client
	// without the capability answers method-not-found. This arm's client
	// always declares it, so every call reaches the browser manager.
	registerBrowsers(srv, browsers, nil)
}

// TestBrowserConformance runs the notes, command, browser and project cases
// against both arms of the seam: the service, and the client's mirror of it
// over a pipe (V1).
func TestBrowserConformance(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, open func(t *testing.T, d service.Deps) seam)
	}{
		{name: "a note is a system message", run: aNoteIsASystemMessage},
		{name: "a note records the user's text", run: aNoteRecordsTheUsersText},
		{name: "a note refuses other roles", run: aNoteRefusesOtherRoles},
		{name: "a note is in the stack at once", run: aNoteIsInTheStackAtOnce},

		{name: "the command list is the registry", run: theCommandListIsTheRegistry},
		{name: "a command runs its handler", run: aCommandRunsItsHandler},
		{name: "a command mints action IDs", run: aCommandMintsActionIDs},
		{name: "an action's result is minted too", run: anActionsResultIsMintedToo},
		{name: "a command edge case", run: aCommandEdgeCase},

		{name: "the prompt history round-trips", run: thePromptHistoryRoundTrips},
		{name: "the prompt history without a database", run: thePromptHistoryWithoutADatabase},
		{name: "messages select by role finality and last", run: messagesSelectByRoleFinalityAndLast},

		{name: "the file index reads the deps", run: theFileIndexReadsTheDeps},
		{name: "plans lists the discovered plans", run: plansListTheDiscoveredPlans},
		{name: "discard refuses outside the plans directory", run: discardRefusesOutsideThePlansDirectory},
		{name: "scaffold reports an existing file", run: scaffoldReportsAnExistingFile},
		{name: "a diff reads without mutating", run: aDiffReadsWithoutMutating},
		{name: "a file read stays in the working directory", run: aFileReadStaysInTheWorkingDirectory},
		{name: "a file read truncates and sniffs only the head", run: aFileReadTruncatesAndSniffsOnlyTheHead},

		{name: "memories list the project's own rows", run: memoriesListTheProjectsOwnRows},
		{name: "memories without a database are coded", run: memoriesWithoutADatabaseAreCoded},
		{name: "the intelligence checks answer four", run: theIntelligenceChecksAnswerFour},
		{name: "a plugin's project scope needs trust", run: aPluginsProjectScopeNeedsTrust},
		{name: "a scanned plugin installs and lists", run: aScannedPluginInstallsAndLists},
		{name: "a discarded scan installs nothing", run: aDiscardedScanInstallsNothing},
		{name: "the skills list both stores with reasons", run: theSkillsListBothStoresWithReasons},
		{name: "a load checks the store against the listing", run: aLoadChecksTheStoreAgainstTheListing},
		{name: "a removal uses the listed source", run: aRemovalUsesTheListedSource},
		{name: "the install refusals", run: theInstallRefusals},
		{name: "autoload writes the user config", run: autoloadWritesTheUserConfig},
		{name: "the skill gate is in the status", run: theSkillGateIsInTheStatus},
	}
	for _, arm := range seamArms() {
		for _, tc := range cases {
			t.Run(arm.name+"/"+tc.name, func(t *testing.T) {
				tc.run(t, arm.open)
			})
		}
	}
}

// The fixtures the cases share. Each builds what its session needs before it
// opens an arm.

// browserConformanceDB opens a migrated database with one project.
func browserConformanceDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

// browserWorkState builds a state over work under the harness's session ID.
func browserWorkState(t *testing.T, work string) *session.State {
	t.Helper()
	return session.New(config.Default(), work, time.Unix(100, 0),
		session.Persistence{SessionID: stackWireSession})
}

// browserWritePlan writes a plan file into dir's plans directory.
func browserWritePlan(t *testing.T, dir, name, body string) string {
	t.Helper()
	plansDir := filepath.Join(dir, ".marshal", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatalf("mkdir plans: %v", err)
	}
	path := filepath.Join(plansDir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// browserSkillStores creates the two skill stores a session reads.
func browserSkillStores(t *testing.T, home, work string) (global, project string) {
	t.Helper()
	global = filepath.Join(config.UserDir(home), "skills")
	project = filepath.Join(work, ".marshal", "skills")
	for _, dir := range []string{global, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create skill store %s: %v", dir, err)
		}
	}
	return global, project
}

// browserWriteSkill writes a top-level skill file into dir.
func browserWriteSkill(t *testing.T, dir, file, name, description string) string {
	t.Helper()
	body := "+++\nname = \"" + name + "\"\ndescription = \"" + description + "\"\n+++\n\n# " + name + "\n"
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", path, err)
	}
	return path
}

// browserParseSkill reads and parses a skill file the way discovery does.
func browserParseSkill(t *testing.T, path string) skills.Skill {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read skill %s: %v", path, err)
	}
	sk, err := skills.Parse(string(raw))
	if err != nil {
		t.Fatalf("parse skill %s: %v", path, err)
	}
	return sk
}

// browserSkillEntry returns the listed entry for one store and source.
func browserSkillEntry(t *testing.T, entries []acpwire.SkillEntry, scope, source string) acpwire.SkillEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Scope == scope && entry.Source == source {
			return entry
		}
	}
	t.Fatalf("no %s/%s entry in %+v", scope, source, entries)
	return acpwire.SkillEntry{}
}

// browserPluginRepo returns a local plugin repository with one command,
// committed.
func browserPluginRepo(t *testing.T) (src, commit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	src = filepath.Join(t.TempDir(), "hello-plugin")
	run := func(args ...string) {
		t.Helper()
		cmdArgs := append([]string{"-C", src}, args...)
		if out, err := exec.Command("git", cmdArgs...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "commands"), 0o755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	const command = "+++\nname = \"hello\"\ndescription = \"says hello\"\n+++\necho hello\n"
	if err := os.WriteFile(filepath.Join(src, "commands", "hello.md"), []byte(command), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", "-A")
	run("commit", "-m", "initial")
	out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return src, strings.TrimSpace(string(out))
}

// stackHoldsText reports whether a message node of snap carries text.
func stackHoldsText(snap acpwire.StackSnapshot, text string) bool {
	for _, node := range snap.Nodes {
		if node.Message != nil && node.Message.Content == text {
			return true
		}
	}
	return false
}

// Notes.

func aNoteIsASystemMessage(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	sess := open(t, service.Deps{State: state})

	if err := sess.AddNote(context.Background(), acpwire.Note{Text: "heads up"}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if got := msgs[0]; got.Role != session.RoleSystem || got.ContentType != session.ContentTypePlain || got.Content != "heads up" {
		t.Errorf("note = %+v, want the agent's plain system message", got)
	}
}

func aNoteRecordsTheUsersText(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	sess := open(t, service.Deps{State: state})

	if err := sess.AddNote(context.Background(), acpwire.Note{Text: "mine", Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != session.RoleUser || msgs[0].Content != "mine" {
		t.Errorf("note = %+v, want the user's text", msgs[0])
	}
}

func aNoteRefusesOtherRoles(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	sess := open(t, service.Deps{State: state})

	err := sess.AddNote(context.Background(), acpwire.Note{Text: "hi", Role: acpwire.RoleAssistant})
	if err == nil || !strings.Contains(err.Error(), "a note's role must be system or user") {
		t.Fatalf("AddNote(assistant) = %v, want the refusal", err)
	}
	if got := len(state.Messages()); got != 0 {
		t.Errorf("messages = %d, want 0: a refused note appends nothing", got)
	}
}

// aNoteIsInTheStackAtOnce is V3 for the note: the read after AddNote returns
// holds it, with no wait.
func aNoteIsInTheStackAtOnce(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	sess := open(t, service.Deps{State: state})

	// The first read starts the idle stack loop, so the note's patch has a
	// loop to reach.
	_ = sess.Stack(0, false)
	const text = "the note is here at once"
	if err := sess.AddNote(context.Background(), acpwire.Note{Text: text}); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if !stackHoldsText(sess.Stack(0, false), text) {
		t.Fatalf("the read right after AddNote does not hold %q", text)
	}
}

// Commands.

// browserCommandRegistry is the registry the command cases serve.
func browserCommandRegistry(t *testing.T) *commands.Registry {
	t.Helper()
	reg := commands.New()
	handler := func(*session.State, []string) commands.Result { return commands.Text("ran") }
	for _, entry := range []commands.Command{
		{Name: "zeta", Description: "Zeta prompt", Group: "Chat", PromptBody: "the body"},
		{Name: "alpha", Description: "Alpha command", Args: "<x>", Group: "Chat", Handler: handler},
		{Name: "trust", Description: "Trust folder", Group: "Settings", TUIOnly: true, Handler: handler},
		{Name: "beta", Description: "Beta command", Hidden: true, Handler: handler},
	} {
		if err := reg.Register(entry); err != nil {
			t.Fatalf("register %q: %v", entry.Name, err)
		}
	}
	return reg
}

func theCommandListIsTheRegistry(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	sess := open(t, service.Deps{State: seamState(t, stackWireSession), Commands: browserCommandRegistry(t)})

	got := sess.Commands()
	want := []acpwire.CommandInfo{
		{Name: "alpha", Description: "Alpha command", Args: "<x>", Group: "Chat", Kind: acpwire.CommandKindHeadless, HasHandler: true},
		{Name: "beta", Description: "Beta command", Kind: acpwire.CommandKindHeadless, Hidden: true, HasHandler: true},
		{Name: "trust", Description: "Trust folder", Group: "Settings", Kind: acpwire.CommandKindTUIOnly, TUIOnly: true, HasHandler: true},
		{Name: "zeta", Description: "Zeta prompt", Group: "Chat", Kind: acpwire.CommandKindPrompt, PromptBody: "the body"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Commands() =\n%+v\nwant\n%+v", got, want)
	}

	bare := open(t, service.Deps{State: seamState(t, stackWireSession)})
	if got := bare.Commands(); got != nil {
		t.Errorf("Commands() without a registry = %+v, want nil", got)
	}
}

func aCommandRunsItsHandler(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	var gotState *session.State
	var gotArgs []string
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "alpha",
		Handler: func(st *session.State, args []string) commands.Result {
			gotState = st
			gotArgs = args
			return commands.Text("done")
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := open(t, service.Deps{State: state, Commands: reg})

	res, err := sess.Command(context.Background(), "alpha", []string{"x"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Text != "done" {
		t.Errorf("Text = %q, want %q", res.Text, "done")
	}
	if gotState != state {
		t.Errorf("handler state = %p, want the session's %p", gotState, state)
	}
	if !reflect.DeepEqual(gotArgs, []string{"x"}) {
		t.Errorf("handler args = %v, want [x]", gotArgs)
	}
}

func aCommandMintsActionIDs(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	var innerState *session.State
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("panel", false, []commands.Row{
				{
					Text:        "outer",
					ActionLabel: "run",
					Action:      func(*session.State) commands.Result { return commands.Text("outer ran") },
				},
				{
					Text: "drill",
					Children: []commands.Row{{
						Text:        "inner",
						ActionLabel: "run",
						Action: func(st *session.State) commands.Result {
							innerState = st
							return commands.Text("inner ran")
						},
					}},
				},
			})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := open(t, service.Deps{State: state, Commands: reg})
	ctx := context.Background()

	res, err := sess.Command(ctx, "panel", nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Doc == nil || len(res.Doc.Rows) != 2 {
		t.Fatalf("Doc = %+v, want two rows", res.Doc)
	}
	if got := res.Doc.Rows[0].ActionID; got != "a1" {
		t.Errorf("outer row ActionID = %q, want a1", got)
	}
	children := res.Doc.Rows[1].Children
	if len(children) != 1 {
		t.Fatalf("drill row has %d children, want 1", len(children))
	}
	if got := children[0].ActionID; got != "a2" {
		t.Errorf("inner row ActionID = %q, want a2", got)
	}

	out, err := sess.CommandAction(ctx, "a2")
	if err != nil {
		t.Fatalf("CommandAction(a2): %v", err)
	}
	if out.Text != "inner ran" {
		t.Errorf("CommandAction(a2).Text = %q, want %q", out.Text, "inner ran")
	}
	if innerState != state {
		t.Errorf("action state = %p, want the session's %p", innerState, state)
	}
}

func anActionsResultIsMintedToo(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	var deepState *session.State
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "panel",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("first", false, []commands.Row{{
				Text:        "open",
				ActionLabel: "open",
				Action: func(*session.State) commands.Result {
					return commands.Panel("second", false, []commands.Row{{
						Text:        "deeper",
						ActionLabel: "run",
						Action: func(st *session.State) commands.Result {
							deepState = st
							return commands.Text("deep ran")
						},
					}})
				},
			}})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := open(t, service.Deps{State: state, Commands: reg})
	ctx := context.Background()

	res, err := sess.Command(ctx, "panel", nil)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if res.Doc == nil || len(res.Doc.Rows) != 1 {
		t.Fatalf("Doc = %+v, want one row", res.Doc)
	}
	outer := res.Doc.Rows[0].ActionID
	if outer != "a1" {
		t.Fatalf("outer ActionID = %q, want a1", outer)
	}

	second, err := sess.CommandAction(ctx, outer)
	if err != nil {
		t.Fatalf("CommandAction(%s): %v", outer, err)
	}
	if second.Doc == nil || len(second.Doc.Rows) != 1 {
		t.Fatalf("second Doc = %+v, want one row", second.Doc)
	}
	inner := second.Doc.Rows[0].ActionID
	if inner == "" {
		t.Fatal("the action's own result minted no ID")
	}
	if inner == outer {
		t.Errorf("inner ActionID = %q, want a new ID", inner)
	}

	out, err := sess.CommandAction(ctx, inner)
	if err != nil {
		t.Fatalf("CommandAction(%s): %v", inner, err)
	}
	if out.Text != "deep ran" {
		t.Errorf("Text = %q, want %q", out.Text, "deep ran")
	}
	if deepState != state {
		t.Errorf("action state = %p, want the session's %p", deepState, state)
	}
}

func aCommandEdgeCase(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	reg := commands.New()
	if err := reg.Register(commands.Command{Name: "trust", TUIOnly: true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := open(t, service.Deps{State: seamState(t, stackWireSession), Commands: reg})
	ctx := context.Background()

	res, err := sess.Command(ctx, "trust", nil)
	if err != nil {
		t.Fatalf("Command(trust) = %v, want no error", err)
	}
	if !reflect.DeepEqual(res, acpwire.CommandResult{}) {
		t.Errorf("Command(trust) = %+v, want an empty result", res)
	}

	if _, err := sess.Command(ctx, "nope", nil); err == nil || !strings.Contains(err.Error(), "/nope") {
		t.Errorf("Command(nope) = %v, want an error naming /nope", err)
	}

	bare := open(t, service.Deps{State: seamState(t, stackWireSession)})
	if _, err := bare.Command(ctx, "trust", nil); err == nil || !strings.Contains(err.Error(), "no command registry") {
		t.Errorf("Command on a session without a registry = %v, want the no-registry refusal", err)
	}
}

// History.

func thePromptHistoryRoundTrips(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	database, projectID := browserConformanceDB(t)
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{DB: database, SessionID: stackWireSession})
	sess := open(t, service.Deps{State: state, ProjectID: projectID})
	ctx := context.Background()

	for _, text := range []string{"first", "second"} {
		if err := sess.SavePrompt(ctx, text); err != nil {
			t.Fatalf("SavePrompt(%q): %v", text, err)
		}
	}

	got, err := sess.RecentPrompts(ctx, 1)
	if err != nil {
		t.Fatalf("RecentPrompts(1): %v", err)
	}
	if want := []string{"second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RecentPrompts(1) = %v, want %v", got, want)
	}

	got, err = sess.RecentPrompts(ctx, 10)
	if err != nil {
		t.Fatalf("RecentPrompts(10): %v", err)
	}
	if want := []string{"second", "first"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RecentPrompts(10) = %v, want %v", got, want)
	}
}

func thePromptHistoryWithoutADatabase(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	database, _ := browserConformanceDB(t)
	cases := []struct {
		name string
		deps service.Deps
	}{
		{
			name: "no database",
			deps: service.Deps{State: seamState(t, stackWireSession), ProjectID: 7},
		},
		{
			name: "no project",
			deps: service.Deps{State: session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
				session.Persistence{DB: database, SessionID: stackWireSession})},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := open(t, tc.deps)
			got, err := sess.RecentPrompts(context.Background(), 10)
			if err != nil {
				t.Fatalf("RecentPrompts: %v", err)
			}
			if got != nil {
				t.Errorf("RecentPrompts = %v, want nil", got)
			}
			if err := sess.SavePrompt(context.Background(), "not saved"); err != nil {
				t.Errorf("SavePrompt = %v, want nil", err)
			}
		})
	}
}

func messagesSelectByRoleFinalityAndLast(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	state := seamState(t, stackWireSession)
	state.AddMessage(session.RoleUser, "one", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "draft", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "answer", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "two", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "   ", session.ContentTypePlain)
	sess := open(t, service.Deps{State: state})
	ctx := context.Background()

	t.Run("role", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleUser})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{
			{Role: acpwire.RoleUser, Content: "one"},
			{Role: acpwire.RoleUser, Content: "two"},
		}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 2 {
			t.Errorf("Total = %d, want 2", got.Total)
		}
	})

	t.Run("role and last", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleAssistant, Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{{Role: acpwire.RoleAssistant, Content: "   ", Final: true}}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 3 {
			t.Errorf("Total = %d, want 3", got.Total)
		}
	})

	t.Run("final", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleAssistant, Final: true, Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{{Role: acpwire.RoleAssistant, Content: "answer", Final: true}}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 1 {
			t.Errorf("Total = %d, want 1", got.Total)
		}
	})

	t.Run("last", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 1 {
			t.Errorf("Messages = %+v, want one message", got.Messages)
		}
		if got.Total != 5 {
			t.Errorf("Total = %d, want every message in the state", got.Total)
		}
	})

	t.Run("last beyond the total", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Last: 10})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 5 {
			t.Errorf("Messages = %+v, want every message, uncut", got.Messages)
		}
		if got.Total != 5 {
			t.Errorf("Total = %d, want 5", got.Total)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: "system"})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 0 {
			t.Errorf("Messages = %+v, want none", got.Messages)
		}
		if got.Total != 0 {
			t.Errorf("Total = %d, want 0", got.Total)
		}
	})
}

// Project.

func theFileIndexReadsTheDeps(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	ctx := context.Background()

	sess := open(t, service.Deps{State: seamState(t, stackWireSession)})
	if got, err := sess.FileIndex(ctx); err != nil || got != nil {
		t.Errorf("FileIndex() without Deps.FileIndex = %v, %v; want nil, nil", got, err)
	}

	want := []string{"a.go", "b.go"}
	sess = open(t, service.Deps{
		State:     seamState(t, stackWireSession),
		FileIndex: func() ([]string, error) { return want, nil },
	})
	got, err := sess.FileIndex(ctx)
	if err != nil {
		t.Fatalf("FileIndex(): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FileIndex() = %v, want %v", got, want)
	}

	sess = open(t, service.Deps{
		State:     seamState(t, stackWireSession),
		FileIndex: func() ([]string, error) { return nil, errors.New("boom") },
	})
	if _, err := sess.FileIndex(ctx); err == nil {
		t.Error("FileIndex() with a failing dep = nil, want the dep's error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("FileIndex() error = %v, want it to carry the dep's sentence", err)
	}
}

func plansListTheDiscoveredPlans(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	dir := t.TempDir()
	browserWritePlan(t, dir, "good.md",
		"# Test Plan\n\n## Global Constraints\n\n- keep it green\n\n## Task 1: Do it\n\nBody.\n")
	browserWritePlan(t, dir, "broken.md", "# No tasks here\n")
	sess := open(t, service.Deps{State: browserWorkState(t, dir)})

	plans, err := sess.Plans(context.Background())
	if err != nil {
		t.Fatalf("Plans(): %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("Plans() returned %d plans, want 2: %+v", len(plans), plans)
	}
	byName := map[string]acpwire.PlanCandidate{}
	for _, p := range plans {
		byName[p.Name] = p
	}
	good, ok := byName["good.md"]
	if !ok {
		t.Fatalf("Plans() = %+v, want a good.md entry", plans)
	}
	if good.Tasks != 1 || good.Error != "" {
		t.Errorf("good.md = %+v, want one task and no error", good)
	}
	broken, ok := byName["broken.md"]
	if !ok {
		t.Fatalf("Plans() = %+v, want a broken.md entry", plans)
	}
	if broken.Error == "" {
		t.Error("broken.md Error is empty, want the parse error")
	}
}

func discardRefusesOutsideThePlansDirectory(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	dir := t.TempDir()
	inside := browserWritePlan(t, dir, "candidate.md", "# Plan\n\n## Task 1: Do it\n")
	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("# Plan\n\n## Task 1: Do it\n"), 0o644); err != nil {
		t.Fatalf("write outside plan: %v", err)
	}
	sess := open(t, service.Deps{State: browserWorkState(t, dir)})
	ctx := context.Background()

	if err := sess.DiscardCandidate(ctx, inside); err != nil {
		t.Fatalf("DiscardCandidate(inside): %v", err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Errorf("inside candidate still present after discard: %v", err)
	}

	if err := sess.DiscardCandidate(ctx, outside); err == nil {
		t.Error("DiscardCandidate(outside) = nil, want a refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside file is gone after a refused discard: %v", err)
	}
}

func scaffoldReportsAnExistingFile(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	dir := t.TempDir()
	sess := open(t, service.Deps{State: browserWorkState(t, dir)})
	ctx := context.Background()

	first, err := sess.ScaffoldPlan(ctx)
	if err != nil {
		t.Fatalf("ScaffoldPlan() first: %v", err)
	}
	if first.Existed {
		t.Error("ScaffoldPlan() first Existed = true, want false")
	}
	written, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read scaffolded plan: %v", err)
	}

	second, err := sess.ScaffoldPlan(ctx)
	if err != nil {
		t.Fatalf("ScaffoldPlan() second: %v", err)
	}
	if !second.Existed {
		t.Error("ScaffoldPlan() second Existed = false, want true")
	}
	kept, err := os.ReadFile(second.Path)
	if err != nil {
		t.Fatalf("read plan after second call: %v", err)
	}
	if string(kept) != string(written) {
		t.Error("ScaffoldPlan() rewrote an existing plan")
	}
}

func aDiffReadsWithoutMutating(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	git := worktree.NewFakeGitOps()
	git.DiffOut = "diff body"
	sess := open(t, service.Deps{State: seamState(t, stackWireSession), Git: git})
	ctx := context.Background()

	got, err := sess.Diff(ctx, acpwire.DiffQuery{Base: "main", Head: "pipeline/add-retries"})
	if err != nil {
		t.Fatalf("Diff(): %v", err)
	}
	if got != "diff body" {
		t.Errorf("Diff() = %q, want %q", got, "diff body")
	}
	for _, call := range git.Calls() {
		switch call {
		case "WorktreeAdd", "WorktreeRemove", "CommitAll", "WorktreePrune":
			t.Errorf("Diff performed a mutating git operation: %s", call)
		}
	}

	if _, err := sess.Diff(ctx, acpwire.DiffQuery{Base: "main"}); err == nil ||
		!strings.Contains(err.Error(), "a diff needs a base and a head") {
		t.Errorf("Diff() without a head error = %v, want the pinned refusal", err)
	}
}

func aFileReadStaysInTheWorkingDirectory(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write blob.bin: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	sess := open(t, service.Deps{State: browserWorkState(t, dir)})
	ctx := context.Background()

	got, err := sess.ReadFile(ctx, "a.txt")
	if err != nil {
		t.Fatalf("ReadFile(a.txt): %v", err)
	}
	if got.Content != "hello" || got.Size != 5 || got.Binary {
		t.Errorf("ReadFile(a.txt) = %+v, want content hello, size 5, text", got)
	}

	if _, err := sess.ReadFile(ctx, "../x"); err == nil {
		t.Error("ReadFile(../x) = nil error, want a refusal")
	}
	if _, err := sess.ReadFile(ctx, filepath.Join(dir, "a.txt")); err == nil {
		t.Error("ReadFile(absolute) = nil error, want a refusal")
	}
	if _, err := sess.ReadFile(ctx, "sub"); err == nil ||
		!strings.Contains(err.Error(), "is not a regular file") {
		t.Errorf("ReadFile(sub) error = %v, want the pinned refusal", err)
	}

	bin, err := sess.ReadFile(ctx, "blob.bin")
	if err != nil {
		t.Fatalf("ReadFile(blob.bin): %v", err)
	}
	if !bin.Binary || bin.Content != "" {
		t.Errorf("ReadFile(blob.bin) = %+v, want binary with no content", bin)
	}
}

// browserMaxReadFile is the service's read cap, copied so the case can build
// the file that crosses it.
const browserMaxReadFile = 1 << 20

func aFileReadTruncatesAndSniffsOnlyTheHead(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	dir := t.TempDir()
	big := strings.Repeat("a", browserMaxReadFile+1)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}
	// A NUL past the sniff window leaves the file text.
	late := append([]byte(strings.Repeat("a", 8192)), 0x00)
	if err := os.WriteFile(filepath.Join(dir, "late.bin"), late, 0o644); err != nil {
		t.Fatalf("write late.bin: %v", err)
	}
	sess := open(t, service.Deps{State: browserWorkState(t, dir)})
	ctx := context.Background()

	got, err := sess.ReadFile(ctx, "big.txt")
	if err != nil {
		t.Fatalf("ReadFile(big.txt): %v", err)
	}
	if !got.Truncated {
		t.Error("ReadFile(big.txt) Truncated = false, want true")
	}
	if len(got.Content) != browserMaxReadFile {
		t.Errorf("ReadFile(big.txt) len(Content) = %d, want %d", len(got.Content), browserMaxReadFile)
	}
	if got.Size != int64(len(big)) {
		t.Errorf("ReadFile(big.txt) Size = %d, want %d (the whole file)", got.Size, len(big))
	}
	if got.Binary {
		t.Error("ReadFile(big.txt) Binary = true, want false")
	}

	lateGot, err := sess.ReadFile(ctx, "late.bin")
	if err != nil {
		t.Fatalf("ReadFile(late.bin): %v", err)
	}
	if lateGot.Binary {
		t.Error("ReadFile(late.bin) Binary = true, want false: the sniff stops at 8192 bytes")
	}
	if len(lateGot.Content) != len(late) {
		t.Errorf("ReadFile(late.bin) len(Content) = %d, want %d", len(lateGot.Content), len(late))
	}
	if lateGot.Truncated {
		t.Error("ReadFile(late.bin) Truncated = true, want false for a small file")
	}
}

// Browsers.

func memoriesListTheProjectsOwnRows(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	database, projectID := browserConformanceDB(t)
	otherID, err := database.GetOrCreateProject("/other", "other")
	if err != nil {
		t.Fatalf("GetOrCreateProject other: %v", err)
	}
	if err := database.CreateSession(stackWireSession, projectID, "", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := database.CreateSession("s9", otherID, "", time.Unix(1000, 0)); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	created := time.Unix(4000, 0).UTC()
	if err := database.SaveMemory(projectID, "fact", "the repo uses sqlite.", stackWireSession, created); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if err := database.SaveMemory(otherID, "fact", "another project's memory.", "s9", created); err != nil {
		t.Fatalf("SaveMemory other: %v", err)
	}
	state := session.New(config.Default(), "/repo", time.Unix(500, 0),
		session.Persistence{DB: database, SessionID: stackWireSession})
	sess := open(t, service.Deps{State: state, ProjectID: projectID})
	ctx := context.Background()

	got, err := sess.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Memories = %+v, want the session project's one row", got)
	}
	entry := got[0]
	if entry.Kind != "fact" || entry.Content != "the repo uses sqlite." {
		t.Errorf("entry = %+v, want the saved fact", entry)
	}
	if entry.Confidence != "tentative" {
		t.Errorf("Confidence = %q, want tentative", entry.Confidence)
	}
	if want := acpwire.Millis(created); entry.CreatedAt != want {
		t.Errorf("CreatedAt = %d, want %d", entry.CreatedAt, want)
	}

	if err := sess.DeleteMemory(ctx, entry.ID); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	after, err := sess.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories after delete: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("Memories after delete = %+v, want none", after)
	}
}

func memoriesWithoutADatabaseAreCoded(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	sess := open(t, service.Deps{State: seamState(t, stackWireSession)})
	ctx := context.Background()

	if _, err := sess.Memories(ctx); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("Memories error = %v, want CodeNoDatabase", err)
	}
	if err := sess.DeleteMemory(ctx, 1); !acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		t.Errorf("DeleteMemory error = %v, want CodeNoDatabase", err)
	}
}

func theIntelligenceChecksAnswerFour(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	t.Setenv("PATH", t.TempDir()) // no language servers on PATH, deterministically
	database, projectID := browserConformanceDB(t)
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0),
		session.Persistence{DB: database, SessionID: stackWireSession})
	sess := open(t, service.Deps{State: state, ProjectID: projectID})

	checks, err := sess.Intelligence(context.Background())
	if err != nil {
		t.Fatalf("Intelligence: %v", err)
	}
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		names = append(names, check.Name)
	}
	want := []string{"Index", "Embeddings", "LSP", "Watcher"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("checks = %v, want %v", names, want)
	}
}

func aPluginsProjectScopeNeedsTrust(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	lf := &plugins.Lockfile{Plugins: []plugins.LockEntry{{Name: "project-plugin", Source: "github:c/d"}}}
	if err := lf.Write(plugins.ProjectLockPath(work)); err != nil {
		t.Fatalf("write the project lockfile: %v", err)
	}
	sess := open(t, service.Deps{State: browserWorkState(t, work), HomeDir: home})
	ctx := context.Background()
	const want = "project scope requires a trusted session"

	if err := sess.InstallPlugin(ctx, "scan-1", "project"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("InstallPlugin project scope error = %v, want %q", err, want)
	}
	if err := sess.RemovePlugin(ctx, "project-plugin", "project"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("RemovePlugin project scope error = %v, want %q", err, want)
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Plugins = %+v, want no project entry on an untrusted session", entries)
	}
}

func aScannedPluginInstallsAndLists(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home := t.TempDir()
	sess := open(t, service.Deps{State: browserWorkState(t, t.TempDir()), HomeDir: home})
	src, commit := browserPluginRepo(t)
	ctx := context.Background()

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if scan.Token == "" || scan.Commit != commit {
		t.Fatalf("scan = %+v, want a token and the repository's %q", scan, commit)
	}
	if len(scan.Contents.Commands) != 1 || scan.Contents.Commands[0].Name != "hello" {
		t.Errorf("commands = %+v, want the scanned one", scan.Contents.Commands)
	}

	if err := sess.InstallPlugin(ctx, scan.Token, "global"); err != nil {
		t.Fatalf("InstallPlugin: %v", err)
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Plugins = %+v, want the one installed plugin", entries)
	}
	if entry := entries[0]; entry.Name != scan.Name || entry.Scope != "global" || entry.Commit != commit {
		t.Errorf("Plugins[0] = %+v, want %q in global scope at %s", entry, scan.Name, commit)
	}

	if err := sess.RemovePlugin(ctx, entries[0].Name, "global"); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}
	after, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins after remove: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("Plugins after remove = %+v, want none", after)
	}
}

// aDiscardedScanInstallsNothing keeps the discard's effect on the seam: a token
// it deleted cannot install, and nothing lands on either arm.
func aDiscardedScanInstallsNothing(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home := t.TempDir()
	src, _ := browserPluginRepo(t)
	sess := open(t, service.Deps{State: browserWorkState(t, t.TempDir()), HomeDir: home})
	ctx := context.Background()

	scan, err := sess.ScanPlugin(ctx, src)
	if err != nil {
		t.Fatalf("ScanPlugin: %v", err)
	}
	if scan.Token == "" {
		t.Fatalf("scan = %+v, want a token to discard", scan)
	}
	if err := sess.DiscardPluginScan(ctx, scan.Token); err != nil {
		t.Fatalf("DiscardPluginScan(%q): %v", scan.Token, err)
	}
	if err := sess.InstallPlugin(ctx, scan.Token, "global"); err == nil {
		t.Error("InstallPlugin on a discarded scan = nil, want the service's refusal")
	}
	entries, err := sess.Plugins(ctx)
	if err != nil {
		t.Fatalf("Plugins: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Plugins after a discarded install = %+v, want none", entries)
	}
}

func theSkillsListBothStoresWithReasons(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	global, project := browserSkillStores(t, home, work)
	browserWriteSkill(t, global, "alpha.md", "alpha", "global fixture")
	browserWriteSkill(t, project, "beta.md", "beta", "project fixture")

	idx := skills.NewIndex()
	idx.Set("alpha", browserParseSkill(t, filepath.Join(global, "alpha.md")))
	sess := open(t, service.Deps{
		State:      browserWorkState(t, work),
		HomeDir:    home,
		SkillIndex: func() *skills.Index { return idx },
	})

	entries, err := sess.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills = %+v, want the global and the project entry", entries)
	}
	if blocked := browserSkillEntry(t, entries, "global", "alpha.md").LoadBlocked; blocked != "" {
		t.Errorf("global alpha LoadBlocked = %q, want \"\" (the index holds it)", blocked)
	}
	wantBeta := "the current session has no beta — restart Marshal to pick it up"
	if blocked := browserSkillEntry(t, entries, "project", "beta.md").LoadBlocked; blocked != wantBeta {
		t.Errorf("project beta LoadBlocked = %q, want %q", blocked, wantBeta)
	}

	// A session with no index at all blocks every entry, whatever it holds.
	bare := open(t, service.Deps{State: browserWorkState(t, work), HomeDir: home})
	entries, err = bare.Skills(context.Background())
	if err != nil {
		t.Fatalf("Skills without an index: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills without an index = %+v, want both entries", entries)
	}
	wantNoIndex := "this session has no skill index — restart Marshal to build one"
	for _, e := range entries {
		if e.LoadBlocked != wantNoIndex {
			t.Errorf("%s/%s LoadBlocked = %q, want %q", e.Scope, e.Source, e.LoadBlocked, wantNoIndex)
		}
	}
}

func aLoadChecksTheStoreAgainstTheListing(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	global, _ := browserSkillStores(t, home, work)
	path := browserWriteSkill(t, global, "alpha.md", "alpha", "original body")

	idx := skills.NewIndex()
	idx.Set("alpha", browserParseSkill(t, path))
	state := browserWorkState(t, work)
	sess := open(t, service.Deps{
		State:      state,
		HomeDir:    home,
		SkillIndex: func() *skills.Index { return idx },
	})
	ctx := context.Background()

	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills: %v", err)
	}
	// Rewrite the file behind the listing's back.
	rewritten := "+++\nname = \"alpha\"\ndescription = \"rewritten body\"\n+++\n\n# alpha\n"
	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		t.Fatalf("rewrite skill: %v", err)
	}
	wantChanged := "alpha.md changed on disk — reopen the panel to pick up the new content"
	if _, err := sess.LoadSkill(ctx, "global", "alpha.md"); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("LoadSkill after a rewrite = %v, want %q", err, wantChanged)
	}

	// A fresh listing sees the new content, and the index is brought up to it.
	idx.Set("alpha", browserParseSkill(t, path))
	if _, err := sess.Skills(ctx); err != nil {
		t.Fatalf("Skills after the rewrite: %v", err)
	}
	name, err := sess.LoadSkill(ctx, "global", "alpha.md")
	if err != nil {
		t.Fatalf("LoadSkill after a fresh listing: %v", err)
	}
	if name != "alpha" {
		t.Fatalf("LoadSkill name = %q, want alpha", name)
	}
	if !state.HasActiveSkill("alpha") {
		t.Fatal("the loaded skill is not active in the session")
	}

	if _, err := sess.LoadSkill(ctx, "global", "alpha.md"); err == nil ||
		!strings.Contains(err.Error(), "is already active") {
		t.Fatalf("second LoadSkill = %v, want the already-active refusal", err)
	}
}

func aRemovalUsesTheListedSource(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	global, _ := browserSkillStores(t, home, work)
	one := browserWriteSkill(t, global, "one.md", "shared", "first fixture")
	two := browserWriteSkill(t, global, "two.md", "shared", "second fixture")
	sess := open(t, service.Deps{State: browserWorkState(t, work), HomeDir: home})
	ctx := context.Background()

	entries, err := sess.Skills(ctx)
	if err != nil {
		t.Fatalf("Skills: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Skills = %+v, want both entries sharing a name", entries)
	}

	if err := sess.RemoveSkill(ctx, "global", "one.md"); err != nil {
		t.Fatalf("RemoveSkill: %v", err)
	}
	if _, err := os.Stat(one); !os.IsNotExist(err) {
		t.Errorf("removed entry's file still exists: %v", err)
	}
	if _, err := os.Stat(two); err != nil {
		t.Errorf("the sibling's file is gone: %v", err)
	}
}

func theInstallRefusals(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	browserSkillStores(t, home, work)
	sess := open(t, service.Deps{State: browserWorkState(t, work), HomeDir: home})
	ctx := context.Background()

	cases := []struct {
		name    string
		source  string
		scope   string
		wantErr string
	}{
		{"blank source", "   ", "global", "enter a source first"},
		{"project scope on an untrusted session", filepath.Join(work, "source.md"), "project", "project scope needs a trusted folder"},
		{"unknown scope", filepath.Join(work, "source.md"), "elsewhere", `unknown skill scope "elsewhere"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sess.InstallSkill(ctx, tc.source, tc.scope)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("InstallSkill(%q, %q) = %v, want %q", tc.source, tc.scope, err, tc.wantErr)
			}
		})
	}
}

func autoloadWritesTheUserConfig(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	userConfig := filepath.Join(home, "config.toml")
	sess := open(t, service.Deps{
		State:          browserWorkState(t, work),
		HomeDir:        home,
		UserConfigPath: userConfig,
	})
	ctx := context.Background()

	if err := sess.SetSkillAutoload(ctx, "alpha", true); err != nil {
		t.Fatalf("SetSkillAutoload(alpha, true): %v", err)
	}
	if got := userConfigAutoload(t, userConfig); !containsString(got, "alpha") {
		t.Errorf("skills.autoload in the user config = %v, want it to hold alpha", got)
	}
	if got := sess.Config().Skills.Autoload; !containsString(got, "alpha") {
		t.Errorf("Config().Skills.Autoload = %v, want it to hold alpha", got)
	}
	// Writing one name leaves the list's other entries alone.
	for _, name := range config.Default().Skills.Autoload {
		if got := userConfigAutoload(t, userConfig); !containsString(got, name) {
			t.Errorf("skills.autoload in the user config = %v, want it to keep %q", got, name)
		}
		if got := sess.Config().Skills.Autoload; !containsString(got, name) {
			t.Errorf("Config().Skills.Autoload = %v, want it to keep %q", got, name)
		}
	}

	if err := sess.SetSkillAutoload(ctx, "alpha", false); err != nil {
		t.Fatalf("SetSkillAutoload(alpha, false): %v", err)
	}
	if got := userConfigAutoload(t, userConfig); containsString(got, "alpha") {
		t.Errorf("skills.autoload in the user config = %v, want no alpha", got)
	}
	if got := sess.Config().Skills.Autoload; containsString(got, "alpha") {
		t.Errorf("Config().Skills.Autoload = %v, want no alpha", got)
	}
}

// userConfigAutoload reads skills.autoload out of a user config file as TOML,
// so a stray substring elsewhere in the file cannot stand in for the entry.
func userConfigAutoload(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the user config: %v", err)
	}
	var file struct {
		Skills struct {
			Autoload []string `toml:"autoload"`
		} `toml:"skills"`
	}
	if err := toml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse the user config: %v", err)
	}
	return file.Skills.Autoload
}

func theSkillGateIsInTheStatus(t *testing.T, open func(t *testing.T, d service.Deps) seam) {
	home, work := t.TempDir(), t.TempDir()
	browserSkillStores(t, home, work)
	state := browserWorkState(t, work)
	sess := open(t, service.Deps{State: state, HomeDir: home})
	ctx := context.Background()

	if err := sess.SetSkillGate(ctx, false); err != nil {
		t.Fatalf("SetSkillGate(false): %v", err)
	}
	browserAwaitGate(t, sess, func(gate acpwire.SkillGateState) bool {
		return !gate.Enabled && len(gate.Decisions) == 0
	}, "the gate off with no decisions")

	if err := sess.SetSkillGate(ctx, true); err != nil {
		t.Fatalf("SetSkillGate(true): %v", err)
	}
	browserAwaitGate(t, sess, func(gate acpwire.SkillGateState) bool {
		return gate.Enabled && len(gate.Decisions) == 0
	}, "the gate on with no decisions")

	state.SkillGateRecordDeny("gamma")
	browserAwaitGate(t, sess, func(gate acpwire.SkillGateState) bool {
		return len(gate.Decisions) == 1 &&
			gate.Decisions[0].Skill == "gamma" &&
			!gate.Decisions[0].Allowed &&
			gate.Decisions[0].Count == 1
	}, "gamma denied once")

	if err := sess.ClearSkillGateDecision(ctx, "gamma"); err != nil {
		t.Fatalf("ClearSkillGateDecision: %v", err)
	}
	browserAwaitGate(t, sess, func(gate acpwire.SkillGateState) bool {
		return len(gate.Decisions) == 0
	}, "no decisions after the clear")
}

// browserAwaitGate polls the session's status until the gate satisfies want.
// The pipe arm carries the gate on a status update, so a read right after the
// call may still hold the previous one; the deadline is the harness's usual
// five seconds.
func browserAwaitGate(t *testing.T, sess seam, want func(acpwire.SkillGateState) bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if want(sess.Status().SkillGate) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; the status holds %+v", what, sess.Status().SkillGate)
}

// containsString reports whether ss holds want.
func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// The budgets (R10).

// TestNoteCallBudget times AddNote, which returns once the barrier flushed the
// note's stack patch: one flush of the large session, whose median must stay
// under R10's 10ms. It logs what the TUI pays in process beside it.
func TestNoteCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	withoutMemoCheck(t)

	st := largeStackSession(t)
	pipe := openPipeSeamTB(t, service.Deps{State: st})
	// The first read starts the idle stack loop, so each note has a loop to
	// reach.
	_ = pipe.Stack(0, false)

	const runs = 30
	ctx := context.Background()
	notes := make([]string, 0, runs)
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		text := "note " + time.Duration(i).String()
		notes = append(notes, text)
		start := time.Now()
		if err := pipe.AddNote(ctx, acpwire.Note{Text: text}); err != nil {
			t.Fatalf("AddNote: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	// The last read shows the last note, so the timing measured the flush.
	if last := notes[len(notes)-1]; !stackHoldsText(pipe.Stack(0, false), last) {
		t.Fatalf("the read after the last AddNote does not hold %q", last)
	}
	median := medianDuration(durations)
	t.Logf("note call over the pipe: median %v over %d runs (budget 10ms)", median, runs)
	if median >= 10*time.Millisecond {
		t.Fatalf("note call median = %v, budget 10ms (R10): do not loosen it", median)
	}

	// What the TUI pays in process after a note: the call, then its read.
	inProcess := openServiceSeam(t, service.Deps{State: largeStackSession(t)})
	_ = inProcess.Stack(0, false)
	inProcessRuns := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := inProcess.AddNote(ctx, acpwire.Note{Text: "in process"}); err != nil {
			t.Fatalf("AddNote in process: %v", err)
		}
		_ = inProcess.Stack(0, false)
		inProcessRuns = append(inProcessRuns, time.Since(start))
	}
	t.Logf("note and read in process: median %v over %d runs", medianDuration(inProcessRuns), runs)
}

// TestBrowserCallBudget times a plain browser call: SetSkillGate, whose answer
// carries a session_status update, must stay under R10's 1ms.
func TestBrowserCallBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing budget: skipped under -short and with the race detector")
	}
	withoutMemoCheck(t)

	st := largeStackSession(t)
	pipe := openPipeSeamTB(t, service.Deps{State: st})
	// The first status read subscribes this connection, so the update a call
	// causes has a reader to reach.
	_ = pipe.Status()

	const runs = 30
	ctx := context.Background()
	durations := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := pipe.SetSkillGate(ctx, i%2 == 0); err != nil {
			t.Fatalf("SetSkillGate: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	median := medianDuration(durations)
	t.Logf("browser call: median %v over %d runs (budget 1ms)", median, runs)
	if median >= time.Millisecond {
		t.Fatalf("browser call median = %v, budget 1ms (R10): do not loosen it", median)
	}
}
