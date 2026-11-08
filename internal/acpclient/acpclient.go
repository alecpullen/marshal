// Package acpclient is the seam the TUI holds the agent side by (spec C2).
//
// The TUI reaches the agent side only through the two interfaces below: Host
// for the transport, Session for one conversation. Reads are synchronous and
// every call takes a context, so a caller never waits on a promise and an
// implementation never has to buffer for one.
//
// Each method's doc ends with an "ACP:" line naming the ACP method that
// carries it, or "ACP: none" when the method is a property of the transport.
// Methods carries the same mapping as data, so the seam can be checked against
// acp.Server without reading the docs.
//
// The TUI may import this package and acpwire, never inproc: the in-process
// adapter is an implementation, and the wire types are the only values that
// cross. The interfaces gain their groups' methods plan by plan (spec
// Migration order); a method added here needs a row in Methods and an ACP:
// line in its doc.
package acpclient

import (
	"context"
	"encoding/json"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// Host is the transport the TUI holds: what the agent side is, independent of
// any one session. It gains its groups' methods plan by plan (spec Migration
// order).
//
// Over ACP every host call but SharedFilesystem names the client's current
// session, whose runtime serves it.
type Host interface {
	// SharedFilesystem reports whether a path the agent names is also a
	// path on this machine, so the TUI may open it in $EDITOR (C12).
	//
	// ACP: none (a property of the transport; true for the in-process
	// adapter and for P3's pipe).
	SharedFilesystem() bool

	// NewSession creates a session in the agent and makes it the current one,
	// closing the session it replaces. A name that is not empty is the new
	// session's title. A failure leaves the current session as it was.
	//
	// ACP: session/new (exists), with replaces naming the current session: the
	// agent builds the new session inside the same runtime. Asynchronous (R6).
	NewSession(ctx context.Context, name string) (Session, error)

	// TrustPrompt is the directory whose project config waits for a trust
	// decision, or "" when none does. It does not change while the host
	// runs.
	//
	// ACP: trust/prompt (exists). Read once, after the first attach, and
	// kept.
	TrustPrompt() string

	// DecideTrust answers the trust prompt for dir. After a trust the client
	// ends its program, and the app restarts with the project config in
	// force; after a decline the session carries on.
	//
	// ACP: trust/decide (exists).
	DecideTrust(ctx context.Context, dir string, decision acpwire.TrustDecision) error

	// ListSessions lists the stored sessions whose working directory is cwd,
	// most recent first, at most limit. It fails with CodeNoDatabase when
	// the agent has no database.
	//
	// ACP: session/list (exists). A marshal client's call names its current
	// session, whose service host serves it.
	ListSessions(ctx context.Context, cwd string, limit int) ([]acpwire.SessionEntry, error)

	// ProviderTemplates lists the provider catalog, sorted by ID, each with
	// every field.
	//
	// ACP: config/provider_templates (exists). Read once, after the first
	// attach, and kept.
	ProviderTemplates() []acpwire.ProviderTemplate

	// ProbeProvider lists the models pc's endpoint reports, for a provider
	// named name. pc need not be saved, and may carry a literal key (C3). The
	// probe itself saves nothing: SaveModels does that. A provider whose own
	// client refreshes the cache while listing (OpenAI Codex) may leave an
	// entry keyed by pc, which SaveModels then overwrites with the saved
	// config's.
	//
	// ACP: config/probe_provider (exists). A marshal client's call is served
	// by the session's service.
	ProbeProvider(ctx context.Context, name string, pc config.ProviderConfig) ([]schema.ModelInfo, error)

	// CanProbeModel reports whether ProbeModel can tell anything about a model
	// of pc: false when pc's client cannot be built or cannot probe a model.
	//
	// ACP: config/probe_model (exists).
	CanProbeModel(ctx context.Context, name string, pc config.ProviderConfig) bool

	// ProbeModel probes one model's capabilities. A client that cannot probe
	// returns zero capabilities and no error; one that cannot be built returns
	// zero capabilities and the error.
	//
	// ACP: config/probe_model (exists).
	ProbeModel(ctx context.Context, name string, pc config.ProviderConfig, model string) (acpwire.ModelCapabilities, error)

	// ListModels returns the cached model list for the provider called name,
	// when the cache holds one for the provider's current config that is still
	// fresh.
	//
	// ACP: config/models_list (exists).
	ListModels(ctx context.Context, name string) ([]schema.ModelInfo, bool)

	// SaveModels caches models for the provider called name, keyed by its
	// current config. An empty list, or a name the session's config does not
	// have, changes nothing.
	//
	// ACP: config/models_save (exists).
	SaveModels(ctx context.Context, name string, models []schema.ModelInfo) error

	// ForgetModels drops the cached list for the provider called name.
	//
	// ACP: config/models_forget (exists).
	ForgetModels(ctx context.Context, name string) error

	// RenameModels moves the cached list of the provider called from to the
	// name to. A from the cache does not hold changes nothing.
	//
	// ACP: config/models_rename (exists).
	RenameModels(ctx context.Context, from, to string) error

	// PrepareProviderLogin checks that the browser sign-in for the provider
	// called name can start, and returns why not.
	//
	// ACP: config/login_provider (exists).
	PrepareProviderLogin(ctx context.Context, name string) error

	// LoginProvider runs the browser sign-in for the provider called name and
	// stores its tokens. It calls show once with the authorization URL, which
	// the client opens, and returns when the sign-in ends.
	//
	// ACP: config/login_provider (exists). show is fed by the call's
	// progress notification.
	LoginProvider(ctx context.Context, name string, show func(url string)) error

	// SaveUserValue writes value at the dotted TOML path tomlPath of the user
	// config, leaving the rest of the file alone. value is a bool, string,
	// integer or string list, or what config.LookupPath returns for one.
	//
	// ACP: config/set_value (exists). The value travels as JSON, and the
	// agent converts it by the field's type.
	SaveUserValue(ctx context.Context, tomlPath string, value any) error

	// SaveUserProviders writes the user config's [providers] section,
	// credentials included. Entries on disk that are in neither providers nor
	// baseline survive: another process wrote them after baseline was taken.
	// A masked key is restored to the stored key it stands for; renamed maps a
	// provider renamed since baseline to its name there, so its key follows
	// it (plan 7d).
	//
	// ACP: config/save_providers (exists).
	SaveUserProviders(ctx context.Context, providers, baseline map[string]config.ProviderConfig, renamed map[string]string) error

	// SaveUserPresets writes the user config's [models.presets] section,
	// merged against baseline as SaveUserProviders is.
	//
	// ACP: config/save_presets (exists).
	SaveUserPresets(ctx context.Context, presets, baseline map[string]routing.ModelPreset) error

	// SaveProviderKey stores key as the literal API key of the provider called
	// name in the user config, keeping the entry's other fields.
	//
	// ACP: config/set_provider_key (exists). A marshal client's call is served
	// by the session's service. Its handler writes the provider's full entry;
	// this call writes only the credential.
	SaveProviderKey(ctx context.Context, name, key string) error

	// PreparePresets proposes presets for the models a connected provider
	// reported: cfg's presets with every reported model's preset created or
	// refreshed from the models.dev catalog (read only when cfg's consent
	// allows) and the provenance sidecar. cfg is not changed.
	//
	// ACP: config/prepare_presets (exists).
	PreparePresets(ctx context.Context, cfg config.Config, provider, baseURL string, models []schema.ModelInfo) (map[string]routing.ModelPreset, acpwire.PresetSetup, error)

	// SaveProvenance writes the provenance sidecar PreparePresets returned.
	// A nil or empty provenance writes nothing.
	//
	// ACP: config/save_provenance (exists).
	SaveProvenance(ctx context.Context, provenance json.RawMessage) error
}

// Session is one conversation the TUI drives. It gains its groups' methods
// plan by plan (spec Migration order).
type Session interface {
	// ID is the session's ID.
	//
	// ACP: session/new (exists). Its result carries the ID, and every
	// session/* call sends it back.
	ID() string

	// Status is the session's current status (C5). It is read without
	// blocking: the in-process adapter builds it from the state on each call,
	// and P3 serves it from a mirror.
	//
	// ACP: session/status (exists). A session_status update refreshes it.
	Status() acpwire.SessionStatus

	// Changed fires after anything Status or Work returns may have changed. It
	// is a coalescing signal with a buffer of 1: a reader re-reads Status and
	// never infers what changed. It is closed when a swap replaces the
	// session.
	//
	// ACP: none (the client fires it; P3's mirror fires it after applying a
	// session/update notification).
	Changed() <-chan struct{}

	// Tools names every tool the session's registry holds, sorted. It reports
	// false while the session has no registry: its provider is not built.
	//
	// ACP: session/config (exists). A session_config update refreshes it; the
	// first read subscribes.
	Tools() ([]string, bool)

	// SendNow submits one Send now request: its text goes to the running
	// turn at the next safe boundary, interrupting the operation in flight.
	// The receipt is in Status before SendNow returns.
	//
	// ACP: session/steer (exists), with delivery "now". A marshal client's
	// call is served by the session's service.
	SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error)

	// Sheet is what the session sheet's sections read (C5): the session's
	// own sheet when subagentID is 0, or the sheet of the subagent with that
	// ID anywhere in the session's tree. An ID that names no child session
	// reads as 0, and the result's SubagentID says which sheet was read.
	// Sheet does not block: its database and git values are the ones the
	// last RefreshSheet read.
	//
	// ACP: session/sheet (exists). P3 serves it from a mirror that a sheet
	// update keeps current.
	Sheet(subagentID int64) acpwire.SessionSheet

	// RefreshSheet re-reads the sheet values req names from the database and
	// git, then fires Changed. It blocks on those reads.
	//
	// ACP: session/sheet (exists), sent with the refresh flags.
	RefreshSheet(ctx context.Context, req acpwire.SheetRefresh) error

	// Stack is the transcript the TUI renders (C6): the session's own when
	// subagentID is 0, or that of the subagent with that ID anywhere in the
	// session's tree. Roots are the turns in order. Nodes hold every node of
	// the tree, then the band node, which is not in Roots. An ID that names no
	// child the session keeps reads as an empty snapshot with that SubagentID.
	// Stack does not block in process. Over the wire it makes a call only on a
	// tree's first read and when busy changes.
	//
	// busy says a turn is running, which decides the live beat and the turn's
	// receipt. Until plan 5 moves turns behind Prompt the TUI owns turns, so it
	// passes its own flag, and the tree is built with it whatever subagentID
	// names. P3 keeps it (R4): over the wire the client sends the flag with
	// session/stack_busy when it changes, and every stack of the session is
	// projected with it.
	//
	// ACP: session/stack (exists). A stack_patch update refreshes it.
	Stack(subagentID int64, busy bool) acpwire.StackSnapshot

	// StackNode is one transcript node's wire form with the detail the
	// projection caps or leaves out (C6): a call's whole arguments, output,
	// diff and summary and its step; a beat's narration, reasoning and step;
	// a completion's whole output and report. subagentID 0 reads the
	// session's transcript, and any other ID the transcript of the subagent
	// with that ID anywhere in the session's tree. It fails for an ID that
	// names no child, and for a node the transcript does not hold.
	//
	// ACP: session/stack_node (exists).
	StackNode(ctx context.Context, nodeID string, subagentID int64) (acpwire.StackNodeResult, error)

	// Callers lists what references the symbol a call row changed (C6). It
	// returns nil and no error when the row changed no resolved symbol, when
	// no language server is wired or ready, and when the node is gone. A
	// caller asks once per row and keeps the answer, a nil one included.
	//
	// ACP: session/callers (exists).
	Callers(ctx context.Context, nodeID string) ([]string, error)

	// ReleaseSubagent says the caller stopped reading the subagent's
	// transcript and sheet, as the TUI does when it leaves a drill-in. The
	// next read of either starts afresh. Releasing 0, the session's own,
	// does nothing.
	//
	// ACP: session/stack_unsubscribe (exists), which also releases the
	// subagent's sheet.
	ReleaseSubagent(ctx context.Context, subagentID int64) error

	// Pending lists every open request a person can answer (C7): the
	// session's own, and the approvals of its running subagents. Pending does
	// not block and settles nothing, so it is safe to read while rendering.
	// A pending_requests update keeps the mirror current, but Changed does not
	// fire for every request change on every implementation, so a reader
	// re-reads Pending on every update and tick rather than relying on it.
	//
	// ACP: session/pending (exists). A pending_requests update refreshes it;
	// the first read subscribes.
	Pending() acpwire.PendingRequests

	// AnswerApproval answers the approval with that key. A session or always
	// answer to a Forced approval is a deny. always saves an allow rule to the
	// user config and applies it to the session's config, or adds a session
	// rule when the save fails; session adds a session rule. Both rules land
	// on the session, whichever subagent asked. A key that names no open
	// approval returns CodeRequestSettled.
	//
	// ACP: session/answer_approval (exists).
	AnswerApproval(ctx context.Context, key string, answer acpwire.ApprovalAnswer) error

	// AnswerQuestion answers the question with that key, one answer per
	// question in order. A key that names no open question returns
	// CodeRequestSettled.
	//
	// ACP: session/answer_question (exists).
	AnswerQuestion(ctx context.Context, key string, answers []acpwire.Answer) error

	// AnswerSkillGate answers the skill gate with that key, and records an
	// allow_skill or allow_all choice for the rest of the session. A key that
	// names no open gate returns CodeRequestSettled.
	//
	// ACP: session/answer_skill_gate (exists).
	AnswerSkillGate(ctx context.Context, key string, choice acpwire.SkillGateChoice) error

	// AnswerSudo answers the sudo prompt with that key. The password is
	// handed to the handler as the same slice, and the handler zeroes it; it
	// is never logged, nor returned by any read. A key that names no open
	// prompt returns CodeRequestSettled.
	//
	// ACP: session/answer_sudo (exists). The password travels only in its
	// params, and the client zeroes the caller's slice when the call returns.
	AnswerSudo(ctx context.Context, key string, answer acpwire.SudoAnswer) error

	// ReconcileAccess settles the typed access request that no longer asks
	// for anything, then returns the one the access panel should present, if
	// any. The TUI calls it once per update, so a request surfaces and leaves
	// without a keypress. An implementation that cannot answer access
	// requests settles each one as failed and reports none.
	//
	// ACP: session/pending (exists). The agent settles a request that asks for
	// nothing before each pending_requests update, and this reads the access
	// request the update carries.
	ReconcileAccess(ctx context.Context) (acpwire.AccessRequest, bool)

	// AnswerAccess answers the access request the choice names. The result
	// says how it settled; refresh_required means the tuple moved while the
	// person decided, and the request stays open.
	//
	// ACP: session/answer_access (exists).
	AnswerAccess(ctx context.Context, choice acpwire.AccessChoice) (acpwire.AccessResult, error)

	// Rollback restores the last backup and logs the rollback as an audit
	// row in the transcript, failed or not. It returns the restore's error.
	//
	// ACP: session/rollback (exists).
	Rollback(ctx context.Context) error

	// Work is the session's live work projection (C8), and whether the
	// session serves work at all. The snapshot is the newest one the work
	// stream delivered for the session's conversation, and Changed fires
	// after each. Work does not block. A session that serves no work reports
	// false and a zero snapshot.
	//
	// ACP: session/work_list (exists), the live view. A work_state update
	// refreshes it.
	Work() (acpwire.WorkSnapshot, bool)

	// ListWork lists one page of the session's attention or history view. A
	// cursor older than the work history fails with CodeStaleWorkCursor.
	//
	// ACP: session/work_list (exists), the attention and history views.
	ListWork(ctx context.Context, query acpwire.WorkQuery) (acpwire.WorkPage, error)

	// InspectWork returns the bounded detail of the item with that task key.
	// raw asks for the output as captured, without display cleaning.
	//
	// ACP: session/work_inspect (exists).
	InspectWork(ctx context.Context, taskKey string, raw bool) (acpwire.WorkDetail, error)

	// StopWork asks the owner of the item with that task key to stop it,
	// giving reason. The result describes the request, and comes back with
	// the error when the request is refused.
	//
	// ACP: session/work_stop (exists).
	StopWork(ctx context.Context, taskKey, reason string) (acpwire.WorkStopResult, error)

	// Steer queues text for the running turn to read at its next step. In
	// process it queues even when no turn runs.
	//
	// ACP: session/steer (exists), with the queued delivery. A marshal
	// client's call is served by the session's service.
	Steer(ctx context.Context, text string) error

	// PopSteering removes and returns the oldest queued steering text. It
	// reports false when the queue is empty, and on a closed session.
	//
	// ACP: session/steering_pop (exists).
	PopSteering(ctx context.Context) (string, bool)

	// ClearSteering drops the queued steering text. An accepted Send now
	// request keeps the entry it covers.
	//
	// ACP: session/steering_clear (exists).
	ClearSteering(ctx context.Context) error

	// SetInteraction applies one interaction patch and returns the committed
	// state. A nil field is left alone.
	//
	// ACP: session/set_mode (exists). A marshal client's call is served by
	// the session's service.
	SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error)

	// SetEffort sets the session's effort override for the active route, or
	// clears it for "", and returns when the change applies:
	// EffortAppliesNextStep or EffortAppliesNextTurn.
	//
	// ACP: session/set_effort (exists). A marshal client's call is served by
	// the session's service.
	SetEffort(ctx context.Context, level string) (string, error)

	// SetSudoUnlock turns sudo's own timestamp grace on or off for the
	// session. Status().SudoUnlock reports it.
	//
	// ACP: session/set_sudo_unlock (exists).
	SetSudoUnlock(ctx context.Context, on bool) error

	// CancelSubagent asks the running subagent with that ID to stop. It
	// reports false when no running subagent has the ID, and on a closed
	// session.
	//
	// ACP: session/subagent_cancel (exists).
	CancelSubagent(ctx context.Context, subagentID int64) bool

	// ClearFinishedRun clears a finished run's progress and its event log.
	//
	// ACP: session/sdd_clear (exists).
	ClearFinishedRun(ctx context.Context) error

	// TakeWatchResume takes the watch-resume latch: the watch a resume fire
	// named when it landed too late for the turn that just ended. ok is false
	// when the latch is not set, and on a closed session.
	//
	// ACP: session/watch_resume_take (exists).
	TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool)

	// Prompt runs one turn with params.Text as the user's message and returns
	// when the turn ends. One run at a time: a turn that cannot start returns
	// CodeBusy (a run holds the session), CodeQuiescing (the session is ending)
	// or CodeNoRunner (no runner is built), and a Queued text then goes back on
	// the steering queue. A stopped turn returns StopReasonCancelled and no
	// error; a failed one returns the runner's error.
	//
	// ACP: session/prompt (exists). The text travels as one text block, with
	// queued and watchResume; a marshal client's turn runs on the session's
	// service.
	Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error)

	// Stop stops the session's run: the commands attached to it first, then
	// the run itself. With nothing running it does nothing.
	//
	// ACP: session/stop (exists). session/cancel, a notification, stays for
	// other clients.
	Stop(ctx context.Context, cause string) error

	// Review runs a reviewer subagent over what params names and returns when
	// it ends. It starts, refuses and stops as Prompt does; a stopped review
	// returns no error.
	//
	// ACP: session/review (exists). A marshal client's call is served by the
	// session's service.
	Review(ctx context.Context, params acpwire.ReviewParams) error

	// RunAgent runs the named custom agent with the goal, as a subagent the
	// session shows, and returns when it ends. It starts, refuses and stops as
	// Prompt does; a stopped run returns no error.
	//
	// ACP: session/agent_run (exists). A marshal client's call is served by the
	// session's service.
	RunAgent(ctx context.Context, params acpwire.AgentRunParams) error

	// Suggest asks the session's model for the user's most likely next reply
	// to lastMessage. "" means it has none.
	//
	// ACP: session/suggest (exists). A marshal client's call is served by the
	// session's service.
	Suggest(ctx context.Context, lastMessage string) (string, error)

	// VerifyProposal runs a one-shot subagent that inspects the repository
	// and proposes the commands that build and test it.
	//
	// ACP: session/verify_proposal (exists). A marshal client's call is served
	// by the session's service.
	VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error)

	// StartSwarm runs a swarm over params.Goal, with params' role overrides
	// for this run only, and returns when it ends. It starts, refuses and stops
	// as Prompt does; a stopped run returns no error.
	//
	// ACP: session/swarm_start (exists). A marshal client's call is served by
	// the session's service.
	StartSwarm(ctx context.Context, params acpwire.SwarmParams) error

	// PlanPreflight inspects the plan a run would execute and resolves the
	// verify gate it would use, without running or writing anything.
	//
	// ACP: session/sdd_preflight (exists).
	PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error)

	// StartSDD runs the plan at params.PlanPath with params' strategy, role
	// overrides and verify commands, and returns when it ends. It starts,
	// refuses and stops as Prompt does; a stopped run returns no error. A run
	// its human gate pauses returns CodeHumanGateRequired: the question is in
	// Status, and AnswerSDDGate resumes the run.
	//
	// ACP: session/sdd_start (exists). A marshal client's call is served by
	// the session's service.
	StartSDD(ctx context.Context, params acpwire.SDDParams) error

	// AnswerSDDGate records answer as the user's message and resumes the plan
	// run paused at its human gate, and returns when that run ends again, as
	// StartSDD does.
	//
	// ACP: session/sdd_answer (exists). A marshal client's call is served by
	// the session's service.
	AnswerSDDGate(ctx context.Context, answer string) error

	// StopSDD abandons the plan run paused at its human gate: the question
	// clears and the run can no longer be answered.
	//
	// ACP: session/sdd_stop (exists).
	StopSDD(ctx context.Context) error

	// AuthorPlan runs the plan author over params and returns the candidate it
	// wrote, for review. It starts, refuses and stops as Prompt does; a stopped
	// run returns StopReasonCancelled and no error.
	//
	// ACP: session/sdd_author (exists).
	AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error)

	// ActiveWork is the work an end of the session would stop, as the
	// active-work warning shows it. It is read without blocking; AcceptEnd
	// checks the user's choice against the live membership.
	//
	// ACP: session/end_work (exists). An end_work update refreshes it; the
	// first read subscribes.
	ActiveWork() acpwire.EndWork

	// AcceptEnd accepts an end of the session for params' intent. It checks
	// the displayed IDs and freezes the session's admission in one step: when
	// work the user was not shown is active, it freezes and stops nothing and
	// reports that work in Expanded. Accepting an end already accepted changes
	// nothing.
	//
	// ACP: session/end_accept (exists).
	AcceptEnd(ctx context.Context, params acpwire.EndAcceptParams) (acpwire.EndDecision, error)

	// WaitForEnd stops and joins the accepted end's work. It may be retried: a
	// failure reports the work still outstanding and never latches success.
	//
	// ACP: session/end_wait (exists).
	WaitForEnd(ctx context.Context) error

	// RunExitAnalysis runs the exit postmortem's agent pass with goal, after an
	// accepted exit has settled. The agent grants the pass its access, restores
	// it afterwards, and joins the pass's work before returning.
	//
	// ACP: session/exit_analysis (exists).
	RunExitAnalysis(ctx context.Context, goal string) error

	// Postmortem writes the session's postmortem report and returns the path
	// it wrote.
	//
	// ACP: session/postmortem (exists).
	Postmortem(ctx context.Context) (string, error)

	// Close ends the session: it cancels the run in flight, settles every
	// pending request as shut down, and shuts the session down. Reads keep
	// answering with their final values, and calls fail. Closing a closed
	// session does nothing.
	//
	// ACP: session/close (exists). A marshal client's call is served by the
	// session's service: it ends the conversation and leaves the runtime to
	// the embedding.
	Close(ctx context.Context) error

	// AddNote appends note to the transcript without starting a turn: a
	// system message unless its role is user. It is persisted with the session,
	// and every client sees it.
	//
	// ACP: session/note (new in P3).
	AddNote(ctx context.Context, note acpwire.Note) error

	// SetNotice makes notice the session's banner, replacing any other. A zero
	// SetAt is stamped with the agent's clock.
	//
	// ACP: session/notice_set (exists). Status carries the current notice.
	SetNotice(ctx context.Context, notice acpwire.Notice) error

	// ClearNotice removes the banner only when its category is category, so a
	// success in one subsystem never dismisses another's notice.
	//
	// ACP: session/notice_clear (exists).
	ClearNotice(ctx context.Context, category string) error

	// DismissNotice removes the banner, whatever its category.
	//
	// ACP: session/notice_dismiss (exists).
	DismissNotice(ctx context.Context) error

	// Commands lists the session's slash commands in name order, hidden ones
	// included. It is empty when the session has no command registry.
	//
	// ACP: session/command_list (exists).
	Commands() []acpwire.CommandInfo

	// Command runs the named command's handler over the session with args and
	// returns its output. A command without a handler returns an empty result
	// from the in-process adapter; an adapter that cannot run the command at
	// all — as the ACP manager cannot run a prompt-only or TUI-only entry —
	// returns an error instead. The row actions of a returned doc carry IDs
	// CommandAction runs.
	//
	// ACP: session/command (exists).
	Command(ctx context.Context, name string, args []string) (acpwire.CommandResult, error)

	// CommandAction runs the row action id names, from a recent Command or
	// CommandAction result, and returns its output. An ID the session no
	// longer keeps returns an error.
	//
	// ACP: session/command_action (new in P3).
	CommandAction(ctx context.Context, id string) (acpwire.CommandResult, error)

	// Memories lists the project's own memories, oldest first. It fails with
	// CodeNoDatabase when the session has no database.
	//
	// ACP: session/memory_list (exists).
	Memories(ctx context.Context) ([]acpwire.MemoryEntry, error)

	// DeleteMemory deletes one memory. It fails with CodeNoDatabase when the
	// session has no database.
	//
	// ACP: session/memory_delete (exists).
	DeleteMemory(ctx context.Context, id int64) error

	// Plugins lists the installed plugins: the global lockfile's, then the
	// project lockfile's when the session is trusted.
	//
	// ACP: session/plugins_list (exists).
	Plugins(ctx context.Context) ([]acpwire.PluginEntry, error)

	// ScanPlugin clones source into a staging directory and scans it, without
	// installing anything. The result's token names the staged clone until
	// InstallPlugin or DiscardPluginScan takes it, or the session closes.
	//
	// ACP: session/plugins_install_scan (exists).
	ScanPlugin(ctx context.Context, source string) (acpwire.PluginScan, error)

	// InstallPlugin installs the staged clone token names into scope's store,
	// if its content still hashes as it did when scanned. The staged clone is
	// deleted whatever the outcome.
	//
	// ACP: session/plugins_install_confirm (exists).
	InstallPlugin(ctx context.Context, token, scope string) error

	// DiscardPluginScan deletes the staged clone token names.
	//
	// ACP: session/plugins_install_discard (exists).
	DiscardPluginScan(ctx context.Context, token string) error

	// RemovePlugin deletes an installed plugin and its lockfile entry.
	//
	// ACP: session/plugins_remove (exists).
	RemovePlugin(ctx context.Context, name, scope string) error

	// Skills lists both skill stores, and remembers the listing for LoadSkill
	// and RemoveSkill.
	//
	// ACP: session/skills_list (exists).
	Skills(ctx context.Context) ([]acpwire.SkillEntry, error)

	// LoadSkill injects the listed skill into the session, if the store and
	// the session's index still hold the content the listing saw. It returns
	// the skill's name.
	//
	// ACP: session/skills_load (exists).
	LoadSkill(ctx context.Context, scope, source string) (string, error)

	// InstallSkill installs a skill from source into scope's store, and
	// returns its name.
	//
	// ACP: session/skills_install (new in P3).
	InstallSkill(ctx context.Context, source, scope string) (string, error)

	// RemoveSkill deletes the listed skill's source from its store.
	//
	// ACP: session/skills_remove (exists).
	RemoveSkill(ctx context.Context, scope, source string) error

	// SetSkillAutoload adds name to skills.autoload in the user config, or
	// removes it, and installs the change in the session's live config.
	//
	// ACP: session/skills_autoload (new in P3).
	SetSkillAutoload(ctx context.Context, name string, on bool) error

	// SetSkillGate turns the session's skill load gate on or off. Turning it
	// on clears its decisions.
	//
	// ACP: session/skill_gate_set (new in P3).
	SetSkillGate(ctx context.Context, on bool) error

	// ClearSkillGateDecision clears the gate's sticky decision for skill, so
	// its next load asks again.
	//
	// ACP: session/skill_gate_clear (new in P3).
	ClearSkillGateDecision(ctx context.Context, skill string) error

	// PrepareMCPLogin checks that the OAuth sign-in for the session's MCP
	// server called server can start: the server resolves and the token store
	// opens. Its error is the sentence the client shows.
	//
	// ACP: session/mcp_login (exists).
	PrepareMCPLogin(ctx context.Context, server string) error

	// LoginMCP runs the OAuth sign-in for the MCP server called server and
	// stores its tokens. It calls show once with the authorization URL, which
	// the client opens, and returns when the sign-in ends. A cancelled
	// sign-in's error wraps context.Canceled and stores nothing.
	//
	// ACP: session/mcp_login (exists). show is fed by the call's progress
	// notification.
	LoginMCP(ctx context.Context, server string, show func(url string)) error

	// Config is the session's live config, its secrets masked (C3): each
	// provider's API key and the web search key as strutil.MaskKey renders
	// one, and the desktop CDP URL's userinfo masked. An empty secret is
	// left empty.
	//
	// ACP: session/config (exists). A session_config update refreshes it; the
	// first read subscribes.
	Config() config.Config

	// Layers is the session's config layers, with provenance: the snapshot
	// the last write or startup loaded. The zero value means none was loaded.
	// Its configs' secrets are masked as Config's are (C3).
	//
	// ACP: session/config (exists). A session_config update refreshes it; the
	// first read subscribes.
	Layers() config.Layers

	// ApplyConfig installs cfg as the session's config without saving it or
	// rebuilding the runtime. A manual mode selection survives, as a reload's
	// does.
	//
	// ACP: session/apply_config (exists).
	ApplyConfig(ctx context.Context, cfg config.Config) error

	// ReloadConfig rebuilds the runtime from cfg. It reports false, and does
	// nothing, when the session has no reload path; the error is the
	// rebuild's.
	//
	// ACP: session/reload_config (exists). The result carries the flag with
	// the error.
	ReloadConfig(ctx context.Context, cfg config.Config) (bool, error)

	// ReconnectMCP rebuilds the runtime from the session's current config, so
	// an MCP server that has just been signed in to connects. It clears the
	// provider and config notices first, so a fresh notice the rebuild sets
	// survives. It reports false, and does nothing, when the session has no
	// reload path.
	//
	// ACP: session/mcp_reconnect (exists). The result carries the flag with
	// the error.
	ReconnectMCP(ctx context.Context) (bool, error)

	// SaveProjectConfig writes cfg's project-scoped values to the session's
	// project config, diffed against the session's layers, so that no
	// user-global value is baked into the project file.
	//
	// ACP: session/save_project_config (exists).
	SaveProjectConfig(ctx context.Context, cfg config.Config) error

	// SaveVerifyCommands writes the plan run's verify commands to the
	// session's project config.
	//
	// ACP: session/save_verify_commands (exists).
	SaveVerifyCommands(ctx context.Context, build, test string) error

	// RecentPrompts returns the project's input history, newest first, at
	// most limit entries. A session without a database or a project returns
	// none.
	//
	// ACP: session/prompt_history (new in P3).
	RecentPrompts(ctx context.Context, limit int) ([]string, error)

	// SavePrompt adds text to the project's input history. A session without
	// a database or a project saves nothing and reports no error.
	//
	// ACP: session/prompt_history_save (new in P3).
	SavePrompt(ctx context.Context, text string) error

	// Messages returns the session's transcript messages that query selects.
	// A closed session keeps answering from its final transcript.
	//
	// ACP: session/messages (new in P3).
	Messages(ctx context.Context, query acpwire.MessageQuery) (acpwire.MessagePage, error)

	// FileIndex returns the repository's indexed file paths, for the @
	// completion popup. A session without an index returns none.
	//
	// ACP: session/file_index (new in P3).
	FileIndex(ctx context.Context) ([]string, error)

	// Intelligence returns the doctor's intelligence checks: the index, the
	// embeddings, the language servers and the index watcher.
	//
	// ACP: session/intelligence (new in P3).
	Intelligence(ctx context.Context) ([]acpwire.IntelligenceCheck, error)

	// Plans lists the plans an SDD run can execute, from the configured plans
	// directory. A plan that does not parse is listed with its error.
	//
	// ACP: session/plans (new in P3).
	Plans(ctx context.Context) ([]acpwire.PlanCandidate, error)

	// DiscardCandidate removes one authored plan candidate. It refuses a path
	// that is not a regular file directly inside the plans directory.
	//
	// ACP: session/sdd_discard (new in P3).
	DiscardCandidate(ctx context.Context, path string) error

	// ScaffoldPlan writes the runnable starter plan into the plans directory.
	// It never overwrites a file that is already there.
	//
	// ACP: session/sdd_scaffold (new in P3).
	ScaffoldPlan(ctx context.Context) (acpwire.ScaffoldResult, error)

	// Diff returns the unified diff of query's range in the session's working
	// directory, with three lines of context. It never changes the
	// repository.
	//
	// ACP: session/diff (exists). P3 gives it an optional head.
	Diff(ctx context.Context, query acpwire.DiffQuery) (string, error)

	// ReadFile reads one regular file, named relative to the session's
	// working directory. It refuses a path that leaves the directory.
	//
	// ACP: session/file (exists).
	ReadFile(ctx context.Context, path string) (acpwire.FileContent, error)
}
