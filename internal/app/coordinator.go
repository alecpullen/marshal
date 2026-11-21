package app

import (
	"strings"

	"marshal/internal/agent"
	"marshal/internal/tools/registry"
)

// The coordination runtime role.
//
// A coordinator conversation is Marshal's own destination: it reads what the
// fleet holds, explains it, and hands work on. It is deliberately not a coding
// agent, so its tool set is an explicit allowlist rather than the parent
// registry minus a few names: a tool added to the standard set later is outside
// this role by default, and adding one here is a deliberate act.
//
// Everything that reaches outside a conversation is excluded — shell execution
// (shell.run, test.run, shell.sudo), repository editing (file.write,
// file.edit, file.write_patch), workspace lifecycle (workspace.worktree,
// workspace.finish), nested agents (agent.run and its siblings), configuration
// and access management (config.write, mode.request), and the image and network
// surfaces. Those are what the project agent a coordinator delegates to owns;
// a coordinator that could call them itself would be a second, unsupervised
// worker rather than the conversation the user is talking to.
//
// The fleet's own tools are reached through the Studio MCP integration, not
// through this registry: the attention read is scoped by the task's session and
// resolved against the access policy on every call (marshal_studio_tools.go).
// This view governs what the coordinator can do locally, which is nothing that
// mutates the host.

// CoordinatorRole is the runtime role of a coordinator conversation.
//
// It is an agent.AgentRole (an alias of routing.AgentRole) so it flows through
// the same role plumbing every other runner uses, but it is deliberately absent
// from routing.AllRoles: it has no dedicated route, resolves through the
// configured implementer binding, and must not appear in /agents settings as a
// role a person can rebind independently of the work that role does.
const CoordinatorRole agent.AgentRole = "coordinator"

// coordinationTools is the explicit coordination-only tool set. A name not
// listed here is not offered to a coordinator conversation.
var coordinationTools = map[string]bool{
	// Reading what the daemon already knows: the transcript the conversation
	// is part of, and the repository it may need to name precisely.
	"file.read":       true,
	"repo.search":     true,
	"symbols":         true,
	"git.status":      true,
	"git.diff":        true,
	"git.log":         true,
	"git.blame":       true,
	"transcript_read": true,
	// The conversation's own affordances: a todo list it shows the user, a
	// question only the user can answer, and the tool disclosure helper.
	"todo.write":   true,
	"question.ask": true,
	"tools.select": true,
}

// CoordinationRegistry returns a registry holding only the coordination tool
// set. A nil or empty source yields an empty registry, never a nil one: a
// coordinator with no tools is a working (if useless) conversation, while a nil
// registry would panic the first turn.
func CoordinationRegistry(src *registry.Registry) *registry.Registry {
	view := registry.New()
	if src == nil {
		return view
	}
	for _, tool := range src.List() {
		if !coordinationTools[tool.Name] {
			continue
		}
		_ = view.Register(tool)
	}
	return view
}

// CoordinationAllowed reports whether one tool name is inside the coordination
// role. It is exported so a test can pin the boundary without building a
// registry.
func CoordinationAllowed(name string) bool { return coordinationTools[name] }

// coordinationPromptAddendum is appended to the system prompt of a coordinator
// conversation. It states the boundary the tool set enforces, so a model that
// reaches for shell.run is told why it is not there rather than retrying.
const coordinationPromptAddendum = `You are Marshal's coordinator for this host. Your job is to read what the host's projects are waiting on, explain it plainly, and hand work to a project agent when the user asks for it.

You cannot run commands, edit files or start agents yourself: those tools are not in this conversation. If a request needs them, say which project it belongs to and let that project's agent do the work. An answer about the fleet is read-only and must say what it could not read: a project whose access was revoked, or whose state could not be reached, is not "all clear".`

// withCoordinationAddendum appends the coordination directive to whatever
// addendum the runner already carries (repository instructions).
func withCoordinationAddendum(existing string) string {
	if strings.TrimSpace(existing) == "" {
		return coordinationPromptAddendum
	}
	return existing + "\n\n" + coordinationPromptAddendum
}

// WithCoordinatorRole composes a runtime as Marshal's coordinator conversation:
// the coordination-only tool set replaces the standard one, and the runner
// carries the coordinator role and its prompt directive.
//
// The role is applied at composition rather than by the model: nothing in a
// prompt or a tool result can widen it, and a tool the standard set gains later
// stays outside this conversation until it is listed above.
func WithCoordinatorRole() Option {
	return func(opts *options) {
		opts.coordinatorRole = true
	}
}
