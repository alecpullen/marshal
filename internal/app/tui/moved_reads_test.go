package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"marshal/internal/app/session"
)

// movedStateReads names each session.State method or field the TUI no
// longer reads from m.state, with the plan that moved it.
//
// Each later P2 plan adds the names it moves, with its own plan number. Plan 9
// deletes this test along with WithLegacyState. A name is never removed to make
// the test pass.
var movedStateReads = map[string]string{
	"WorkingDir":         "2a",
	"Generation":         "2a",
	"LeafID":             "2a",
	"Branches":           "2a",
	"SessionID":          "2a",
	"ActiveRoute":        "2a",
	"TurnRoute":          "2a",
	"TurnUsage":          "2a",
	"TurnQuota":          "2a",
	"EffortOverride":     "2a",
	"InteractionState":   "2a",
	"SudoUnlockEnabled":  "2a",
	"Trusted":            "2a",
	"HasBackup":          "2a",
	"Activity":           "2a",
	"HasRunningSubagent": "2a",
	"Workspace":          "2a",
	"BrowserInfo":        "2a",

	"SteeringQueue":    "2b",
	"SteeringSnapshot": "2b",
	"SubmitSendNow":    "2b",
	"RunningJobsCount": "2b",
	"SwarmProgress":    "2b",
	"SDDProgress":      "2b",
	"SDDGate":          "2b",

	"AuditLog":        "2c",
	"ContextPack":     "2c",
	"SessionRules":    "2c",
	"ActiveSkills":    "2c",
	"ToolBudget":      "2c",
	"LoadedToolNames": "2c",
	"StartedAt":       "2c",

	// callers.go's last transcript read moved in 3b: the rows a callers
	// lookup applies to now come from the wire index.
	"Transcript": "3b",

	// The cursor's box stop found its child through the state; drill-in
	// resolves it through Status since 3d.
	"Subagent": "3d",

	// The access panel presents what ReconcileAccess reports and answers
	// through AnswerAccess since 4.
	"AccessRequest": "4",
	"CancelAccess":  "4",

	// The three prompts read Pending and answer through the session since 4:
	// their forms hold the wire request, and the adapter records what the
	// answer implies.
	"PendingQuestion":      "4",
	"PendingSkillGate":     "4",
	"PendingSudoAuth":      "4",
	"SetPendingQuestion":   "4",
	"SetPendingSkillGate":  "4",
	"SetPendingSudoAuth":   "4",
	"ResolveQuestion":      "4",
	"ResolveSkillGate":     "4",
	"ResolveSudoAuth":      "4",
	"SkillGateRecordAllow": "4",

	// The running shell row and the approval panel read the sandbox from
	// Status since 4.
	"SandboxInfo": "4",

	// Both rollback paths call Session.Rollback since 4; the adapter restores
	// the backup and logs the audit row.
	"RollbackBackup": "4",
	"LogToolCall":    "4",

	// Approvals read Pending and answer through AnswerApproval since 4: the
	// session lists every open approval, the form holds the wire request, and
	// the adapter derives the forced deny and the standing rules.
	"PendingApproval":    "4",
	"SetPendingApproval": "4",
	"ResolveApproval":    "4",
	"ClearPrompt":        "4",

	// A work item's subagent is found through Status().Subagents since 5a:
	// the item's reference names the child's session ID, and the wire view
	// carries it on its drill.
	"Subagents": "5a",

	// The steering queue, the drilled subagent's stop, a finished run's clear
	// and the watch-resume latch all go through the session since 5b: the
	// adapter forwards each to the state.
	"PushSteering":     "5b",
	"PopSteering":      "5b",
	"ClearSteering":    "5b",
	"CancelSubagent":   "5b",
	"ClearRunEvents":   "5b",
	"ClearSDDProgress": "5b",
	"TakeWatchResume":  "5b",

	// /system unlock goes through the session since 5b.
	"SetSudoUnlockEnabled": "5b",

	// The gate's stop goes through StopSDD since 5d.
	"ClearSDDGate": "5d",

	// Every run the TUI starts registers its work, stop group and idle reset
	// in the session since 5d: the run path owns them, so the model no longer
	// calls them.
	"BeginWork":       "5d",
	"EndWork":         "5d",
	"BeginTurnStop":   "5d",
	"EndTurnStop":     "5d",
	"RequestTurnStop": "5d",
	"SetActivity":     "5d",

	// Quitting closes the session since 5e: Close settles the pending requests
	// and shuts the state down agent side.
	"ResolvePendingForShutdown": "5e",
	"Shutdown":                  "5e",

	// The TUI's notes go through AddNote, and it logs through its own logger,
	// since 6.
	"AddMessage": "6",
	"Logger":     "6",

	// The banner is read from Status and changed through the session since 6:
	// a failed run's notice is classified agent side and carried on its error.
	"Notice":        "6",
	"SetNotice":     "6",
	"ClearNotice":   "6",
	"DismissNotice": "6",

	// Every message scan goes through Session.Messages since 8a: the last
	// assistant reply, the last final answer, the user turns, the has-a-user-turn
	// check and /new's cleared count.
	"Messages": "8a",

	// The model reads the layers through the session since 7b, and the
	// adapter refreshes them after every write: the TUI holds no snapshot of
	// its own and never installs one. Installing a new config is the same
	// plan's move: applyNewConfig rebases through Session.ApplyConfig.
	"Layers":                  "7b",
	"SetLayers":               "7b",
	"RebaseInteractionConfig": "7b",

	// Every config read goes through Session.Config since 7b.
	"Config": "7b",

	// The skills panel drives the session's skill surfaces through the seam
	// since 8b: the gate is read from Status and changed through the session,
	// and "Load now" asks whether the skill is already active agent side.
	"SkillGateEnabled":       "8b",
	"SkillGateSetEnabled":    "8b",
	"SkillGateDecisions":     "8b",
	"SkillGateClearDecision": "8b",
	"HasActiveSkill":         "8b",
}

// deferredStateReads names, per file, the moved reads that file still makes and
// the plan that finishes the move. It exists so a move staged across plans is
// still enforced everywhere else: the guard fails the moment any *other* file
// reads the name, and an entry fails the guard once its last read disappears.
var deferredStateReads = map[string]map[string]string{}

// movedReadSites returns one "<line>\t<Name>" entry per selector X.Sel in f
// where X is itself a selector whose Sel is "state" and whose X is an
// identifier — as in m.state — and Sel is in moved.
//
// It finds m.state.Trusted() and m.state.WorkingDir. It does not find
// child.Trusted(), state.Workspace() or m.state.Config.
func movedReadSites(fset *token.FileSet, f *ast.File, moved map[string]string) []string {
	var sites []string
	ast.Inspect(f, func(n ast.Node) bool {
		outer, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, ok := moved[outer.Sel.Name]; !ok {
			return true
		}
		inner, ok := outer.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "state" {
			return true
		}
		if _, ok := inner.X.(*ast.Ident); !ok {
			return true
		}
		sites = append(sites, fmt.Sprintf("%d\t%s", fset.Position(outer.Pos()).Line, outer.Sel.Name))
		return true
	})
	return sites
}

// TestMovedStateReadsStayMoved fails on any read of a moved State method or
// field through a model's state (T1b). It walks the package's non-test Go
// files the way seamViolations does.
func TestMovedStateReadsStayMoved(t *testing.T) {
	fset := token.NewFileSet()
	// seenDeferred records the staged exemptions this walk actually observed,
	// so an exemption cannot outlive the read it covers.
	seenDeferred := map[string]bool{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return perr
		}
		rel, rerr := filepath.Rel(".", p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		deferred := deferredStateReads[rel]
		for _, site := range movedReadSites(fset, f, movedStateReads) {
			line, field, _ := strings.Cut(site, "\t")
			if plan, ok := deferred[field]; ok {
				// Staged: this file's move finishes in a later plan. Recorded
				// in deferredStateReads, which TestDeferredStateReadsStaysLive
				// keeps honest.
				_ = plan
				seenDeferred[rel+"\t"+field] = true
				continue
			}
			t.Errorf("%s:%s reads state.%s, which moved behind the seam in plan %s: read it from m.sess",
				rel, line, field, movedStateReads[field])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for file, names := range deferredStateReads {
		for name, plan := range names {
			if !seenDeferred[file+"\t"+name] {
				t.Errorf("deferredStateReads exempts %s from state.%s until plan %s, but %s no longer reads it: drop the entry",
					file, name, plan, file)
			}
		}
	}
}

// TestMovedStateReadsAreRealNames keeps the guard's list honest: every name it
// polices must exist on session.State as a method or a field. Without this, a
// typo in a later plan's addition switches that entry off silently, and the
// guard reports success for a name nothing can ever match.
func TestMovedStateReadsAreRealNames(t *testing.T) {
	typ := reflect.TypeOf(&session.State{})
	names := make([]string, 0, len(movedStateReads))
	for name := range movedStateReads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := typ.MethodByName(name); ok {
			continue
		}
		if _, ok := typ.Elem().FieldByName(name); ok {
			continue
		}
		t.Errorf("movedStateReads names %q (plan %s), which is neither a method nor a field of session.State",
			name, movedStateReads[name])
	}
}

// TestMovedReadsGuardFindsSelectors guards the guard: it must see a model's
// state selectors and nothing else.
func TestMovedReadsGuardFindsSelectors(t *testing.T) {
	src := `package tui

func f(m *Model, child *session.State) {
	_ = m.state.Trusted()
	_ = m.state.WorkingDir
	_ = child.Trusted()
	_ = m.state.Config
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	got := movedReadSites(fset, f, movedStateReads)
	if len(got) != 3 {
		t.Fatalf("movedReadSites returned %d entries, want 3: %v", len(got), got)
	}
	var names []string
	for _, site := range got {
		_, field, _ := strings.Cut(site, "\t")
		names = append(names, field)
	}
	sort.Strings(names)
	if want := []string{"Config", "Trusted", "WorkingDir"}; !slices.Equal(names, want) {
		t.Errorf("movedReadSites names = %v, want %v", names, want)
	}
}
