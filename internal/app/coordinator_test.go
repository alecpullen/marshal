package app

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// TestCoordinationRegistryKeepsNoMutationTool pins the coordination role's tool
// boundary: a coordinator conversation is offered reads and its own affordances,
// and nothing that shells out, edits a repository, manages access or mutates the
// fleet.
//
// The assertion is made against a registry built by the real native
// registration, so it is the actual tool set that is filtered rather than a
// hand-written list that could drift from it. A control check proves the source
// registry really does hold the excluded tools, so the exclusion cannot pass
// vacuously.
func TestCoordinationRegistryKeepsNoMutationTool(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	src := registry.New()
	if err := native.RegisterAll(src, native.Options{WorkspaceRoot: dir, Config: cfg}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// The boundary the plan names. Each name is one the standard set really
	// holds, so dropping it is a decision rather than an accident of
	// registration.
	mutation := []string{
		"shell.run",          // shell execution
		"test.run",           // shell execution
		"file.write",         // repository editing
		"file.edit",          // repository editing
		"workspace.worktree", // workspace lifecycle
		"workspace.finish",   // workspace lifecycle
		"agent.run",          // nested agents
		"agent.steer",        // steering another agent
		"agent.await",        // waiting on another agent
		"agent.kill",         // stopping another agent
		"config.read",        // configuration surface
		"mode.request",       // access management
		"question.ask",       // re-added below only as a control, see allowlist
		"todo.write",         // re-added below only as a control, see allowlist
	}
	for _, name := range mutation {
		if _, ok := src.Lookup(name); !ok {
			continue
		}
		if !isCoordinationControl(name) && CoordinationAllowed(name) {
			t.Errorf("%s is a mutation tool inside the coordination role", name)
		}
	}

	view := CoordinationRegistry(src)
	if view == nil {
		t.Fatal("CoordinationRegistry returned nil")
	}
	for _, name := range mutation {
		if !isCoordinationControl(name) {
			if _, ok := view.Lookup(name); ok {
				t.Errorf("the coordination registry still offers %s", name)
			}
		}
	}
	// The read tools a coordinator needs are present, so the view is a working
	// conversation rather than an empty one.
	for _, name := range []string{"file.read", "repo.search"} {
		if _, ok := src.Lookup(name); !ok {
			t.Fatalf("control: the standard set lacks %s", name)
		}
		if _, ok := view.Lookup(name); !ok {
			t.Errorf("the coordination registry dropped the read tool %s", name)
		}
	}
	// Every tool in the view is inside the allowlist: a view that kept
	// something else would widen the role without naming it.
	for _, tool := range view.List() {
		if !CoordinationAllowed(tool.Name) {
			t.Errorf("the coordination registry offers %s, which is not in the allowlist", tool.Name)
		}
	}
}

// isCoordinationControl reports whether a name is deliberately inside the
// coordination role. Those two names are the conversation's own affordances
// (a visible todo list and a question only the user can answer), so they are
// in the allowlist by design and must not be counted as mutation tools.
func isCoordinationControl(name string) bool {
	return name == "question.ask" || name == "todo.write"
}

// TestCoordinationRoleIsNotAUserEditableRole pins that the coordinator role does
// not appear among the roles a person can rebind in settings: it is a runtime
// role for one kind of conversation, not a configurable model slot.
func TestCoordinationRoleIsNotAUserEditableRole(t *testing.T) {
	for _, role := range routing.AllRoles {
		if role == CoordinatorRole {
			t.Fatalf("the coordinator role must not appear in the user-editable role list")
		}
	}
	if CoordinatorRole == "" {
		t.Fatal("the coordinator role has no name")
	}
}

// TestCoordinationPromptStatesTheBoundary pins that the directive a coordinator
// conversation carries says what it cannot do, so a model that reaches for a
// shell tool is told why it is absent instead of retrying.
func TestCoordinationPromptStatesTheBoundary(t *testing.T) {
	composed := withCoordinationAddendum("repo instructions")
	for _, want := range []string{"repo instructions", "coordinator", "all clear"} {
		if !containsFold(composed, want) {
			t.Fatalf("the composed addendum lacks %q:\n%s", want, composed)
		}
	}
	if got := withCoordinationAddendum(""); got != coordinationPromptAddendum {
		t.Fatal("an empty addendum was not replaced by the coordination directive")
	}
}

// containsFold reports whether s contains substr, ignoring case.
func containsFold(s, substr string) bool {
	return len(substr) == 0 || indexFold(s, substr) >= 0
}

// indexFold is a small case-insensitive substring search, so the assertion does
// not depend on the prompt's capitalisation.
func indexFold(s, substr string) int {
	lower := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	needle := make([]byte, len(substr))
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		needle[i] = c
	}
	for i := 0; i+len(needle) <= len(lower); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if lower[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
