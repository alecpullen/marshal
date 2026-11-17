package contract

// ContainerViewerMethods are the methods a containerized agent's viewer may
// call (decision 1), sorted. The relay and the agent both refuse every other.
//
// The list is the session screen's own surface, minus everything that would
// let a container touch the machine it runs on. Each group is here because the
// session screen needs it to be:
//
//   - the connection's own (initialize, session/status, session/sync,
//     session/stack_busy, $/cancel_request): a viewer must open, learn what
//     the session is doing, hear that its stacks moved, say whether a turn is
//     running, and cancel a request it started. Without these there is no
//     connection to serve the rest on;
//   - turns (session/prompt and the stop, steer, review, agent-run, suggest,
//     verify, swarm, subagent-cancel, subagent-steer, rollback and plan-run
//     calls): the screen starts and shapes the work it watches. All of them act
//     on the session's runtime and workspace, which is the part of the machine
//     the container holds;
//   - controls (session/set_mode, session/set_effort): the two knobs the
//     screen turns for the next turn, on the session and no wider;
//   - answers (session/pending, session/answer_approval,
//     session/answer_question, session/answer_skill_gate,
//     session/answer_access): a blocked turn is answered where it is shown, and
//     these five carry no credential — the sudo and sign-in answers are
//     refused, below;
//   - reads and browse calls (session/sheet, the stack and callers family,
//     the work list, messages, the file index and file, intelligence, plans,
//     diff, config, prompt history, the command, memory, skill and plugin
//     lists, the watch-resume take and the postmortem): the screen reads the
//     session's own state and workspace through them. The agent-side slash
//     commands (session/command) stay here too: each works on the session's
//     state and workspace, and none writes user config;
//   - notes (session/note, session/notice_set, session/notice_clear,
//     session/notice_dismiss): a session's transcript keeps what the screen
//     writes into it;
//   - images (session/images_put, session/vision_force): the screen pastes or
//     uploads a picture into the session it is watching, and answers a refusal
//     with "send anyway". The bytes land in the agent's own store, which the
//     container holds, and the override is the session's own memory; nothing
//     about either reaches the host.
//
// A method that is not here is refused: every Host method and with it every
// config/ and trust/ method, the lifecycle and the ending, credentials and
// sign-ins, and the settings' writes — the calls that would let a container
// write the host's configuration or spend its owner's secrets.
//
// TestTheContainerListIsSorted and the rest of container_viewers_test.go keep
// this list against acpclient.Methods and the server's registrations.
var ContainerViewerMethods = []string{
	"$/cancel_request",
	"initialize",
	"session/agent_run",
	"session/answer_access",
	"session/answer_approval",
	"session/answer_question",
	"session/answer_skill_gate",
	"session/callers",
	"session/command",
	"session/command_action",
	"session/command_list",
	"session/config",
	"session/diff",
	"session/file",
	"session/file_index",
	"session/images_put",
	"session/intelligence",
	"session/memory_delete",
	"session/memory_list",
	"session/messages",
	"session/note",
	"session/notice_clear",
	"session/notice_dismiss",
	"session/notice_set",
	"session/pending",
	"session/plans",
	"session/plugins_list",
	"session/postmortem",
	"session/prompt",
	"session/prompt_history",
	"session/prompt_history_save",
	"session/review",
	"session/rollback",
	"session/sdd_answer",
	"session/sdd_author",
	"session/sdd_clear",
	"session/sdd_discard",
	"session/sdd_preflight",
	"session/sdd_scaffold",
	"session/sdd_start",
	"session/sdd_stop",
	"session/set_effort",
	"session/set_mode",
	"session/sheet",
	"session/skills_list",
	"session/skills_load",
	"session/stack",
	"session/stack_busy",
	"session/stack_node",
	"session/stack_unsubscribe",
	"session/status",
	"session/steer",
	"session/steering_clear",
	"session/steering_pop",
	"session/stop",
	"session/subagent_cancel",
	"session/subagent_steer",
	"session/suggest",
	"session/swarm_start",
	"session/sync",
	"session/verify_proposal",
	"session/vision_force",
	"session/watch_resume_take",
	"session/work_inspect",
	"session/work_list",
	"session/work_stop",
}

// The child-control wire types, mirroring internal/acpwire's: the bridge is
// standard library only, so it carries its own copies and the JSON property
// names are the contract. A session's status reports SubagentCapabilities
// under "subagentControls"; absent there means unsupported.

// SubagentCapabilities is which child controls a session registers. A false
// (or absent) bit is unavailable: a client offers no control and never infers
// one from another session.
type SubagentCapabilities struct {
	// Settings is per-session child settings, which Marshal does not enforce
	// yet.
	Settings bool `json:"settings"`
	// Steer delivers one user instruction to a live child.
	Steer bool `json:"steer"`
	// Stop asks a live child to stop.
	Stop bool `json:"stop"`
	// Observe is the child's own transcript and lifecycle.
	Observe bool `json:"observe"`
}

// SubagentSteerParams is session/subagent_steer's params: one user instruction
// for a live child, named by the request ID that makes it a single delivery.
// The bridge never forwards its own ID: a retry must carry the caller's.
type SubagentSteerParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId"`
	RequestID  string `json:"requestId"`
	Message    string `json:"message"`
}

// SubagentSteerResult is session/subagent_steer's result. The status is
// accepted, never consumed: the child's queue took the instruction, and a
// child that settles first may never read it.
type SubagentSteerResult struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
}

// SubagentCancelParams is session/subagent_cancel's params. It is the existing
// ACP control: the bridge's cancel route is a second route, not a second
// mechanism.
type SubagentCancelParams struct {
	SessionID  string `json:"sessionId"`
	SubagentID int64  `json:"subagentId"`
}

// SubagentCancelResult is session/subagent_cancel's result: the cancellation
// was asked for, which is not the same as the child having stopped.
type SubagentCancelResult struct {
	Cancelled bool `json:"cancelled"`
}
