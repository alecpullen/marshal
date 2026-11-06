package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/shlex"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/clipboard"
	"marshal/internal/app/config"
	"marshal/internal/app/tui/accesspanel"
	"marshal/internal/app/tui/activeworkpanel"
	"marshal/internal/app/tui/agents"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/app/tui/doctorpanel"
	"marshal/internal/app/tui/gatepanel"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/inspector"
	"marshal/internal/app/tui/livebox"
	"marshal/internal/app/tui/mcpauth"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/modeloptions"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/postmortempanel"
	"marshal/internal/app/tui/presetflow"
	"marshal/internal/app/tui/probe"
	"marshal/internal/app/tui/sddreview"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/steerpanel"
	"marshal/internal/app/tui/theme"
	"marshal/internal/app/tui/trustpanel"
	"marshal/internal/interaction"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
)

// endIntent is one pending interactive end request: what the user asked for,
// held until the runtime accepts the work membership it would stop.
type endIntent struct {
	// kind is acpwire.EndIntentExit or acpwire.EndIntentReplace.
	kind string
	// name is the new-session name for a /new or /clear replacement.
	name string
	// resumeID is the session to resume for a replacement of that kind. It is
	// applied only after acceptance: a declined request must leave the resume
	// target untouched.
	resumeID string
}

// endStage is how far an interactive end has got.
type endStage int

const (
	// endIdle: no end is in progress.
	endIdle endStage = iota
	// endAwaitingDecision: the active-work warning is up, awaiting the user.
	endAwaitingDecision
	// endStopping: the end was accepted and its stop/join pass is running (or
	// last failed and is waiting for a retry).
	endStopping
	// endPostmortem: the work settled and the exit postmortem policy applies.
	endPostmortem
	// endFinishing: the postmortem question is answered; the exit is settling.
	endFinishing
)

// endSettlementTimeout bounds the interactive stop/join pass. It bounds the
// JOIN, never any process's lifetime: a join that times out reports the
// remaining work and offers a retry.
const endSettlementTimeout = 30 * time.Second

// endSettledMsg reports the outcome of one stop/join pass back to the model,
// off the render loop. The pass itself runs in a tea.Cmd so the input stays
// responsive while work is stopping.
type endSettledMsg struct{ err error }

// endAnalysisFinishedMsg reports the outcome of the scoped exit-analysis pass.
// It always quits, whatever the outcome: the analysis runs after the extraction
// has already landed, so a failed annotation must never strand the user at an
// elevated ordinary prompt.
type endAnalysisFinishedMsg struct{ err error }

// postmortemWrittenMsg reports the exit postmortem's extraction+write step back
// to the model. Extraction is real I/O (it walks the audit log and writes a
// file), so it runs in a tea.Cmd rather than on the render loop; analysis
// reports whether the scoped exit-analysis pass is to follow once the report
// landed.
type postmortemWrittenMsg struct {
	path     string
	err      error
	analysis bool
}

const (
	minTerminalWidth  = 80
	minTerminalHeight = 24

	successPulseDuration = 2 * time.Second
	// noticeBannerDuration bounds how long a notice banner stays up before
	// auto-dismissing, so a stale failure does not sit under the transcript
	// forever. The store stamps SetAt at write time, so the TTL applies to
	// every notice regardless of which layer produced it. Same-category
	// success and esc dismiss it earlier.
	noticeBannerDuration = 30 * time.Second

	// settingsBusyMessage is shown when runtime work makes a settings change
	// unsafe to persist. A background command is NOT that work: the
	// conversation's command service survives a reload (only future launches
	// take the new defaults), so a running job no longer blocks a settings
	// write.
	settingsBusyMessage = "Stop the active turn before applying settings."

	// singleModelProfileName is the profile /connect and /models write into when
	// the user picks one model for everything.
	singleModelProfileName = "single"

	// sddCustomPlanPathValue is the picker value emitted by the "Custom plan
	// path..." item in the SDD plan picker. It is not a valid filesystem path,
	// so it cannot collide with an auto-detected plan.
	sddCustomPlanPathValue = "__sdd_custom_path__"

	// sddScaffoldPlanValue is the picker value emitted by the "Write a starter
	// plan" row. It is a sentinel, never a path: the handler writes a template
	// into the plans directory rather than trying to run it.
	sddScaffoldPlanValue = "__sdd_scaffold_plan__"

	// maxRegionOffset bounds how far back a live region can be scrolled.
	// liveregion.Render clamps for display, so an offset past the head is
	// harmless to render — this only stops the stored value running away so
	// far that scrolling back down takes hundreds of wheel events.
	maxRegionOffset = 40
)

// completionRef is one completion event, indexed by the WorkKey it shares with
// the work item it finished.
type completionRef struct {
	node nodeID
	info *acpwire.WireCompletion
}

type Model struct {
	// host and sess are the client seam (P2): the TUI reaches the agent side
	// through them, never through the runtime directly.
	host  acpclient.Host
	sess  acpclient.Session
	input textarea.Model
	// approvalEdit is the isolated editor for one pending approval's command
	// or JSON arguments, or nil while no edit is open. It owns its own
	// textarea, so editing a decision never shares text, cursor, placeholder
	// or paste attachments with the chat composer underneath it.
	approvalEdit *approvalEdit
	ctx          context.Context
	// logger is the logger the TUI logs through. It is nil unless WithLogger
	// sets it; log falls back to a discard logger.
	logger *slog.Logger
	// lspCtx is the context for background LSP reference lookups. It is
	// derived from context.Background() rather than m.ctx because m.ctx is
	// the program's cancellable turn context and can be nil or replaced on
	// a provider-build failure; the LSP queries must outlive a turn but be
	// cancelled on shutdown so a lingering lookup never leaks past exit.
	// lspCancel is called from beginShutdown.
	lspCtx    context.Context
	lspCancel context.CancelFunc
	// calls is the model's call queue (P3 R6): the converted asynchronous
	// calls and the steps that wait behind them. Update starts the head of
	// the queue from the Update goroutine; the call itself runs off it.
	calls []callQueueEntry
	// callInFlight reports whether a converted call is running off the Update
	// goroutine. A second call never starts while one is in flight, which is
	// what keeps the order the calls were made in.
	callInFlight bool
	// callCancel cancels the in-flight call's context, so a quit can unblock
	// a call that respects it.
	callCancel context.CancelFunc
	// callQuitWaiting records that a quit arrived while a call was in flight:
	// the quit finishes when that call's result is handled.
	callQuitWaiting bool
	busy            bool
	// end is the pending interactive end intent, non-nil from the moment the
	// user asks to exit or replace the conversation until the warning is
	// declined. It holds the target (a new-session name or a resume ID) so
	// nothing about that target is mutated or announced before acceptance.
	end *endIntent
	// endStage is how far the end has got (see endStage).
	endStage endStage
	// endSnapshot is the most recent membership the warning rendered, used to
	// refresh the panel from runtime events and as the displayed set a
	// confirmation submits.
	endSnapshot acpwire.EndWork
	// endDisplaced remembers the dock panel the warning displaced, so declining
	// re-opens exactly the surface the user was working in. A panel the runtime
	// resolved during settlement is never restored: acceptance clears it.
	endDisplaced dock.Panel
	// endWarning is the warning panel this end opened. Holding the instance is
	// what lets a refresh keep the same surface — and the same key routing —
	// instead of substituting a second panel the user never opened.
	endWarning dock.Panel
	// endAnswered is set once the user has answered the exit postmortem
	// question. It is the latch that keeps a repeated quit request from
	// re-opening a question that was already answered, and it keeps an
	// in-flight analysis pass from being interrupted by a fresh prompt.
	endAnswered        bool
	openConnectOnStart bool
	homeDir            string
	workDir            string
	agentCancel        context.CancelFunc
	// syncedAccessMode is the session mode the last reconciliation adopted.
	// The sync is edge-triggered on it: a mode the model already reflects is
	// not re-asserted over a value another path set, while a mode the session
	// changed underneath the model (a grant, a reload) still propagates.
	syncedAccessMode string
	approvalModel    *approvalModel
	questionModel    *questionModel
	skillGateModel   *skillGateModel
	sudoAuthModel    *sudoAuthModel

	// F18: editor completions. cmdPopup is fed by the commands registry
	// (triggered by `/` at position 0) and filePopup is fed by the repo
	// file index (triggered by `@` at a word start). fileIndex holds the
	// repo file paths used to build filePopup, loaded once at startup via
	// WithFileIndex (or lazy on first `@` keystroke — see
	// updateCompletionPopups). lastInputForPopups caches the value seen
	// by updateCompletionPopups so non-key events (mouse, paste, ticks)
	// don't re-evaluate and clobber the popup's index/offset.
	// completionSuppressed is set by Esc and cleared when the input is
	// cleared, on accept, or on submit/reset. This keeps the popup dismissed
	// while editing the same trigger after Esc.
	cmdPopup             *completionPopup
	filePopup            *completionPopup
	setPopup             *completionPopup
	fileIndex            []completionItem
	fileIndexLoaded      bool
	lastInputForPopups   string
	completionSuppressed bool
	// cmdArgMode arms argument completion right after a command is
	// accepted from the popup. While armed and the input still carries
	// the accepted "/<cmd> " prefix, commandTrigger keeps firing so
	// argument completions stay available. Anything that drops the
	// prefix — or submit, history recall, an empty input — disarms it.
	// Ordinary words like "run tests" never trigger anything.
	cmdArgMode   bool
	cmdArgPrefix string
	// Prompt history (project-scoped), newest first. histIdx == -1 means
	// "not browsing history, editing own draft"; draft stashes in-progress
	// text while browsing.
	history []string
	histIdx int
	draft   string
	setReg  *settings.Registry
	// configSavePending means the session's config contains a change that
	// could not be persisted. An otherwise unchanged /set retries the full
	// config.
	configSavePending bool

	// diagnosticCount caches the number of semantic config diagnostics
	// found by Diagnose. Refreshed at construction and after every
	// successful persist. Zero means no known issues.
	diagnosticCount int

	// suggestion is the active ghost-text next-prompt suggestion shown in
	// the input box after a turn completes ("" = none). suggestionDismissed
	// is set by Esc so a dismissed suggestion is not resurrected until the
	// next turn completes. Both are cleared when a new turn starts.
	suggestion          string
	suggestionDismissed bool
	// suggestionGen is a generation counter bumped on every keystroke and
	// turn start; a late suggestion result from an older generation is
	// discarded.
	suggestionGen int

	cancelling bool
	// watchResumeSeq is the last watch-resume sequence the model acted on, so
	// one fire wakes the session once.
	watchResumeSeq uint64
	// workspaceSeen is the workspace the sheet's base ref last followed.
	workspaceSeen acpwire.Workspace
	// steeringComposer is the temporary composer currently holding the dock,
	// or nil. It is identified by its ComposerID so a late message from a
	// replaced composer can never act on the new one.
	steeringComposer *steerpanel.Panel
	// composerID is the active composer's ID; composerSessionID/composerTurnID
	// are the conversation and turn it was bound to at open time. A
	// submission whose binding no longer matches is stale and rejected.
	composerID        string
	composerSessionID string
	composerTurnID    string
	// composerDisplaced is the one panel the composer displaced. It is not a
	// general panel stack: the composer is the only surface that suspends
	// another, so a single retained slot is enough.
	composerDisplaced dock.Panel
	// composerDisplacedAccess is the canonical access-request ID that panel
	// represented, and composerDisplacedPrompt the request when a pending
	// prompt (approval/question/skill/sudo) was displaced. Restoring requires
	// the same identity: a request cancelled by interruption, or a prompt that
	// settled while the composer was up, must retire rather than resurrect.
	composerDisplacedAccess string
	composerDisplacedPrompt promptRef
	// composerInputFocused is the base textarea's focus state before the
	// composer took over, restored on close so Esc puts the user back where
	// they were rather than stealing or leaving focus arbitrarily.
	composerInputFocused bool

	// endAnalyzing is set when the exit flow dispatched the scoped exit-analysis
	// pass through the end controller. The pass reports back through
	// endAnalysisFinishedMsg, which always quits: analysis can never return the
	// user to an ordinary prompt.
	endAnalyzing bool
	// endExtracting is set while the exit postmortem's extraction+write step is
	// running off the render loop. A repeated quit request during it must not
	// quit ahead of the report: the extraction is best-effort, but it is the one
	// chance to record the session, so it is allowed to finish first.
	endExtracting bool

	// New Layout State
	rawWidth  int // unclamped terminal dimensions (gate check)
	rawHeight int
	width     int // clamped to ≥ minTerminalWidth/Height (internal geometry)
	height    int
	// leftWidth is the width of the single content column. It always equals
	// width; the name survives from when a side rail took the remainder.
	leftWidth int
	// mouseReleased is the session-only Ctrl+S override that hands the mouse
	// back to the terminal so click-drag text selection works without the
	// terminal's modifier key. Not persisted; [tui].mouse_capture is the
	// durable setting. Capture and native selection are mutually exclusive —
	// the terminal cannot deliver events to both — so this is a toggle rather
	// than something the two features can share.
	mouseReleased bool
	viewport      viewport.Model

	// Viewport dirty tracking.
	lastTranscriptHash uint64
	// expandAll is Ctrl+G: every node draws expanded. expanded holds the
	// per-node choices made with Enter or a click, cleared whenever Ctrl+G
	// flips. A row without one follows its beat, and a beat follows expandAll.
	expandAll bool
	expanded  map[nodeID]bool
	// Browse mode (Esc): a cursor over the rendered nodes. browseItems is
	// rebuilt on every full refresh.
	browsing   bool
	osc52Noted bool
	// copyWriter is the local clipboard helper; copyRemote reports an SSH
	// session, where that helper would write the remote machine's clipboard.
	// With neither usable, copies go to the terminal over OSC 52.
	copyWriter  clipboard.Writer
	copyRemote  func() bool
	cursor      nodeID
	browseItems []browseItem
	// wire holds the last refresh's snapshot without the band, indexed by
	// node ID. Browse actions read node fields from it.
	wire map[string]*acpwire.WireNode
	// flash is a transient status-line message (see flash.go).
	flash      string
	flashUntil time.Time
	// regionOffset holds the per-region body scroll offset for bounded live
	// regions (see internal/app/tui/liveregion). Pruned on every
	// refreshViewport, so a finished region's entry does not leak.
	regionOffset map[nodeID]int
	// regionRows is the high-water mark for each bounded live region: the
	// tallest it has rendered so far. liveregion.Render is pure and cannot
	// remember, and the body genuinely shrinks (SubagentActivityTail
	// switches between streamed reasoning and audit summaries), so without
	// this the card oscillates. Pruned with regionOffset.
	regionRows map[nodeID]int
	// callers caches reference lookups per tool row. A present-but-empty
	// entry is a negative result and must not be re-queried.
	callers      map[nodeID][]string
	callersAsked map[nodeID]bool
	// details caches StackNode detail for expanded rows the wire cut, by wire
	// node ID, each for the node version it was fetched at. detailWant holds
	// what renders asked for since the last update, and detailAsked the
	// version in flight per node.
	details     map[string]cachedDetail
	detailWant  map[string]detailWant
	detailAsked map[string]string
	// renderCache holds each settled transcript block's rendered output,
	// keyed by node identity and valid while its payload version, width and
	// render inputs are unchanged. Only live nodes re-render on a spinner
	// tick. Pruned to the nodes seen on the latest refresh.
	renderCache map[nodeID]cachedNode
	// nodeRegions maps content-line ranges in the transcript viewport to the
	// node occupying them, rebuilt every time refreshViewport rebuilds
	// blocks. Rows inside a step record their own narrower ranges. See
	// click.go.
	nodeRegions []nodeRegion
	// blockSpans are the content-line ranges of every transcript block, in
	// order, under the keys a text selection anchors to. contentLines is the
	// same content split into lines. Both are rebuilt with nodeRegions.
	blockSpans   []blockSpan
	contentLines []string
	// sel is the transcript text selection, nil when there is none; press
	// is a left press waiting for its release, and drag the pointer state
	// of a drag in progress. See selection.go.
	sel   *selection
	press *tea.Mouse
	drag  dragState
	// viewStack is the subagent drill-down stack: when non-empty, the
	// transcript viewport renders the top subagent's transcript instead of
	// the orchestrator's. Each entry is the subagent's status view as it was
	// at drill-in. Pushed by clicking a subagent card (see browse.go),
	// popped by Esc (see keypress.go). Depth is ≤1 today (nested agent.run
	// is forbidden) but the stack is kept general.
	viewStack []acpwire.Subagent
	// rollbackArmed is the first half of Ctrl+R's arm-then-confirm. Cleared
	// by any other keypress; see handleKeypress.
	rollbackArmed bool
	// ctrlCArmedAt/ctrlCArmedFor record a first Ctrl+C press. A second press
	// for the same action within ctrlCWindow fires it; any other keypress
	// disarms. Never read directly for display: use ctrlCArmed, which
	// accounts for expiry.
	ctrlCArmedAt   time.Time
	ctrlCArmedFor  ctrlCAction
	viewportFollow bool

	// Connect panel (docked; opened by /connect, /models, Ctrl+P).
	connectModel *connect.Model
	discovered   map[string][]schema.ModelInfo
	// modelsProbeGen records the newest /models discovery attempt dispatched
	// per provider, so an older attempt's completion cannot overwrite a newer
	// one. It persists for the model's lifetime — closing and reopening the
	// picker does not restart the count.
	modelsProbeGen map[string]uint64

	// Picker modal (opened by commands like /rewind, /branches, /mode).
	// The picker itself is hosted in m.dock; pickerCommand records which
	// command opened it so PickedMsg can dispatch correctly.
	pickerCommand string
	// resumeSession is non-empty when the user asked to resume a different
	// session; the app.Run program runner reads it from the final model.
	resumeSession string
	dock          dock.Host

	// mcpAuthServer is the name of the MCP server whose OAuth flow is in
	// flight, set while the authorization panel is docked. It scopes the
	// success/cancel message to the right server when AuthDoneMsg arrives.
	mcpAuthServer string
	// openBrowser launches the authorization URL for /mcp auth. New selects
	// mcpauth.OpenBrowser when no option supplies one; tests inject a stub so
	// an executed flow never opens a real browser.
	openBrowser func(url string) error

	// doctorSecret is the masked key field of an in-progress /doctor repair,
	// nil when no repair is in progress. It owns its own text input: the chat
	// composer's text, cursor, placeholder and paste attachments are never
	// touched while it is up.
	doctorSecret *doctorSecret

	spinner      Spinner
	spinnerFrame string
	// turnStartedAt is when the current agent turn began, used for the
	// pinned turn spinner's elapsed clock. Zero while idle. Distinct from
	// session.Activity.StartedAt, which is per-phase and resets between
	// phases — the spinner must survive those gaps.
	turnStartedAt  time.Time
	successPulse   bool
	successPulseAt time.Time
	now            func() time.Time

	// tasksPanel is the open Work list panel, kept so a second open can tell
	// it apart from other docked panels. The zero value (nil) means the panel
	// is closed.
	tasksPanel *tasksPanelModel
	// panelSeq is the monotonic source of dock panel IDs. A new panel takes
	// the next value; IDs are never derived from a label and never reused, so
	// a result from a closed panel can be told apart from its replacement.
	panelSeq uint64

	// boxMemo is this frame's box layout, planned once in viewString; nil
	// outside viewString. It lives only on that call's model copy.
	boxMemo *livebox.Layout

	// dockFramed records whether the docked panel's last view was framed, so
	// consoleAbove can tell a joined panel from one that drew nothing.
	dockFramed bool

	// band is the pinned band's content: todo progress with its stale
	// signal, and the running cards. It comes from the stack's band node;
	// the now bar renders it.
	band acpwire.WireBand

	// heldSince is when the current held window opened: a finished item that
	// ended after it keeps its tray row until the next prompt. It is set when
	// the model is built and again at each turn start.
	heldSince time.Time
	// planDismissed is the fingerprint of the finished list seen at the last
	// turn start. The tray leaves out a finished list with that fingerprint,
	// so a finished plan is one row until the next prompt.
	planDismissed string
	// completions indexes the completion events by WorkKey, rebuilt on every
	// refresh. A held row takes its outcome and summary from its event, so the
	// tray and history always agree.
	completions map[string]completionRef

	// workSnapshot is the newest immutable Work projection. The bar only
	// reads it: it never mutates the snapshot and never fills a gap from the
	// agent, job or watch owners.
	workSnapshot acpwire.WorkSnapshot
	// workPrevious holds the previous progress observation per TaskKey, kept
	// so a card can derive a measured rate and ETA from two observations.
	workPrevious map[string]*acpwire.WorkProgress
	// workExpanded names the single card the reader opened. Empty is the
	// collapsed default; a card whose item settles or disappears clears it.
	workExpanded string
	// rowBeat maps each row of a beat to the beat, from the last build, so
	// a completion marker can open the beat that hides its card past the row
	// cap before jumping to it.
	rowBeat map[nodeID]nodeID
	// sheetPanel is the open Ctrl+B session sheet, kept for the same reason.
	sheetPanel *sessionsheet.Panel

	// connectReturnToSettings and connectReturnFilter track whether the
	// connect wizard was opened from the settings browser, so completing
	// or canceling the wizard returns to the browser with the same filter.
	connectReturnToSettings bool
	connectReturnFilter     string

	// pastes holds condensed paste attachments for the current input.
	// They are expanded into fenced blocks on submit and cleared whenever
	// the input is reset.
	pastes []pasteAttachment

	// pendingModelOptions holds a config candidate saved while the runner
	// or background jobs are active. It is flushed when the model becomes
	// idle and applies via the configured reloader.
	pendingModelOptions *pendingModelOptionsState

	// pendingRun holds the pre-flight state while the cast list panel is
	// open. It is set by openRunPreflight and consumed by the castlist
	// StartMsg/CancelMsg handlers.
	pendingRun *pendingAgentRun
}

// pendingAgentRun captures the run waiting for the user to confirm it in the
// cast list.
type pendingAgentRun struct {
	goal string
	// kind is "sdd" or "swarm", set by openRunPreflight.
	kind string
	// verifyBuild/verifyTest hold commands proposed by the offer-to-fill
	// flow. When set, they are persisted on confirm and used for this run.
	verifyBuild string
	verifyTest  string
}

type Option func(*Model)

// WithOpenConnectOnStart opens the connect panel as the TUI starts. Used
// for first run: provider setup is the same flow on the first run and on
// the hundredth.
func WithOpenConnectOnStart() Option {
	return func(m *Model) { m.openConnectOnStart = true }
}

func WithHomeDir(homeDir string) Option {
	return func(m *Model) {
		m.homeDir = homeDir
	}
}

// WithOpenBrowser overrides the browser launcher used by /mcp auth and the
// connect wizard's OAuth sign-in. Production omits it, which selects
// mcpauth.OpenBrowser; tests inject a recorder so an executed authorization
// command never opens a real browser.
func WithOpenBrowser(open func(url string) error) Option {
	return func(m *Model) { m.openBrowser = open }
}

// Host returns the client host the model was built with.
func (m Model) Host() acpclient.Host { return m.host }

// Session returns the client session the model was built with.
func (m Model) Session() acpclient.Session { return m.sess }

func WithWorkingDir(workDir string) Option {
	return func(m *Model) {
		m.workDir = workDir
	}
}

// WithClipboard sets the local clipboard helper used by copy, and how to tell
// that the session is remote. Without it copies go over OSC 52 only.
func WithClipboard(w clipboard.Writer, remote func() bool) Option {
	return func(m *Model) {
		m.copyWriter = w
		m.copyRemote = remote
	}
}

// WithFileIndex seeds the F18 @file completion popup with a snapshot of
// the repo's file paths. Eager seeding is preferred when the model is
// constructed in a context that already holds a db.DB (avoids a
// per-keystroke DB hit on the first `@`); if the model is constructed
// without it, the popup falls back to a lazy load on the first `@`
// keystroke (see updateCompletionPopups).
func WithFileIndex(paths []string) Option {
	return func(m *Model) {
		m.fileIndex = buildFileIndexItems(paths)
		m.fileIndexLoaded = true
	}
}

func buildFileIndexItems(paths []string) []completionItem {
	items := make([]completionItem, 0, len(paths))
	for _, p := range paths {
		if containsRunnerWhitespace(p) {
			continue
		}
		items = append(items, completionItem{Text: p, Kind: completionFile})
	}
	return items
}

func containsRunnerWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t\n\r\f")
}

// WithContext carries the program's context. It is the parent of the session's
// event subscriptions and of each turn.
func WithContext(ctx context.Context) Option {
	return func(m *Model) {
		m.ctx = ctx
	}
}

// WithLogger sets the logger the TUI logs through. Without it, the model logs
// to a discard logger.
func WithLogger(l *slog.Logger) Option {
	return func(m *Model) {
		m.logger = l
	}
}

// log is the model's logger, never nil.
func (m *Model) log() *slog.Logger {
	if m.logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return m.logger
}

// note appends text to the transcript as a plain system note, through the
// session. A dropped note is the one failure the user cannot see, so it is
// logged at warn rather than debug.
func (m *Model) note(text string) {
	if err := m.sess.AddNote(m.ctx, acpwire.Note{Text: text, ContentType: acpwire.ContentTypePlain}); err != nil {
		m.log().Warn("note dropped", "error", err)
	}
}

func relPath(workingDir, path string) string {
	rel, err := filepath.Rel(workingDir, path)
	if err != nil {
		return path
	}
	return rel
}

// applyNewConfig installs cfg as the live session config and invalidates
// anything derived from it. The footer mode is re-seeded from the new
// config so a /settings or /set approval-mode change is reflected in the
// status line (the policy engine is rebuilt from the same value by the
// runtime reload).
func (m *Model) applyNewConfig(cfg config.Config) {
	// Rebase rather than assign: the session owns the mode override, so an
	// unrelated reload must not silently revert a manual selection or an
	// approved grant. ApplyConfig installs the new config with the remaining
	// override applied and preserves the system-access flag; its error is the
	// rebase's, which this path has never surfaced.
	_ = m.sess.ApplyConfig(m.ctx, cfg)
	m.configViewsChanged(cfg)
}

// configViewsChanged resets what the TUI caches from the config, and reloads
// the theme: applyNewConfig's TUI half.
func (m *Model) configViewsChanged(cfg config.Config) {
	m.setReg = nil
	m.setPopup = nil
	m.lastInputForPopups = ""
	m.completionSuppressed = false
	loadTheme(cfg.TUI)
}

// refreshDiagnostics runs config.Diagnose against the current session config
// and caches the number of diagnostics found. Real per-layer provenance
// comes from the session's layers; when the snapshot is the zero value (e.g.
// tests that don't construct via app.Run), every Diagnostic.Source reads as
// "default".
func (m *Model) refreshDiagnostics() {
	ds := config.Diagnose(m.sess.Config(), m.sess.Layers())
	// Info-severity entries are hoist notices, not problems; the badge
	// counts only errors and warnings.
	n := 0
	for _, d := range ds {
		if d.Severity != config.SeverityInfo {
			n++
		}
	}
	m.diagnosticCount = n
}

// userHome returns the home directory used to locate the user-global
// config: the injected home (WithHomeDir) when set, else the OS home.
// Tests inject a temp home; deriving paths from os.UserHomeDir() directly
// would let test runs write the developer's real ~/.config/marshal.
func (m *Model) userHome() (string, error) {
	if m.homeDir != "" {
		return m.homeDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return home, nil
}

// persistAndReload saves cfg to the project config file, asks the runtime
// to reload it, and applies the outcome to TUI state. It returns the save
// error or, when saving succeeded, the reload error (nil on full success).
// On any error the in-memory change is kept (and configSavePending set for
// save failures) so the user can fix the underlying problem and retry
// without losing their edit. Messaging is the caller's job.
func (m *Model) persistAndReload(cfg config.Config) (saveErr, reloadErr error) {
	if err := m.sess.SaveProjectConfig(m.ctx, cfg); err != nil {
		m.applyNewConfig(cfg)
		m.configSavePending = true
		return err, nil
	}
	m.configSavePending = false
	// reloadAgentRuntime may install cfg before reporting a cleanup
	// error; invalidate config-derived state before attempting it.
	m.setReg = nil
	reloaded, err := m.sess.ReloadConfig(m.ctx, cfg)
	if err != nil {
		// The runtime has already swapped cfg before cleanup can fail.
		// Keep all TUI-derived state aligned with that live config.
		m.applyNewConfig(cfg)
		return nil, err
	}
	if reloaded {
		m.afterRuntimeReload()
	}
	m.applyNewConfig(cfg)
	m.refreshDiagnostics()
	return nil, nil
}

// afterRuntimeReload runs the recovery steps that become valid once the
// runtime has successfully rebuilt itself from a new config: the stale
// provider-error banner no longer applies (a rebuilt runner proves
// provider construction worked). A runner a reload builds is used from the
// next turn: the session reads the runtime's current runner on each call.
func (m *Model) afterRuntimeReload() {
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeConfig)
}

// settingsRegistry returns the cached /set registry, rebuilt after a config
// change invalidates it.
func (m *Model) settingsRegistry() *settings.Registry {
	if m.setReg == nil {
		m.setReg = settings.BuildRegistry(m.sess.Config())
	}
	return m.setReg
}

func (m *Model) handleSetCommand(args []string) {
	sys := func(text string) {
		m.note(text)
	}

	switch len(args) {
	case 0:
		m.openSettingsBrowser("")
		return
	case 1:
		key := args[0]
		reg := m.settingsRegistry()
		kind, current, options, err := reg.Describe(key)
		if err != nil {
			m.openSettingsBrowser(key)
			return
		}
		line := fmt.Sprintf("%s = %s (%s)", key, current, kind)
		if len(options) > 0 {
			line += " · options: " + strings.Join(options, ", ")
		}
		sys(line)
		return
	default:
		key, value := args[0], strings.Join(args[1:], " ")
		if reason := m.settingsBlockReason(); reason != "" {
			sys(reason)
			return
		}
		reg := m.settingsRegistry()
		change, err := reg.Apply(key, value)
		if err != nil {
			m.setReg = nil
			sys("✗ " + key + ": " + err.Error())
			return
		}
		if !change.Changed {
			if !m.configSavePending {
				sys(fmt.Sprintf("• %s unchanged (%s)", key, change.NewValue))
				return
			}
		}
		// Global-target branch: if the field is marked writeGlobal, write
		// the single value to the user config instead of the project config.
		if f, ok := reg.Lookup(key); ok && settings.FieldWriteGlobal(f) && settings.FieldTomlPath(f) != "" {
			tomlPath := settings.FieldTomlPath(f)
			value, ok := config.LookupPath(reg.Config(), tomlPath)
			if !ok {
				sys(fmt.Sprintf("✗ %s: cannot look up value at %s", key, tomlPath))
				return
			}
			home, err := m.userHome()
			if err != nil {
				// Fall back to persistAndReload if home is unavailable.
				saveErr, reloadErr := m.persistAndReload(reg.Config())
				m.reportSetResult(key, change, saveErr, reloadErr)
				return
			}
			if err := m.host.SaveUserValue(m.ctx, tomlPath, value); err != nil {
				m.applyNewConfig(reg.Config())
				m.configSavePending = true
				sys(fmt.Sprintf("✗ %s applied in session, but user config save failed: %v", key, err))
				return
			}
			m.configSavePending = false
			m.setReg = nil
			reloaded, err := m.sess.ReloadConfig(m.ctx, reg.Config())
			if err != nil {
				m.applyNewConfig(reg.Config())
				sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, err))
				return
			}
			if reloaded {
				m.afterRuntimeReload()
			}
			m.applyNewConfig(reg.Config())
			m.refreshOpenSettingsBrowser()
			userPath := m.sess.Status().UserConfigPath
			if !change.Changed {
				sys(fmt.Sprintf("✓ %s persisted · %s", key, relPath(home, userPath)))
			} else {
				sys(fmt.Sprintf("✓ %s: %s → %s · %s", key, change.OldValue, change.NewValue, relPath(home, userPath)))
			}
			return
		}

		saveErr, reloadErr := m.persistAndReload(reg.Config())
		m.reportSetResult(key, change, saveErr, reloadErr)
		return
	}
}

// reportSetResult emits the system message for a /set command outcome.
// Shared by the project-save and global-target branches.
func (m *Model) reportSetResult(key string, change settings.Change, saveErr, reloadErr error) {
	sys := func(text string) {
		m.note(text)
	}
	m.refreshOpenSettingsBrowser()
	st := m.sess.Status()
	switch {
	case saveErr != nil:
		sys(fmt.Sprintf("✗ %s applied in session, but save failed: %v", key, saveErr))
	case reloadErr != nil:
		sys(fmt.Sprintf("✗ %s saved, but live reload failed: %v", key, reloadErr))
	case !change.Changed:
		sys(fmt.Sprintf("✓ %s persisted · %s", key, relPath(st.WorkingDir, st.ProjectConfigPath)))
	default:
		sys(fmt.Sprintf("✓ %s: %s → %s · %s", key, change.OldValue, change.NewValue, relPath(st.WorkingDir, st.ProjectConfigPath)))
	}
}

// provenanceForPath renders the one-line provenance summary for a TOML
// path, or "" when layers are unavailable.
func (m *Model) provenanceForPath(tomlPath string) string {
	layers := m.sess.Layers()
	if reflect.ValueOf(layers).IsZero() {
		return ""
	}
	p := layers.ProvenanceOf(tomlPath)
	if p.SetBy == config.LayerDefault && p.Overrides == config.LayerDefault {
		return "set by: built-in default"
	}
	s := "set by: " + p.SetBy.String()
	if p.Overrides != config.LayerDefault {
		s += " · overrides: " + p.Overrides.String()
	}
	return s
}

func (m *Model) openSettingsBrowser(query string) {
	opts := []settings.BrowserOption{
		settings.WithProvenance(m.provenanceForPath),
		settings.WithHost(m.host),
		settings.WithSession(m.sess),
	}
	if userConfig := m.sess.Status().UserConfigPath; userConfig != "" {
		opts = append(opts, settings.WithUserConfigPath(userConfig))
	}
	browser := settings.NewBrowser(
		m.sess.Config(),
		query,
		opts...,
	)
	// The session's config may already hold an unsaved change left behind by
	// a previous failed save (browser or /set) — applyNewConfig keeps it
	// applied in-memory so the edit isn't lost, which means this fresh
	// panel's baseline is cloned from that already-advanced value and diffs
	// empty on construction. Seed savePending so a repeated commit still
	// retries persistence instead of silently no-op'ing (see
	// BrowserPanel.SetSavePending and flushChanges).
	browser.SetSavePending(m.configSavePending)
	m.dock.Open(browser)
}

func (m *Model) openAgentsRoster(arg string) {
	roster := agents.NewRosterPanel(
		m.sess.Config(),
		m.sess,
		arg,
		runNowDispatch,
	)
	roster.SetQuota(m.sess.Status().Quota)
	m.dock.Open(roster)
}

// openTrustPanel opens the modal folder-trust question for the directory the
// host reports as pending, and reports whether it did. The panel answers
// through the seam: DecideTrust records the decision and, after a trust, the
// app restarts with the project config in force. A refusal here means the
// wiring is broken, so it is logged rather than shown.
func (m *Model) openTrustPanel() bool {
	// The closure runs after Update has returned, so it captures the host,
	// the context and the logger by value, never the model.
	host, ctx, log := m.host, m.ctx, m.log()
	dir := host.TrustPrompt()
	if dir == "" {
		return false
	}
	m.dock.Open(trustpanel.New(dir, func(decision acpwire.TrustDecision) {
		if err := host.DecideTrust(ctx, dir, decision); err != nil {
			log.Warn("trust decision was not recorded", "error", err)
		}
	}))
	return true
}

// runAgentMsg asks the live model to start a Run-now. The roster's dispatch
// captures nothing, so the model that handles this message is the live one.
type runAgentMsg struct{ agent, goal string }

// runNowDispatch is the roster's Run-now dispatch. It captures nothing: the
// live model starts the run when it handles runAgentMsg, so the turn's busy
// flag, cancellation and transcript all land on the model the user sees.
func runNowDispatch(agentName, goal string) tea.Cmd {
	if agentName == "" {
		return nil
	}
	return func() tea.Msg {
		return runAgentMsg{agent: agentName, goal: goal}
	}
}

// refreshOpenSettingsBrowser rebuilds an open settings browser from the
// current session config so that a preceding /set (or other external config
// change) isn't reverted by the next browser edit/save. The filter query is
// preserved; cursor and drill stack are reset because field closures are bound
// to the old registry's state.
func (m *Model) refreshOpenSettingsBrowser() {
	if browser, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
		m.openSettingsBrowser(browser.FilterValue())
	}
}

// inputPromptWidth is the number of cells the input textarea's prompt
// function reserves on every display row: "❯ " on row 0 and two spaces on
// wrapped rows, both visible width 2. The suggestion ghost adds it to the
// cursor's column offset within a row to get the column in the rendered
// frame (see ghostPosition in view.go).
const inputPromptWidth = 2

// newComposerTextarea builds a textarea configured exactly like the chat
// composer: the ❯ prompt on the first line, a two-cell gutter on continuation
// lines, shift+enter for a newline, dynamic height and the theme's styles.
// Callers set Placeholder, CharLimit and Width for their own surface; an
// isolated editor therefore looks and types like the composer it sits over.
func newComposerTextarea() textarea.Model {
	input := textarea.New()
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.MinHeight = 1
	input.DynamicHeight = true
	input.SetHeight(1)
	input.SetWidth(80)

	// The prompt is rendered inside the textarea on every display line via
	// SetPromptFunc. promptWidth=2 reserves two cells on each line: line 0
	// shows "❯ " (width 2) and continuation/wrapped lines show "  " (two
	// spaces, width 2), so wrapped text aligns under the first line's text
	// column. "❯" is rune-width 1, so "❯ " and "  " are both visible width 2.
	input.SetPromptFunc(inputPromptWidth, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	// Re-apply width so the prompt's reserved cells are subtracted from
	// the text wrap width.
	input.SetWidth(80)

	km := textarea.DefaultKeyMap()
	km.InsertNewline.SetKeys("shift+enter")
	input.KeyMap = km
	input.Focus()

	textStyle := lipgloss.NewStyle().Foreground(activeTheme.FGDefault)
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Text = textStyle
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(dimColor)
	styles.Blurred.Text = textStyle
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(dimColor)

	// The prompt string from SetPromptFunc is re-rendered through
	// computedPrompt() (the Prompt style). Set it to a plain style so the
	// prompt keeps its default color — the ❯ glyph is visually distinct on
	// its own. (If you want the ❯ coral, set these to
	// lipgloss.NewStyle().Foreground(accentColor).Bold(true) instead.)
	styles.Focused.Prompt = lipgloss.NewStyle()
	styles.Blurred.Prompt = lipgloss.NewStyle()

	// CursorLine is the style wrapping the active text row. The upstream
	// default adds a dark background ("0") that extends across the full
	// line width (including padding spaces), producing a dark bar behind
	// the cursor line. We override it to have no background so the input
	// area stays on a single clean line.
	styles.Focused.CursorLine = textStyle
	styles.Blurred.CursorLine = lipgloss.NewStyle().Foreground(compat.AdaptiveColor{
		Light: activeTheme.FGMuted,
		Dark:  activeTheme.FGDefault,
	})

	// EndOfBuffer is the filler row(s) below the last line of text. The
	// upstream default applies a dark foreground ("0") that can leave a
	// faint artifact row when the textarea height is 1.
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()

	// The textarea keeps its virtual cursor (the v2 default); the coral
	// colour fills the rendered cursor block.
	styles.Cursor.Color = accentColor
	styles.Cursor.Blink = true
	input.SetStyles(styles)
	return input
}

func New(host acpclient.Host, sess acpclient.Session, opts ...Option) Model {
	if host == nil || sess == nil {
		panic("tui.New: nil client host or session")
	}

	m := Model{
		host:           host,
		sess:           sess,
		ctx:            context.Background(),
		viewport:       viewport.New(),
		spinner:        NewSpinner(),
		now:            time.Now,
		viewportFollow: true,
		discovered:     map[string][]schema.ModelInfo{},
	}
	for _, opt := range opts {
		opt(&m)
	}
	st := m.sess.Status()
	// One session-config read for the whole construction: the reads below
	// seed theme, discovery and history from the live config.
	cfg := m.sess.Config()

	loadTheme(cfg.TUI)

	input := newComposerTextarea()
	input.Placeholder = "Ask Marshal..."
	input.CharLimit = 4000
	m.input = input
	// Seed the reconciliation baseline from the session's effective mode.
	// session.New normalizes the configured value, so a blank or
	// non-canonical config does not make the first reconcile flip the
	// model: it stays a no-op rather than re-asserting the mode over
	// whatever the model already shows.
	m.syncedAccessMode = st.Interaction.Mode

	// The held window opens when the model is built, so a resumed session
	// starts with nothing held. It is taken after the options run so an
	// injected clock is used.
	m.heldSince = m.now()
	if m.openBrowser == nil {
		m.openBrowser = mcpauth.OpenBrowser
	}

	// The LSP reference-lookup context is independent of m.ctx (the program's
	// cancellable turn context, which can be nil or replaced on a
	// provider-build failure). It is cancelled on shutdown so a lingering
	// lookup never leaks past exit.
	m.lspCtx, m.lspCancel = context.WithCancel(context.Background())

	// bubbles maps wheel-left/right to horizontal panning; step 0 disables
	// it at the viewport level too (the wheel events are already dropped in
	// Update, this covers any other path that forwards mouse messages).
	m.viewport.SetHorizontalStep(0)

	// Seed the in-session discovered map from the host's cache so the model
	// picker opens instantly with the last-seen lists when the config hash
	// still matches. The host reports nothing when no cache directory is
	// configured.
	for name := range cfg.Providers {
		if models, ok := m.host.ListModels(m.ctx, name); ok {
			m.discovered[name] = models
		}
	}

	// F18: build the completion popups eagerly from whatever source data
	// was wired by options. The cmd popup is built empty and the
	// registry is queried lazily inside updateCompletionPopups (avoids
	// duplicating the registry snapshot and lets the registry be
	// registered in any order).
	if m.cmdPopup == nil {
		m.cmdPopup = newCompletionPopup(nil)
	}
	if m.filePopup == nil {
		m.filePopup = newCompletionPopup(m.fileIndex)
	}
	// Seed the work view from the session the model holds. The session serves
	// the runtime's Work snapshot, and signals Changed when it changes.
	m.resetWork()

	// Eagerly build inline approval/question forms if the session already
	// has a pending request (parent or subagent), so the first render shows
	// the huh surface instead of the legacy fallback panels. One Pending read
	// builds every form, so a request that opens or settles mid-construction
	// cannot leave two surfaces disagreeing.
	pending := m.sess.Pending()
	if len(pending.Approvals) > 0 {
		m.approvalModel = newApprovalModel(displayApproval(pending.Approvals[0]), st.Sandbox, st.HasBackup, max(m.leftWidth-4, 30))
	}
	if q := pending.Question; q != nil {
		m.questionModel = newQuestionModel(*q, max(m.leftWidth-4, 30))
	}
	if sg := pending.SkillGate; sg != nil {
		m.skillGateModel = newSkillGateModel(*sg, max(m.leftWidth-4, 30))
	}
	if sa := pending.Sudo; sa != nil {
		m.sudoAuthModel = newSudoAuthModel(*sa, max(m.leftWidth-4, 30))
	}

	m.histIdx = -1
	if cfg.History.Enabled {
		if prompts, err := m.sess.RecentPrompts(m.ctx, promptHistoryLimit); err == nil {
			m.history = prompts
		}
	}

	m.openTrustPanel()

	if m.openConnectOnStart && !m.dock.IsOpen() {
		// A pending modal (trust prompt) owns the dock; connect opens after
		// it resolves. On the trust-grant path the app reloads and lands
		// here again with trust settled, so nothing is lost.
		m.openConnect("/")
	}

	m.refreshDiagnostics()

	return m
}

func blinkCmd() tea.Cmd {
	return textarea.Blink
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{blinkCmd()}
	// The session reads HEAD and the changed files off the UI thread, so
	// ±N files is right from the first frame instead of waiting for a turn
	// to end.
	cmds = append(cmds, refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true}))
	if cmd := pumpChanged(m.sess.Changed()); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (m *Model) resize(width, height int) {
	m.rawWidth = width
	m.rawHeight = height
	if width < minTerminalWidth {
		width = minTerminalWidth
	}
	if height < minTerminalHeight {
		height = minTerminalHeight
	}

	m.width = width
	m.height = height

	m.leftWidth = width

	// The approval review body's height has to be known before any row budget
	// is queried: inputChromeRows reads the prompt's rendered rows, and the
	// tray's budget subtracts that.
	m.syncPromptReviewHeights()

	// Input interior: the console's "│ " and " │" take 4 cells of the left
	// column. The textarea's SetWidth sets the text wrap width and internally
	// subtracts promptWidth (2), so the rendered line plus its gutter stays
	// inside the outline.
	m.input.SetWidth(max(m.leftWidth-4, 1))
	if m.approvalEdit != nil {
		// The isolated edit editor occupies the same column while it is open.
		m.approvalEdit.input.SetWidth(max(m.leftWidth-4, 1))
	}
	if m.doctorSecret != nil {
		// The masked key field occupies the same column while a repair is open.
		m.doctorSecret.input.SetWidth(max(m.leftWidth-4, 1))
	}

	// Transcript viewport spans the left column, less the drill frame's gutter.
	m.syncTranscriptWidth()
	m.input.MaxHeight = m.maxInputHeight()
	m.viewport.SetHeight(max(height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.drillCloseRows()-m.boxRows()-m.dockRows()-m.inputAreaRows()-statusLineRows, 1))
}

// sheetSections is the session sheet's section list, minus any section the
// config's [tui.side_panel].hidden names.
func (m Model) sheetSections() []sessionsheet.Section {
	hidden := map[string]bool{}
	for _, id := range m.sess.Config().TUI.SidePanel.Hidden {
		hidden[id] = true
	}
	all := sessionsheet.DefaultSections()
	visible := make([]sessionsheet.Section, 0, len(all))
	for _, s := range all {
		if !hidden[s.ID()] {
			visible = append(visible, s)
		}
	}
	return visible
}

// openSessionSheet opens the Ctrl+B session sheet (or closes it, if it is
// already the open panel). The session re-reads the sheet's cached values
// once on open, before the panel is built, so its first frame is fresh.
func (m *Model) openSessionSheet() {
	if m.sheetOpen() {
		m.closeSessionSheet()
		return
	}
	_ = m.sess.RefreshSheet(m.ctx, acpwire.SheetRefresh{Changed: true, Stats: true, Fleet: true})
	panel := sessionsheet.NewPanel(m.sheetSections(), m.sheetDataFor)
	// Opening a panel with nothing in it would leave an invisible dock that
	// swallows keys until Esc (every section hidden or irrelevant).
	if !panel.HasContent() {
		m.note("Nothing to show in the session sheet.")
		m.refreshViewport()
		return
	}
	m.tasksPanel = nil // opening replaces the Tasks panel if it was up
	m.sheetPanel = panel
	m.dock.Open(m.sheetPanel)
	m.refreshViewport()
}

// sheetOpen reports whether the session sheet is the open dock panel.
func (m Model) sheetOpen() bool {
	return m.sheetPanel != nil && m.dock.Panel() == dock.Panel(m.sheetPanel)
}

func (m *Model) closeSessionSheet() {
	if m.sheetOpen() {
		m.dock.CloseNow()
	}
	m.sheetPanel = nil
	m.refreshViewport()
}

// sheetDataFor is the sheet's data source. The adapter picks the subagent's
// sheet and falls back to the session's, so the Model only says which ID to
// read.
func (m Model) sheetDataFor() sessionsheet.Data {
	st := m.sess.Status()
	sh := m.sess.Sheet(m.drilledSheetID())
	data := sessionsheet.Data{
		Git:         st.Git,
		Repo:        sh.Repo,
		Turns:       sh.Turns,
		Totals:      sh.Totals,
		Changed:     sh.Changed,
		Fleet:       sh.Fleet,
		Now:         m.now(),
		Context:     sh.Context,
		Tools:       sh.Tools,
		Files:       sh.Files,
		Rules:       sh.Rules,
		Skills:      sh.Skills,
		StartedAt:   acpwire.Time(sh.StartedAt),
		ToolBudget:  sh.ToolBudget,
		LoadedTools: sh.LoadedTools,
		Sandbox:     sh.Sandbox,
		Spinner:     m.turnSpinnerFrame(),
	}
	// The run progress belongs to the session, not to a subagent's sheet.
	if sh.SubagentID == 0 {
		data.Swarm = st.Swarm
		data.SDD = st.SDD
	}
	return data
}

// drilledSheetID is the drilled-in subagent's ID, or 0 when the view is not
// drilled in.
func (m Model) drilledSheetID() int64 {
	if len(m.viewStack) == 0 {
		return 0
	}
	v, ok := m.drilledInto()
	if !ok {
		return 0
	}
	return v.ID
}

// ctrlCAction is what a double Ctrl+C will do.
type ctrlCAction int

const (
	ctrlCNone ctrlCAction = iota
	ctrlCStop
	ctrlCQuit
)

// ctrlCExpiredMsg only triggers a repaint once an armed Ctrl+C has lapsed.
type ctrlCExpiredMsg struct{}

// ctrlCWindow is how long a first Ctrl+C stays armed.
const ctrlCWindow = 3 * time.Second

// ctrlCArmed returns the armed action, or ctrlCNone once the window has
// expired. Rendering calls this too, so a stale arm shows as disarmed
// without needing a timer message.
func (m Model) ctrlCArmed() ctrlCAction {
	if m.ctrlCArmedFor == ctrlCNone || m.now().Sub(m.ctrlCArmedAt) > ctrlCWindow {
		return ctrlCNone
	}
	return m.ctrlCArmedFor
}

// Update is the Bubble Tea entry point. It reconciles the access panel around
// the existing message routing rather than inside it: reconciliation before
// the update covers the runtime early returns that would otherwise skip it,
// and reconciliation after covers every dock-close branch without patching
// each one. A value receiver cannot defer, so the two calls are explicit.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.reconcileAccess()
	// An approval edit is bound to exactly one request. When that request is no
	// longer the pending one — settled, interrupted or replaced — the edit is
	// retired before any message can reach it, the composer or a replacement.
	if m.approvalEdit != nil && !m.approvalEditLive() {
		m.retireApprovalEdit()
		m.updateViewportHeight()
		m.invalidateTranscript()
	}
	// Below the supported terminal minimum the size gate hides the frame, so
	// no approval may be accepted from an invisible panel. Ctrl+C still passes
	// through: the application's interrupt handling must keep working.
	hidden := m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight
	if hidden {
		// The exit postmortem question's agent stage asks for consent to a
		// pass with system access and editing approval. A hidden frame cannot
		// show that disclosure, so the affirmative answers are consumed here,
		// before the dock can turn them into consent. Declining (n, N, Esc)
		// and Ctrl+C still pass through: the question must never be a dead
		// end, whatever the geometry. Enter is consumed unconditionally — it
		// is the affirmative confirmation while Yes is selected, so it cannot
		// be classified as a decline key here; n, N and Esc are the decline
		// routes at a hidden geometry.
		consentOwnsDock := m.postmortemConsentPanel() != nil
		if consentOwnsDock {
			if key, ok := msg.(tea.KeyPressMsg); ok && postmortemConsentKey(key) {
				return m, nil
			}
		}
		// An access request queued behind another panel consumes keys as well.
		// The postmortem question is the one panel whose decline must still
		// reach it: those keys answer the modal question the user can act on,
		// never the request neither of them can see. Approvals are already
		// consumed above, and access choices stay consumed here.
		if m.sess.Pending().Access != nil {
			switch key := msg.(type) {
			case tea.KeyPressMsg:
				if key.String() != "ctrl+c" && !(consentOwnsDock && postmortemDeclineKey(key)) {
					return m, nil
				}
			case tea.PasteMsg, accesspanel.ChoiceMsg:
				return m, nil
			}
		}
		// A hidden review cannot show which candidate `d` would delete, so the
		// discard keys are consumed before the dock can arm or confirm one.
		// Esc still reaches the panel and keeps the candidate, and Ctrl+C keeps
		// its own path.
		if m.reviewPanel() != nil {
			if key, ok := msg.(tea.KeyPressMsg); ok {
				switch key.String() {
				case "d", "D":
					return m, nil
				}
			}
		}
	}
	if choice, ok := msg.(accesspanel.ChoiceMsg); ok {
		m.handleAccessChoice(choice.Choice)
		m.reconcileAccess()
		return m, nil
	}

	// Send now (Alt+Enter) is global, so it is handled here — before the dock
	// and the pending-prompt routing that would otherwise have the first claim
	// on the key. Below the supported terminal minimum the whole frame is
	// hidden: an invisible keypress must interrupt nothing, and neither may a
	// submission from an invisible composer. While the composer is up the key
	// belongs to the composer, which the routing below delivers it to.
	switch sendNow := msg.(type) {
	case steerpanel.SubmitMsg:
		if hidden {
			return m, nil
		}
		out, cmd := m.handleComposerSubmit(sendNow)
		m = reconcileIfModel(out)
		return m, cmd
	case steerpanel.CloseMsg:
		out, cmd := m.handleComposerClose(sendNow)
		m = reconcileIfModel(out)
		return m, cmd
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && isSendNowKey(k) && !hidden && m.steeringComposer == nil {
		// Any key other than a second Ctrl+C disarms the pending interrupt, and
		// Alt+Enter is intercepted before the inner router can apply that rule.
		m.ctrlCArmedFor = ctrlCNone
		out, cmd := m.beginSendNow()
		m = reconcileIfModel(out)
		return m, cmd
	}

	// The composer owns the input area. Key and paste messages go only to it;
	// every other message runs the ordinary routing once against the panel the
	// composer suspended, so that panel's asynchronous result still lands.
	//
	// Ctrl+C is the exception: the composer deliberately declines it, because a
	// global interrupt must keep working while a steering message is being
	// written. It falls through to the ordinary routing below.
	if m.steeringComposer != nil && !composerPassesThrough(msg) {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			m.ctrlCArmedFor = ctrlCNone
			return m, m.dock.Update(msg)
		}
		updated, cmd := m.routeWithSuspendedSurface(msg)
		// Detail wants are fetched here, after the update, never during it:
		// refreshViewport runs on every spinner tick and returns no commands.
		// tea.Batch drops nil commands, so an update with nothing wanted
		// returns exactly the command it returned before.
		switch next := updated.(type) {
		case Model:
			next.reconcileAccess()
			fetch := next.detailQueryCmds()
			// The call queue is served here too (P3 R6): a handler that
			// enqueued a call starts it from the epilogue, never inside
			// itself.
			calls := next.serveCalls()
			return next, tea.Batch(cmd, fetch, calls)
		case *Model:
			next.reconcileAccess()
			fetch := next.detailQueryCmds()
			calls := next.serveCalls()
			return next, tea.Batch(cmd, fetch, calls)
		default:
			return updated, cmd
		}
	}

	updated, cmd := m.update(msg)
	switch next := updated.(type) {
	case Model:
		next.reconcileAccess()
		fetch := next.detailQueryCmds()
		calls := next.serveCalls()
		return next, tea.Batch(cmd, fetch, calls)
	case *Model:
		next.reconcileAccess()
		fetch := next.detailQueryCmds()
		calls := next.serveCalls()
		return next, tea.Batch(cmd, fetch, calls)
	default:
		return updated, cmd
	}
}

// reconcileIfModel applies post-routing access reconciliation to whichever
// model form a handler returned.
func reconcileIfModel(updated tea.Model) Model {
	switch next := updated.(type) {
	case Model:
		next.reconcileAccess()
		return next
	case *Model:
		next.reconcileAccess()
		return *next
	}
	return Model{}
}

// routeWithSuspendedSurface runs the ordinary routing once with the panel the
// composer suspended temporarily holding the dock, then restores the composer
// and records whatever the pass left behind. Without it a settings save, a
// provider probe or an install completion would be offered to the composer —
// which can never own it — and the suspended panel would never settle. The
// result reaches exactly one panel: the retained one, never both.
func (m Model) routeWithSuspendedSurface(msg tea.Msg) (tea.Model, tea.Cmd) {
	composer := m.dock.Panel()
	// The composer is suspended, not released: the exchange is silent and it
	// comes back below. With no displaced panel the slot is simply emptied the
	// same way, so the composer is never told it was closed.
	m.dock.SwapRetained(m.composerDisplaced)
	updated, cmd := m.update(msg)
	next, ok := modelOf(updated)
	if !ok {
		return updated, cmd
	}
	// Whatever the pass left in the dock is the surface to return to when the
	// composer closes: a panel that closed itself stays closed, and a panel
	// that re-opened (or one the pass opened) is the new one.
	next.composerDisplaced = next.dock.Panel()
	if composer != nil {
		next.dock.SwapRetained(composer)
	}
	return next, cmd
}

// modelOf normalises Update's return. The model is a value type with pointer
// methods, so a handler may legitimately return either form.
func modelOf(updated tea.Model) (Model, bool) {
	switch v := updated.(type) {
	case Model:
		return v, true
	case *Model:
		return *v, true
	}
	return Model{}, false
}

// composerPassesThrough reports whether a message must bypass the composer and
// run the ordinary routing. Ctrl+C is the only one: the composer declines it on
// purpose so the global stop/quit handling keeps working while a steering
// message is being typed.
func composerPassesThrough(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyPressMsg)
	return ok && k.String() == "ctrl+c"
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Ctrl+C never stops a turn or quits on a single press: the first press
	// arms (shown on the status line), a second within ctrlCWindow fires.
	// Checked before any overlay routing so it can never be captured by a
	// form's keymap.
	//
	// Non-key messages do not disarm: the window expires on its own, and a
	// turn that ends between the presses must not clear the arm (the second
	// press then sees a mismatched action and re-arms for quit rather than
	// turning a stop into a quit).
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() == "ctrl+c" {
			// Once an ending is in progress, Ctrl+C belongs to that ending: it
			// retries a failed stop/join (or is an idempotent no-op while the
			// warning is up). It must never reach the turn-stop path below —
			// cancelling a turn would stop work the user has not yet agreed to
			// stop — and it must never stack a second warning.
			if m.endStage != endIdle {
				m.ctrlCArmedFor = ctrlCNone
				return m, m.beginEnd(nil)
			}
			// Stop only a turn that has not been cancelled yet: cancelTurn
			// leaves busy set until agentFinishedMsg, so a turn that ignores
			// cancellation would otherwise make Ctrl+C unable to quit.
			want := ctrlCQuit
			if (m.busy || m.agentCancel != nil) && !m.cancelling {
				want = ctrlCStop
			}
			if m.ctrlCArmed() == want {
				m.ctrlCArmedFor = ctrlCNone
				if want == ctrlCStop {
					m.cancelTurn()
					m.note("Turn stopped.")
					m.refreshViewport()
					return m, nil
				}
				return m, m.beginEnd(nil)
			}
			m.ctrlCArmedFor = want
			m.ctrlCArmedAt = m.now()
			// Nothing else repaints an idle session, so schedule a repaint for
			// when the window lapses; otherwise the hint would outlive the arm.
			return m, tea.Tick(ctrlCWindow+50*time.Millisecond, func(time.Time) tea.Msg { return ctrlCExpiredMsg{} })
		}
		// Any other keypress disarms, so Ctrl+C never fires on a press the
		// user has mentally separated from the first.
		m.ctrlCArmedFor = ctrlCNone
	}

	// WindowSizeMsg must always resize the underlying layout (and the
	// settings/memory overlays) regardless of which overlay is open.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		// A resize invalidates any earlier rendering: the exit postmortem
		// question's consent is only valid for the geometry its disclosure was
		// drawn at. Invalidate before the new size is applied — the next
		// visible render recomputes it — so a key that arrives in between is
		// refused rather than consenting to rows the user never saw.
		if panel := m.postmortemConsentPanel(); panel != nil {
			panel.InvalidateVisibility()
		}
		// The review panel's discard confirmation is destructive: a resize
		// must not carry an arm into a frame the user has not seen, so the
		// next render has to re-show the candidate and the action.
		if panel := m.reviewPanel(); panel != nil {
			panel.InvalidateVisibility()
		}
		m.resize(ws.Width, ws.Height)
		if m.approvalModel != nil {
			m.approvalModel.SetSize(max(m.leftWidth-4, 30))
		}
		if m.questionModel != nil {
			m.questionModel.SetSize(max(m.leftWidth-4, 30))
		}
		if m.skillGateModel != nil {
			m.skillGateModel.SetSize(max(m.leftWidth-4, 30))
		}
		if m.sudoAuthModel != nil {
			m.sudoAuthModel.SetSize(max(m.leftWidth-4, 30))
		}
		m.refreshViewport()
		return m, nil
	}

	switch msg := msg.(type) {
	case workRequestMsg:
		return m.handleWorkRequest(msg)
	case workPageMsg:
		return m.handleWorkPage(msg)
	case workDetailMsg:
		return m.handleWorkDetail(msg)
	case workActionMsg:
		return m.handleWorkAction(msg)
	case boxStopMsg:
		return m.handleBoxStop(msg)
	case ctrlCExpiredMsg:
		return m, nil
	case callDoneMsg:
		// A converted call's result is handled here, on the Update goroutine,
		// and the queue's next entry starts from Update's epilogue.
		cmd := m.handleCallDone(msg)
		return m, cmd
	case settings.ChangedMsg:
		st := m.sess.Status()
		if msg.BlockedReason != "" {
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = true
			for _, receipt := range msg.Receipts {
				m.note("✗ " + receipt + " · save blocked: " + msg.BlockedReason)
			}
			m.refreshViewport()
			return m, nil
		}
		if msg.Saved {
			// The panel already wrote the target file; reload only.
			m.setReg = nil
			reloaded, err := m.sess.ReloadConfig(m.ctx, msg.Cfg)
			if err != nil {
				m.note(fmt.Sprintf("✗ settings saved, but live reload failed: %v", err))
				m.refreshViewport()
				return m, nil
			}
			if reloaded {
				m.afterRuntimeReload()
			}
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = false
			cfgPath := st.ProjectConfigPath
			base := st.WorkingDir
			if msg.GlobalTarget {
				if home, err := m.userHome(); err == nil {
					cfgPath = st.UserConfigPath
					base = home
				}
			}
			for _, receipt := range msg.Receipts {
				m.note("✓ " + receipt + " · " + relPath(base, cfgPath))
			}
			m.refreshViewport()
			return m, nil
		}
		// Global-target save failure: apply in memory, mark pending, emit
		// error, but NEVER fall through to persistAndReload (which writes
		// into the project config, violating the provenance constraint).
		if msg.GlobalTarget && !msg.Saved {
			m.applyNewConfig(msg.Cfg)
			m.configSavePending = true
			m.note(fmt.Sprintf("✗ save failed: %v · %s", msg.SaveErr, relPath(st.WorkingDir, st.UserConfigPath)))
			m.refreshViewport()
			return m, nil
		}
		saveErr, reloadErr := m.persistAndReload(msg.Cfg)
		if saveErr != nil {
			m.note(fmt.Sprintf("✗ save failed: %v", saveErr))
			m.refreshViewport()
			return m, nil
		}
		if reloadErr != nil {
			m.note(fmt.Sprintf("✗ settings saved, but live reload failed: %v", reloadErr))
			m.refreshViewport()
			return m, nil
		}
		// The adapter refreshed the layer snapshots when the panel's own
		// save wrote the file; persistAndReload below only reloads the
		// runtime.
		for _, receipt := range msg.Receipts {
			m.note("✓ " + receipt + " · " + relPath(st.WorkingDir, st.ProjectConfigPath))
		}
		m.refreshViewport()
		return m, nil
	case mcpauth.AuthDoneMsg:
		return m.handleMCPAuthDone(msg)
	case settings.BrowserClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case modeloptions.ChangedMsg:
		cmd := m.handleModelOptionsChanged(msg)
		m.refreshViewport()
		return m, cmd
	case modeloptions.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case memory.ShowMsg:
		m.dock.CloseNow()
		m.note(memory.RenderEntry(msg.Entry))
		m.refreshViewport()
		return m, nil
	case memory.DeletedMsg:
		if msg.Err != nil {
			m.note("✗ delete failed: " + msg.Err.Error())
		} else {
			m.note("✓ deleted memory: " + msg.Title)
		}
		m.refreshViewport()
		return m, nil
	case memory.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case trustpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case sessionsheet.ClosedMsg:
		m.closeSessionSheet()
		return m, nil
	case sessionsheet.RunCommandMsg:
		m.closeSessionSheet()
		return m.dispatchCommand("/" + msg.Command)
	case selScrollTickMsg:
		cmd := m.handleSelScrollTick(msg)
		return m, cmd
	case copyDoneMsg:
		cmd := m.handleCopyDone(msg)
		return m, cmd
	case connect.CopyLinkMsg:
		return m, m.copyText(msg.URL)
	case mcpauth.CopyLinkMsg:
		// A delayed copy request must not act on a replacement auth flow,
		// even one showing an identical URL, so the live panel has to accept
		// the message before anything reaches the clipboard.
		if panel, ok := m.dock.Panel().(*mcpauth.Panel); ok && panel.AcceptsCopyLink(msg) {
			return m, m.copyText(msg.URL)
		}
		return m, nil
	case flashClearMsg:
		// Only wakes the view; it must not reach the textarea path, which
		// would bump the suggestion generation and drop an in-flight result.
		return m, nil
	case editorDoneMsg:
		if msg.err != nil {
			return m, m.setFlash("$EDITOR failed: " + msg.err.Error())
		}
		return m, nil
	case inspector.ClosedMsg, inspector.NavigateMsg, inspector.CopyMsg, inspector.OpenMsg:
		cmd, _ := m.handleInspectorMsg(msg)
		return m, cmd
	case docpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case docActionMsg:
		m.dock.CloseNow()
		if msg.err != nil {
			m.note(msg.err.Error())
			m.refreshViewport()
			return m, nil
		}
		if msg.result.Doc != nil {
			m.dock.Open(docpanel.New(*msg.result.Doc, msg.act))
			m.refreshViewport()
			return m, nil
		}
		if msg.result.Text != "" {
			m.note(msg.result.Text)
		} else {
			// An action that carries neither a doc nor text would otherwise
			// close the pane in silence, as if the key had done nothing.
			m.note("That action returned nothing.")
		}
		m.refreshViewport()
		return m, nil
	case doctorpanel.ClosedMsg:
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case doctorpanel.FixMsg:
		// The repair asks for a key in its own masked field. The chat composer
		// is left exactly as it is: no reset, no placeholder swap, no paste
		// attachment touched, so a repair can neither discard nor submit the
		// draft the user was writing.
		m.dock.CloseNow()
		m.doctorSecret = newDoctorSecret(msg.Provider, max(m.leftWidth-4, 1))
		m.updateViewportHeight()
		m.invalidateTranscript()
		m.refreshViewport()
		return m, nil
	case activeworkpanel.DoneMsg:
		return m.handleActiveWorkDone(msg)
	case endSettledMsg:
		return m.handleEndSettled(msg)
	case postmortemWrittenMsg:
		return m.handlePostmortemWritten(msg)
	case endAnalysisFinishedMsg:
		return m.handleEndAnalysisFinished(msg)
	case postmortempanel.DoneMsg:
		m.dock.CloseNow()
		return m.handlePostmortemDone(msg)
	}

	// Runtime messages always stay with the parent model so background state
	// remains current while a dock panel is open.
	switch msg.(type) {
	case agentFinishedMsg, planAuthorFinishedMsg, changedMsg, agentTickMsg, spinnerTickMsg, suggestionMsg, callersMsg, detailMsg:
		return m.handleRuntimeMessage(msg)
	}

	// OpenConnectMsg from the settings browser: capture the current filter,
	// close the browser, and open the connect wizard.
	if _, ok := msg.(settings.OpenConnectMsg); ok {
		if bp, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
			m.connectReturnFilter = bp.FilterValue()
		}
		m.connectReturnToSettings = true
		m.dock.CloseNow()
		m.openConnect("/")
		m.refreshViewport()
		return m, nil
	}

	// OpenModelOptionsForProviderMsg from the settings browser: close the
	// browser and open the model-options picker for the named provider.
	if om, ok := msg.(settings.OpenModelOptionsForProviderMsg); ok {
		m.dock.CloseNow()
		m.openModelOptionsForProvider(om.ProviderName)
		m.refreshViewport()
		return m, nil
	}

	// Browser-owned picker and probe/action messages must return to the
	// browser before the model's command-picker and connect handlers see
	// them. The browser itself is the dock panel, so it can safely handle
	// every remaining message here.
	if browser, ok := m.dock.Panel().(*settings.BrowserPanel); ok {
		blockedCmd := browser.SetSaveBlocked(m.settingsBlockReason())
		cmd := m.dock.Update(msg)
		if blockedCmd != nil {
			return m, tea.Batch(blockedCmd, cmd)
		}
		return m, cmd
	}

	// Picker messages: the picker itself lives in m.dock and is updated
	// through the dock. We only handle the terminal messages here.
	switch pm := msg.(type) {
	case connect.RefreshMsg:
		for _, name := range pm.Providers {
			delete(m.discovered, name)
			m.evictDiscovered(name)
		}
		return m, m.probeProviders(pm.Providers)
	case connect.DoneMsg:
		m.applyConnectDone(pm)
		m.dock.CloseNow()
		m.connectModel = nil
		if m.connectReturnToSettings {
			m.connectReturnToSettings = false
			m.openSettingsBrowser(m.connectReturnFilter)
			m.connectReturnFilter = ""
		}
		m.refreshViewport()
		return m, nil
	case connect.CancelledMsg:
		m.dock.CloseNow()
		m.connectModel = nil
		if m.connectReturnToSettings {
			m.connectReturnToSettings = false
			m.openSettingsBrowser(m.connectReturnFilter)
			m.connectReturnFilter = ""
		}
		m.refreshViewport()
		return m, nil
	case connect.OAuthDoneMsg, connect.TickMsg, connect.PreparedMsg:
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case modelsProbeResultMsg:
		return m.handleModelsProbeResult(pm)
	case connect.WizardProbeResult:
		return m.handleWizardProbeResult(pm)
	case probe.ResultMsg:
		if pm.FieldID == modelsProbeFieldID {
			// A discovery completion must arrive correlated with the attempt
			// that dispatched it; an untagged one cannot prove it is the
			// newest for its provider, so it moves no state at all.
			return m, nil
		}
		// Every model-discovery and setup probe is now wrapped in
		// modelsProbeResultMsg / connect.WizardProbeResult, so what reaches
		// here is a probe from another surface (the model-options panel
		// claims its own FieldID first). Keep the shared-state application and
		// the dock hand-off for those, since a panel that asked for the probe
		// is still the only one that can interpret its result.
		if pm.Err == nil && pm.Provider != "" {
			pm.Models = schema.PreserveThinkingOptions(pm.Models, m.discovered[pm.Provider])
			m.discovered[pm.Provider] = pm.Models
			m.persistDiscovered(pm.Provider, pm.Models)
			if panel, ok := m.dock.Panel().(*modeloptions.Panel); ok {
				preset := m.sess.Config().Models.Presets[panel.PresetName()]
				if preset.Provider == pm.Provider {
					panel.SetThinkingOptions(m.modelThinkingOptions(panel.PresetName()))
				}
			}
		}
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case presetflow.CapabilityProbedMsg:
		if _, ok := m.dock.Panel().(connect.Panel); ok {
			return m, m.dock.Update(pm)
		}
		return m, nil
	case picker.PickedMsg:
		// Forward to panel overlays that host their own picker (agents roster,
		// connect wizard). Their internal handlers will emit their own done
		// messages; we must not close the dock or treat this as a top-level
		// picker pick here.
		switch m.dock.Panel().(type) {
		case *agents.Panel, connect.Panel, *castlist.Panel:
			return m, m.dock.Update(pm)
		}
		cmdName := m.pickerCommand
		m.dock.CloseNow()
		m.pickerCommand = ""
		switch {
		case cmdName == "" || pm.Value == "":
			m.refreshViewport()
			return m, nil
		case cmdName == "model":
			// preset names may contain spaces; apply directly instead of
			// round-tripping through the arg splitter
			m.switchModelPreset(pm.Value)
			m.refreshViewport()
			return m, nil
		case cmdName == "model-options":
			// Open the model-options editor for the picked model pair.
			m.dock.Open(modeloptions.New(m.sess.Config(), pm.Value, m.resolveReasoningSupport(pm.Value), m.modelThinkingOptions(pm.Value)))
			m.refreshViewport()
			return m, m.probeThinkingOptions(pm.Value)
		case cmdName == "mode" && pm.Value == "sdd":
			m.openSDDPlanPicker()
			m.refreshViewport()
			return m, nil
		case cmdName == "mode":
			return m.dispatchCommand("/" + pm.Value)
		case cmdName == "effort":
			m.applyEffort(pm.Value)
			m.refreshViewport()
			return m, nil
		case cmdName == "sdd-plan":
			if pm.Value == sddCustomPlanPathValue {
				m.openSDDCustomPlanPathPicker()
				m.refreshViewport()
				return m, nil
			}
			if pm.Value == sddScaffoldPlanValue {
				m.scaffoldSDDPlan()
				m.refreshViewport()
				return m, nil
			}
			// Dock already closed above; dispatch /sdd with the picked path.
			return m.dispatchCommand("/sdd " + pm.Value)
		case cmdName == "sessions":
			if pm.Value == "" {
				m.refreshViewport()
				return m, nil
			}
			return m.beginResume(pm.Value)
		default:
			return m.dispatchCommand("/" + cmdName + " " + pm.Value)
		}
	case picker.CancelledMsg:
		// Forward to panel overlays that host their own picker.
		switch m.dock.Panel().(type) {
		case *agents.Panel, connect.Panel, *castlist.Panel:
			return m, m.dock.Update(pm)
		}
		m.dock.CloseNow()
		m.pickerCommand = ""
		m.refreshViewport()
		return m, nil
	case gatepanel.AnswerMsg:
		m.dock.CloseNow()
		// The session records the answer as the user's message and resumes the
		// run it paused; the TUI only starts that resumption on the run path
		// (decision 5).
		sess := m.sess
		return m.startSessionRun("Cannot start work", func(ctx context.Context) error {
			return sess.AnswerSDDGate(ctx, pm.Text)
		})

	case gatepanel.StopMsg:
		m.dock.CloseNow()
		if err := m.sess.StopSDD(m.ctx); err != nil {
			m.log().Warn("plan run stop was not accepted", "error", err)
		}
		m.note("Plan run stopped. " + sddResumeHint)
		m.refreshViewport()
		return m, nil

	case castlist.StartMsg:
		m.dock.CloseNow()
		run := m.pendingRun
		m.pendingRun = nil
		if run == nil || m.busy {
			m.refreshViewport()
			return m, nil
		}
		st := m.sess.Status()
		// Persist proposed verify commands to project config before the run
		// starts. A write failure warns but never blocks the run.
		if run.verifyBuild != "" || run.verifyTest != "" {
			if err := m.sess.SaveVerifyCommands(m.ctx, run.verifyBuild, run.verifyTest); err != nil {
				m.note(fmt.Sprintf("✗ verify commands apply to this run but could not be persisted: %v", err))
			} else {
				m.note("✓ verify commands persisted to " + relPath(st.WorkingDir, st.ProjectConfigPath))
			}
		}
		// Every run the TUI starts now takes the session's run path: the
		// per-run role overrides, the strategy and the proposed verify
		// commands travel as parameters and apply agent side (decision 2).
		sess := m.sess
		overrides := roleNames(pm.Overrides)
		switch run.kind {
		case "swarm":
			params := acpwire.SwarmParams{Goal: run.goal, Overrides: overrides}
			return m.startSessionRun("Cannot start work", func(ctx context.Context) error {
				return sess.StartSwarm(ctx, params)
			})
		case "sdd":
			params := acpwire.SDDParams{
				PlanPath:  run.goal,
				Strategy:  pm.Strategy,
				Overrides: overrides,
			}
			if run.verifyBuild != "" || run.verifyTest != "" {
				params.Verify = &acpwire.VerifyCommands{Build: run.verifyBuild, Test: run.verifyTest}
			}
			return m.startSessionRun("Cannot start work", func(ctx context.Context) error {
				return sess.StartSDD(ctx, params)
			})
		}
		m.refreshViewport()
		return m, nil
	case castlist.CancelMsg:
		m.dock.CloseNow()
		m.pendingRun = nil
		m.refreshViewport()
		return m, nil
	case runAgentMsg:
		if m.busy {
			return m, nil
		}
		if !m.sess.Status().AgentReady {
			m.refreshViewport()
			return m, nil
		}
		sess := m.sess
		params := acpwire.AgentRunParams{Agent: pm.agent, Goal: pm.goal}
		return m.startSessionRun("Cannot start work", func(ctx context.Context) error {
			return sess.RunAgent(ctx, params)
		})
	case verifyProposeMsg:
		if pm.err != nil {
			m.note(fmt.Sprintf("✗ could not propose verify commands: %v", pm.err))
			m.refreshViewport()
			return m, nil
		}
		// Stash the proposed commands for this run and re-render the gate
		// row so the user sees exactly what will run. Persistence happens on
		// confirm (castlist.StartMsg), not here.
		if m.pendingRun != nil {
			m.pendingRun.verifyBuild = pm.build
			m.pendingRun.verifyTest = pm.test
		}
		if p, ok := m.dock.Panel().(*castlist.Panel); ok {
			detail := strings.TrimSpace(pm.build + " · " + pm.test)
			if detail == "" {
				detail = "no commands proposed"
			}
			p.SetVerifyRow(strutil.Truncate(detail, 44, true))
		}
		m.note("Proposed verify commands: " + strings.TrimSpace(pm.build+" · "+pm.test) + ". Press Enter to run with them (persisted to .marshal/config.toml), or Esc to cancel.")
		m.refreshViewport()
		return m, nil
	case sddreview.AcceptMsg:
		// Accept leaves the candidate artifact in place for a later /sdd. A
		// delayed message from a review that is no longer docked must not
		// close its replacement.
		if pm.Panel == nil || pm.Panel != m.reviewPanel() {
			return m, nil
		}
		m.dock.CloseNow()
		m.refreshViewport()
		return m, nil
	case sddreview.KeepMsg:
		// Esc keeps the candidate whether the inspection was clean or blocked:
		// leaving review must never delete work the user may still edit.
		if pm.Panel == nil || pm.Panel != m.reviewPanel() {
			return m, nil
		}
		path := pm.Panel.CandidatePath()
		m.dock.CloseNow()
		m.note(fmt.Sprintf("Kept %s. Edit it first, then run /sdd to execute it.", candidatePhrase(path)))
		m.refreshViewport()
		return m, nil
	case sddreview.DiscardMsg:
		// Discard removes only the active review's generated candidate, and
		// only after the panel's own two-press confirmation.
		if pm.Panel == nil || pm.Panel != m.reviewPanel() {
			return m, nil
		}
		path := pm.Panel.CandidatePath()
		if err := m.sess.DiscardCandidate(m.ctx, path); err != nil {
			// The candidate is still on disk: keep the panel open so the user
			// can retry or keep it, and never claim it was removed.
			pm.Panel.SetError(err)
			m.refreshViewport()
			return m, nil
		}
		m.dock.CloseNow()
		m.note(fmt.Sprintf("Discarded %s.", candidatePhrase(path)))
		m.refreshViewport()
		return m, nil
	}
	if m.dock.IsOpen() {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			// Offer-to-fill: while the /sdd preflight is open and the verify
			// gate is unknown, `f` dispatches a one-shot proposal task instead
			// of falling through to the dock.
			if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "f" && m.pendingRun != nil && m.pendingRun.verifyBuild == "" && m.pendingRun.verifyTest == "" {
				if p, ok := m.dock.Panel().(*castlist.Panel); ok {
					if p.VerifyGateUnknown() {
						m.note("Proposing verify commands from the repository…")
						m.refreshViewport()
						return m, m.startVerifyProposal()
					}
				}
			}
			// Ctrl+B is the global toggle for the sessions sheet: with the
			// Tasks panel or the sheet itself up, it opens (or refreshes) the
			// sheet rather than being swallowed.
			if k, ok := msg.(tea.KeyPressMsg); ok && m.input.Value() == "" && !m.editingApproval() && (m.tasksOpen() || m.sheetOpen()) {
				switch k.String() {
				case "ctrl+b":
					m.openSessionSheet()
					return m, nil
				}
			}
			return m, m.dock.Update(msg)
		default:
			// A panel's own async results (install finished, scan returned)
			// are typically unexported, so no case above can name them and
			// they would otherwise fall through and be dropped — leaving the
			// panel's Update case dead code. Let the panel claim them.
			if m.dock.OwnsMsg(msg) {
				return m, m.dock.Update(msg)
			}
		}
		// Other non-key messages (ticks, agent events) keep flowing to the
		// normal handlers below so background work continues.
	}

	// F-BUG-147: Block overlay-opening hotkeys (Ctrl+O, Ctrl+K) while a
	// tool decision is pending. These must be intercepted before the
	// approval/question routing below, which would otherwise swallow them.
	// One Pending read serves this block and the prompt routing chain.
	p := m.sess.Pending()
	if len(p.Approvals) > 0 || p.Question != nil || p.SkillGate != nil || p.Sudo != nil {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "ctrl+o":
				m.note("Resolve the pending tool decision before opening settings.")
				m.refreshViewport()
				return m, nil
			case "ctrl+k":
				m.note("Resolve the pending tool decision before opening memory browser.")
				m.refreshViewport()
				return m, nil
			}
		}
	}

	// Inline privileged-auth prompt: while a sudo password is pending, route
	// messages to the masked form (scroll gestures first, mirroring the
	// approval and question forms).
	if sa := p.Sudo; sa != nil {
		// A prompt the user cannot see must never read a password byte or
		// authenticate: below the supported minimum, behind a full-frame panel,
		// or with no room for its body, only Escape reaches the form so denial
		// still works. Every other key and every paste is dropped before the
		// masked field can see it. Ctrl+C never arrives here: it is handled at
		// the top of update().
		if !m.sudoActionable() {
			switch k := msg.(type) {
			case tea.PasteMsg:
				return m, nil
			case tea.KeyPressMsg:
				if k.String() != "esc" {
					return m, nil
				}
				return m.handleSudoAuth(msg, *sa)
			}
		}
		if m.handleSudoPageKey(msg) {
			return m, nil
		}
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleSudoAuth(msg, *sa)
	}

	// Inline skill-gate dialog: while a skill load gate is pending, route
	// messages to it (scroll gestures first, mirroring the approval and
	// question forms).
	if sg := p.SkillGate; sg != nil {
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleSkillGate(msg, *sg)
	}

	// Inline approval chooser: when a tool call is pending, route every
	// message (keypresses AND huh's internal nextField/nextGroup messages)
	// to the approval form so selection navigation round-trips correctly.
	// The edit sub-mode owns its own textarea (approvalEdit) and never
	// touches the chat composer, so a draft typed before the prompt is
	// still there afterwards.
	//
	// Scroll gestures are handled first so the user can review transcript
	// history before approving a destructive command.
	if a, ok := m.pendingApprovalTarget(); ok {
		// A prompt the user cannot fully see must never accept a decision they
		// could not review: below the supported minimum, behind a full-frame
		// panel, or with no room for the body, only Escape reaches the form so
		// the deny window still works. Every other key and paste is dropped
		// before it can reach the form or the edit sub-mode. Ctrl+C never
		// arrives here: it is handled at the top of update().
		if !m.promptActionable() {
			switch k := msg.(type) {
			case tea.PasteMsg:
				return m, nil
			case tea.KeyPressMsg:
				if k.String() != "esc" {
					return m, nil
				}
				return m.handleApproval(msg, a)
			}
		}
		if m.handleApprovalPageKey(msg) {
			return m, nil
		}
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleApproval(msg, a)
	}

	// Inline question prompt: when a clarifying question is pending, route
	// messages to the question form. Scroll gestures are handled first so the
	// transcript remains navigable while the question is visible.
	if q := p.Question; q != nil {
		if updated, cmd, handled := m.scrollTranscript(msg); handled {
			return updated, cmd
		}
		return m.handleQuestion(msg, *q)
	}

	// Bubble Tea v2 emits a KeyReleaseMsg alongside every KeyPressMsg.
	// Nothing in the model cares about release events, and letting them
	// fall through to the default path would re-run
	// updateCompletionPopups() and snap the popup selection back to
	// index 0 after every arrow-key press.
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return m, nil
	}

	// Doctor repair: the masked key field owns the keyboard while it is up. It
	// is intercepted here — after the prompts, which keep their precedence, and
	// before the composer, the paste-condensing branch and handleKeypress — so
	// ordinary typing, "?", completion, history and the large-paste attachment
	// never see a byte of the key.
	if m.doctorSecret != nil {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			return m.handleDoctorSecret(msg)
		}
	}

	switch msg := msg.(type) {
	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		// Wheel, click and drag all go to the transcript (selection_input.go).
		cmd, _ := m.handleTranscriptMouse(msg)
		return m, cmd
	case tea.PasteMsg:
		if shouldCondensePaste(msg.Content) {
			cmd := m.addPaste(msg.Content)
			m.updateViewportHeight()
			m.refreshViewport()
			return m, cmd
		}
		// Small paste: fall through to the textarea as today.
	case tea.KeyPressMsg:
		if mm, cmd, handled := m.handleKeypress(msg); handled {
			return mm, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// Typing that breaks the suggestion's prefix clears it so a stale ghost
	// cannot resurrect if the user deletes back to empty. Any keystroke also
	// bumps the generation counter so an in-flight LLM fallback result is
	// discarded.
	m.suggestionGen++
	m.clearSuggestionIfPrefixBroken()
	m.updateCompletionPopups()

	// The textarea updates its own height (DynamicHeight); recalculate the
	// viewport height and refresh if it changed.
	viewportHeightChanged := m.updateViewportHeight()
	if viewportHeightChanged {
		m.invalidateTranscript()
		m.refreshViewport()
	}

	return m, cmd
}

// pendingApprovalTarget returns the pending approval the user should answer
// next, as the session lists it: the session's own first, then each running
// subagent's, in registration order.
//
// Pending returns them in that order, so the first is deterministic. With
// parallel agent.run (up to agent.max_concurrent_subagents at once), several
// children could pend at once; answering the first-registered one is
// acceptable — the other surfaces on the next message.
func (m *Model) pendingApprovalTarget() (acpwire.ApprovalRequest, bool) {
	approvals := m.sess.Pending().Approvals
	if len(approvals) == 0 {
		return acpwire.ApprovalRequest{}, false
	}
	return approvals[0], true
}

// hasPendingApproval reports whether any approval is pending — the session's
// own or a running subagent's. Rendering, status indicators, and keypress
// gating must all consult this so a child approval is not silently routed
// into an invisible form.
func (m *Model) hasPendingApproval() bool {
	return len(m.sess.Pending().Approvals) > 0
}

// displayApproval is a as the dialog shows it. A subagent's approval has its
// reason prefixed with the subagent's label and its Access mark cleared, as
// the display copy had.
func displayApproval(a acpwire.ApprovalRequest) acpwire.ApprovalRequest {
	if a.SubagentID == 0 {
		return a
	}
	a.Reason = fmt.Sprintf("subagent %q: %s", a.Subagent, a.Reason)
	a.Access = false
	return a
}

// pendingApprovalDisplay returns the approval to render for the pending
// approval the user should answer next: a subagent's approval names the
// subagent, so the rendered panel (and the eager form) attribute the request.
// Returns false when no approval is pending.
func (m *Model) pendingApprovalDisplay() (acpwire.ApprovalRequest, bool) {
	a, ok := m.pendingApprovalTarget()
	if !ok {
		return acpwire.ApprovalRequest{}, false
	}
	return displayApproval(a), true
}

// accessWaitingRow is the muted disclosure shown while a typed access request
// is queued behind another dock panel. The panel itself is the only decision
// UI; this row exists so the request is never invisible, and it deliberately
// renders no approve/deny form of its own.
const accessWaitingRow = "Access request waiting — close the current panel to review."

// accessPanelOwnsDock reports whether the dock's current panel is the access
// panel. When it is, that panel is the decision UI and the input area must not
// also disclose a queued request.
func (m Model) accessPanelOwnsDock() bool {
	_, ok := m.dock.Panel().(*accesspanel.Panel)
	return ok
}

// accessRequestSnapshot reads the pending access request through the session.
// The adapter settles a request that no longer asks for anything and refuses
// one that belongs to a session it does not serve, so this reads the panel's
// subject and nothing else.
func (m *Model) accessRequestSnapshot() (acpwire.AccessRequest, bool) {
	return m.sess.ReconcileAccess(m.ctx)
}

// accessOutcomeNote renders one concise line naming the outcome and the actual
// effective state, so the transcript never claims a capability the session
// does not hold.
func accessOutcomeNote(result acpwire.AccessResult) string {
	state := fmt.Sprintf("mode %s, system access %t", result.Effective.Mode, result.Effective.System)
	switch result.Status {
	case acpwire.AccessGranted:
		return fmt.Sprintf("Access granted — %s.", state)
	case acpwire.AccessDenied:
		return fmt.Sprintf("Access denied — %s.", state)
	case acpwire.AccessAvailable:
		return fmt.Sprintf("Access already available — %s.", state)
	case acpwire.AccessCanceled:
		return fmt.Sprintf("Access request canceled — %s.", state)
	default:
		msg := result.Message
		if msg == "" {
			msg = "the request could not be applied"
		}
		return fmt.Sprintf("Access request failed: %s — %s.", msg, state)
	}
}

// reconcileAccess brings the dock's access panel in line with the runtime's
// pending request. It runs on every update, so a request surfaces on the next
// runtime tick without the user typing anything, and a request that was
// canceled, replaced or satisfied stops being shown.
//
// It is presentation only. It never applies a grant: the adapter settles a
// request that nothing can answer, and refuses one that belongs to a session
// this model does not serve.
func (m *Model) reconcileAccess() {
	// The displayed permission state is synchronized here rather than in the
	// message handlers, so a grant the runtime applied (or a reload that
	// preserved an override) is reflected on the next update. The sync is
	// edge-triggered on the session's own mode: re-asserting an unchanged
	// value would clobber a mode another path just set (cycleMode reads
	// m.approvalMode to compute the next rung).
	effectiveMode := m.sess.Status().Interaction.Mode
	modeChanged := m.syncedAccessMode != effectiveMode
	if modeChanged {
		m.syncedAccessMode = effectiveMode
	}

	snapshot, pending := m.accessRequestSnapshot()

	current, isAccessPanel := m.dock.Panel().(*accesspanel.Panel)
	switch {
	case isAccessPanel && !pending:
		// The request this panel was showing is gone — settled, canceled or
		// replaced. Close only that panel.
		m.dock.CloseNow()
		m.refreshViewport()
	case isAccessPanel:
		shown := current.Request()
		changed := shown.RequestID != snapshot.RequestID ||
			shown.SessionID != snapshot.SessionID ||
			shown.Current != snapshot.Current ||
			shown.MissingEditing != snapshot.MissingEditing ||
			shown.MissingSystem != snapshot.MissingSystem
		if changed {
			// A different request owns the slot, or the permission tuple moved
			// under the same request: re-present from scratch so Deny is
			// selected again for the state the user can actually see.
			m.dock.Open(accesspanel.New(snapshot))
			m.refreshViewport()
		}
		// Otherwise the snapshot is unchanged: keep the same panel pointer so
		// selection and scroll survive ordinary rerenders and resizes.
	case pending && !m.dock.IsOpen():
		// The dock is free. Below the supported terminal minimum the size gate
		// hides the frame, so opening here would present an invisible approval.
		if m.rawWidth >= minTerminalWidth && m.rawHeight >= minTerminalHeight {
			m.dock.Open(accesspanel.New(snapshot))
			m.refreshViewport()
		}
	}
	// A pending request queued behind another panel is disclosed by the input
	// area and the status line; that panel is deliberately left open.
	if modeChanged {
		m.refreshViewport()
	}
}

// handleAccessChoice submits the user's decision for the access panel that is
// actually open. It is deliberately narrow: the choice must name the panel's
// exact request, session and presented tuple, so a stale message from an
// earlier request can never answer a newer one.
func (m *Model) handleAccessChoice(choice acpwire.AccessChoice) {
	panel, ok := m.dock.Panel().(*accesspanel.Panel)
	if !ok {
		return
	}
	shown := panel.Request()
	if choice.RequestID != shown.RequestID ||
		choice.SessionID != shown.SessionID ||
		choice.Current != shown.Current {
		// Stale or foreign: leave whatever is on screen alone.
		return
	}
	// The resolver is a bounded, memory-only operation, so it runs inline: the
	// panel must not close before the runtime has committed the decision.
	result, err := m.sess.AnswerAccess(m.ctx, choice)
	if err != nil {
		m.dock.CloseNow()
		m.note(fmt.Sprintf("Access request failed: %v", err))
		m.refreshViewport()
		return
	}
	if result.Status == acpwire.AccessRefresh {
		// The permission tuple moved while the user was deciding. The request
		// stays pending; re-present the fresh state with Deny selected.
		if fresh, pending := m.accessRequestSnapshot(); pending {
			m.dock.Open(accesspanel.New(fresh))
		} else {
			m.dock.CloseNow()
		}
		m.refreshViewport()
		return
	}
	// Final outcome: close only the panel that asked, then report the state the
	// runtime actually committed.
	m.dock.CloseNow()
	m.note(accessOutcomeNote(result))
	m.refreshViewport()
}

// approvalEdit is the isolated editor for one pending approval's command or
// JSON arguments. It owns its own textarea, so opening, typing in, saving or
// abandoning an edit never shares text, cursor, placeholder or paste
// attachments with the chat composer. It remembers the key of the approval it
// edits: an edit whose request settled, was replaced or was interrupted is
// retired instead of being allowed to settle whatever is pending now.
type approvalEdit struct {
	input textarea.Model
	key   string // the approval's key
}

// newApprovalEdit builds the editor for a, prefilled from the request. The
// character limit is unlimited so a long command or argument blob is never
// silently truncated.
func newApprovalEdit(a acpwire.ApprovalRequest, width int) *approvalEdit {
	input := newComposerTextarea()
	input.CharLimit = 0
	if a.Tool == "shell.run" {
		// Command editing shows the whole command as one editable value.
		input.Placeholder = "Edit command..."
		input.SetValue(a.Command)
	} else {
		input.Placeholder = "Edit JSON arguments..."
		input.SetValue(a.Args)
	}
	input.SetWidth(max(width, 1))
	input.MoveToEnd()
	return &approvalEdit{input: input, key: a.Key}
}

// approvalEditLive reports whether the open edit is still bound to the approval
// pending now. A settled, interrupted or replaced request leaves the edit
// stale, and a stale edit must not own the input or accept a decision.
func (m Model) approvalEditLive() bool {
	ed := m.approvalEdit
	if ed == nil {
		return false
	}
	a, ok := m.pendingApprovalTarget()
	return ok && a.Key == ed.key
}

// editingApproval reports whether an approval edit holds the input.
func (m Model) editingApproval() bool { return m.approvalEditLive() }

// retireApprovalEdit drops the edit without settling, approving or clearing
// anything. The chat composer underneath is untouched.
func (m *Model) retireApprovalEdit() { m.approvalEdit = nil }

// handleApproval routes messages to the inline approval chooser (or the
// isolated edit-command editor) while a tool-call approval is pending. It is
// called before the main keypress switch so huh's internal navigation messages
// (nextFieldMsg/nextGroupMsg) round-trip back to the form.
//
// a is the pending approval as the session lists it; the decision goes back
// through AnswerApproval on its key. A display copy whose reason names the
// subagent is what the dialog shows.
func (m Model) handleApproval(msg tea.Msg, a acpwire.ApprovalRequest) (tea.Model, tea.Cmd) {
	st := m.sess.Status()
	// A typed access request is presented by the accesspanel through
	// reconciliation, never here: it must not create a generic approval model
	// or consume runtime events. A raw untyped mode.request (no Access
	// metadata) fails closed with an ordinary denial, because nothing has
	// validated or applied what it asks for.
	if a.SubagentID == 0 && a.Tool == "mode.request" && !a.Access {
		if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "enter" {
			_ = m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalDeny})
			m.note("mode.request was not resolved by the runtime; no capability was granted.")
			m.refreshViewport()
			return m, nil
		}
		return m, nil
	}

	// Edit sub-mode: an isolated editor owns the input. The chat composer and
	// its paste attachments stay untouched underneath it, and the edit is
	// bound to the exact approval it was opened for.
	if m.approvalEdit != nil {
		if !m.approvalEditLive() {
			// The request this edit was bound to is gone: replaced, settled or
			// interrupted. Retire the edit without approving anything, without
			// clearing the replacement and without touching the draft.
			m.retireApprovalEdit()
			m.updateViewportHeight()
			m.invalidateTranscript()
			return m, nil
		}
		ed := m.approvalEdit
		if k, ok := msg.(tea.KeyPressMsg); ok {
			switch k.String() {
			case "esc":
				// Abandon the edit and return to the same pending approval.
				m.retireApprovalEdit()
				m.updateViewportHeight()
				m.invalidateTranscript()
				return m, nil
			case "enter":
				value := strings.TrimSpace(ed.input.Value())
				if value != "" {
					// The session arbitrates against a steering cancellation
					// that already owns the settlement; the error is not acted
					// on, exactly as the other answer calls.
					_ = m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce, Edited: value})
					m.approvalModel = nil
					m.retireApprovalEdit()
					m.updateViewportHeight()
					m.invalidateTranscript()
					return m, nil
				}
				// Empty content is not an approval: stay in the editor.
				m.invalidateTranscript()
				return m, nil
			}
		}
		var cmd tea.Cmd
		ed.input, cmd = ed.input.Update(msg)
		m.updateViewportHeight()
		return m, cmd
	}

	// Lazily build the inline approval chooser the first time a message
	// arrives for a pending approval, and rebuild it when it is bound to
	// another key.
	if m.approvalModel == nil || m.approvalModel.a.Key != a.Key {
		// A different pending request replaces the prompt, so the review must
		// start at the top of the new operation rather than wherever the
		// previous one was left scrolled.
		m.approvalModel = newApprovalModel(displayApproval(a), st.Sandbox, st.HasBackup, max(m.leftWidth-4, 30))
		m.syncPromptReviewHeights()
	}
	am, cmd := m.approvalModel.Update(msg)
	m.approvalModel = am
	if !m.approvalModel.IsDone() {
		return m, cmd
	}

	choice := m.approvalModel.Choice()
	m.approvalModel = nil
	switch choice {
	case choiceApprove:
		_ = m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce})
		m.invalidateTranscript()
		return m, nil
	case choiceDeny:
		_ = m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalDeny})
		m.invalidateTranscript()
		return m, nil
	case choiceAlways:
		// The adapter saves the allow rule to the user config and applies it to
		// the session, or adds a session rule when the save fails. A forced
		// prompt never offers this choice, and a forged one is the adapter's
		// deny.
		if err := m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalAlways}); err == nil {
			m.configViewsChanged(m.sess.Config())
		}
		m.invalidateTranscript()
		return m, nil
	case choiceSessionAllow:
		_ = m.settleApproval(a, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalSession})
		m.invalidateTranscript()
		return m, nil
	case choiceEdit:
		// The edit gets its own editor bound to this exact approval; the chat
		// composer, its cursor and its paste attachments are left exactly as
		// the user typed them.
		m.approvalEdit = newApprovalEdit(a, max(m.leftWidth-4, 30))
		m.updateViewportHeight()
		m.invalidateTranscript()
		return m, nil
	case choiceRollback:
		if st.HasBackup {
			// The adapter restores the backup and logs the audit row, failed or
			// not. The error is ignored here because the row carries it, as the
			// transcript did before.
			_ = m.sess.Rollback(m.ctx)
			m.invalidateTranscript()
			m.refreshViewport()
			// Keep the approval open so the user can then approve/deny the
			// original tool.
			return m, nil
		}
		return m, nil
	default:
		return m, nil
	}
}

// settleApproval answers the approval through the session. The adapter
// arbitrates a late answer against an accepted steering cancellation, applies
// a standing choice's rule on the session, and refuses a key that names no
// open approval; the error is returned so a caller that needs it can see it.
func (m *Model) settleApproval(a acpwire.ApprovalRequest, answer acpwire.ApprovalAnswer) error {
	return m.sess.AnswerApproval(m.ctx, a.Key, answer)
}

// handleQuestion routes messages to the inline question form while a
// clarifying question is pending. On completion it answers through the
// session. Escape is an explicit skip rather than a blanket abort: the form
// finalises the questions it has already advanced past and records the
// focused question and every later one as acpwire.AnswerUnanswered (see
// questionModel.skipRemaining), so a partial answer set is what the session
// answers with. Live input on the focused question — a highlighted default,
// half-typed text, an unsubmitted toggle — is not an answer and is never
// copied.
func (m Model) handleQuestion(msg tea.Msg, q acpwire.QuestionRequest) (tea.Model, tea.Cmd) {
	if m.questionModel == nil || m.questionModel.q.Key != q.Key {
		// Build the form for the question that is pending now — and rebuild it
		// when the open form is bound to a round-trip that settled or was
		// interrupted out from under it. Driving the stale form would answer
		// the replacement with the previous question's answers (parity with
		// the approval/skill-gate/sudo pending-identity guards).
		m.questionModel = newQuestionModel(q, max(m.leftWidth-4, 30))
		return m, m.questionModel.Init()
	}
	qm, cmd := m.questionModel.Update(msg)
	m.questionModel = qm
	if !m.questionModel.IsDone() {
		return m, cmd
	}

	// The session is the one place that settles the request: it arbitrates a
	// late answer against an accepted steering cancellation and clears only a
	// winning claim. The form closes either way, so the error is not acted on.
	_ = m.sess.AnswerQuestion(m.ctx, q.Key, m.questionModel.Answers())
	// The form has its own input: settling it must leave the composer exactly
	// as the user left it — text, cursor, placeholder and paste attachments.
	// Clearing here would discard an unrelated draft the user was writing
	// when the question arrived.
	m.questionModel = nil
	m.updateViewportHeight()
	m.invalidateTranscript()
	return m, nil
}

// handleSkillGate routes messages to the inline skill-gate dialog while a
// skill load gate is pending. On completion it answers through the session,
// which responds on the gate and records a per-skill or allow-all choice so
// the runner's gate sees it on the next attempt.
func (m Model) handleSkillGate(msg tea.Msg, sg acpwire.SkillGateRequest) (tea.Model, tea.Cmd) {
	if m.skillGateModel == nil || m.skillGateModel.sg.Key != sg.Key {
		// Build lazily — and rebuild when the open dialog is bound to a
		// pending request that was superseded while it was up (parity with
		// the approval/question pending-identity guards, F-BUG-51 class).
		m.skillGateModel = newSkillGateModel(sg, max(m.leftWidth-4, 30))
		return m, nil
	}
	gm, cmd := m.skillGateModel.Update(msg)
	m.skillGateModel = gm
	if !gm.IsDone() {
		return m, cmd
	}
	// The adapter records the choice: the sticky-skill record belongs inside
	// the settlement, so a decision that lost to an accepted steering
	// cancellation never records a bypass for a gate it cancelled.
	_ = m.sess.AnswerSkillGate(m.ctx, sg.Key, gm.Choice())
	m.skillGateModel = nil
	m.refreshViewport()
	return m, nil
}

// handleSudoAuth routes messages to the inline masked password prompt while a
// privileged-command authentication is pending. The decision goes to the
// session, which hands it to the handler; the password lives only in that
// answer and is zeroed after use.
func (m Model) handleSudoAuth(msg tea.Msg, sa acpwire.SudoRequest) (tea.Model, tea.Cmd) {
	if m.sudoAuthModel == nil || m.sudoAuthModel.sa.Key != sa.Key {
		// Build lazily — and rebuild when the open form is bound to a request
		// that was superseded while it was up (parity with the
		// approval/question/skill-gate pending-identity guards). A freshly
		// built prompt starts its review at the top.
		m.sudoAuthModel = newSudoAuthModel(sa, max(m.leftWidth-4, 30))
		m.syncPromptReviewHeights()
	}
	// msg is applied exactly once, including on the build path above: the
	// prompt appears asynchronously, so the user is often already typing when
	// it opens. Dropping that first keystroke would silently corrupt the
	// password (and burn one of only three attempts).
	sm, cmd := m.sudoAuthModel.Update(msg)
	m.sudoAuthModel = sm
	if !sm.IsDone() {
		return m, cmd
	}
	decision := sm.Decision()
	m.sudoAuthModel = nil
	// The session settles the prompt: a canceled one delivers an empty
	// decision, so a password typed into a prompt the interruption already
	// settled never reaches the privileged runner.
	_ = m.sess.AnswerSudo(m.ctx, sa.Key, decision)
	m.refreshViewport()
	return m, nil
}

// scrollTranscript routes viewport scroll gestures to the transcript
// viewport so the user can review history while an approval or question
// panel is open. It returns (model, cmd, true) when the message was handled.
func (m *Model) scrollTranscript(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg.(type) {
	case tea.MouseWheelMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		cmd, handled := m.handleTranscriptMouse(msg)
		return *m, cmd, handled
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "pgup":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			m.viewportFollow = false
			return *m, vpCmd, true
		case "pgdown":
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return *m, vpCmd, true
		case "ctrl+u":
			m.viewport.HalfPageUp()
			m.viewportFollow = false
			return *m, nil, true
		case "ctrl+d":
			m.viewport.HalfPageDown()
			if m.viewport.AtBottom() {
				m.viewportFollow = true
			}
			return *m, nil, true
		case "end":
			m.viewport.GotoBottom()
			m.viewportFollow = true
			return *m, nil, true
		}
	}
	return *m, nil, false
}

// inputChromeRows counts the rows the input area reserves for everything
// except the textarea itself: its console headers, the SDD hint,
// question/approval panels, and the completion popup. Splitting this out of
// inputAreaRows breaks the circularity in the MaxHeight budget (available rows
// must not depend on the textarea's own height).
func (m Model) inputChromeRows() int {
	// While the composer owns the input, the base input area renders only its
	// suspended notice: the pending prompts, the completion popup and the base
	// textarea belong to the composer now, so reserving rows for them would
	// over-budget the transcript.
	if m.steeringComposer != nil {
		return 1 + m.inputHeaderRows()
	}
	rows := 0
	st := m.sess.Status()
	if st.SDD.Active {
		rows++ // SDD hint row
	}
	// One Pending read for the three prompts this budget mirrors.
	pending := m.sess.Pending()
	if sa := pending.Sudo; sa != nil {
		content := ""
		if m.sudoAuthModel != nil {
			content = m.sudoAuthModel.View()
		} else {
			content = renderSudoAuthPanel(*sa, max(m.leftWidth-4, 1))
		}
		rows += len(consoleLines(content))
	} else if sg := pending.SkillGate; sg != nil {
		content := ""
		if m.skillGateModel != nil {
			content = m.skillGateModel.View()
		} else {
			content = renderSkillGatePanel(*sg, max(m.leftWidth-4, 1))
		}
		rows += len(consoleLines(content))
	} else if q := pending.Question; q != nil {
		content := ""
		if m.questionModel != nil {
			content = m.questionModel.View()
		} else {
			content = renderQuestionPanel(*q, max(m.leftWidth-4, 1))
		}
		rows += len(consoleLines(content))
	} else if a, ok := m.pendingApprovalDisplay(); ok {
		// Mirror inputSections' switch exactly so the budget never disagrees
		// with what is on screen.
		var content string
		switch {
		case a.Access:
			// A typed access request is presented by the accesspanel, which
			// owns the decision. When another panel holds the dock the request
			// is queued: disclose it without rendering a second approve/deny
			// form, and leave the main draft untouched.
			if m.dock.IsOpen() && !m.accessPanelOwnsDock() {
				content = mutedStyle().Render(accessWaitingRow)
			}
		case m.editingApproval():
			// The ❯ prompt is rendered inside the isolated edit editor by
			// SetPromptFunc, so its View already includes it — do not prepend
			// it again. The chat textarea is not rendered while editing.
			content = m.approvalEdit.input.View()
		case m.approvalModel != nil:
			content = m.approvalModel.View()
		default:
			content = renderApprovalPanel(a, st.Sandbox, max(m.leftWidth-4, 1))
		}
		rows += len(consoleLines(content))
	} else if m.doctorSecret != nil {
		// Mirror inputSections' doctor case exactly: the masked key field and
		// its hint row, before the composer and after the prompt chain.
		rows += len(m.doctorSecretLines())
	}
	if p := m.activeCompletionPopup(); p != nil {
		// Cap the popup at completionPopupMax visible match rows plus the
		// console header row (matches completionSection's rows).
		rows += min(len(p.matches()), completionPopupMax) + completionPanelChromeRows
	}
	return rows + m.inputHeaderRows()
}

func (m Model) inputAreaRows() int {
	rows := m.inputChromeRows()
	if m.steeringComposer != nil {
		// The composer's own panel height is budgeted by the dock; the base
		// input area below it renders only its suspended notice row.
		return rows
	}
	pending := m.sess.Pending()
	if pending.Question == nil && len(pending.Approvals) == 0 && pending.SkillGate == nil &&
		pending.Sudo == nil && m.doctorSecret == nil {
		// DynamicHeight clamps Height() to [MinHeight, MaxHeight], so the
		// only guard needed is the max(..., 1) floor.
		rows += max(m.input.Height(), 1)
	}
	return rows
}

// maxInputHeight is the row budget for the chat textarea: whatever the
// terminal leaves after the transcript frame, status line, auxiliary
// panels, input chrome, and the transcript floor. Always at least 1 so the
// input never becomes untypable on short terminals.
func (m Model) maxInputHeight() int {
	return max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.drillCloseRows()-statusLineRows-m.boxRows()-m.dockRows()-m.inputChromeRows()-minTranscriptRows, 1)
}

// scrollHintRows reports the rows the "↑ scrolled — End to follow" hint
// occupies above the transcript: 1 while the user has scrolled off the
// bottom of an overflowing transcript, 0 otherwise. It is a row of the left
// column like any other, so it must be in the budget — an unbudgeted row
// makes the column taller than the terminal and pushes the input area off
// the bottom of the screen.
func (m Model) scrollHintRows() int {
	if !m.viewportFollow && m.viewport.TotalLineCount() > m.viewport.Height() {
		return 1
	}
	return 0
}

// dockRows reports the rows the docked panel occupied at last render, so the
// transcript viewport shrinks while a panel is open.
func (m Model) dockRows() int { return m.dock.Rows() }

// promptReviewBodyHeight is the number of display rows an approval review body
// may occupy in the current frame.
//
// It reserves the status row, the scroll hint, the dock, one console header,
// the prompt's own pinned control rows, and the transcript floor, then caps the
// body at half the terminal. It deliberately does not consult boxRows: the
// tray's own budget already subtracts inputChromeRows, which includes this
// body, so the tray yields to the prompt instead of the other way round — and
// reading boxRows here would close the boxMaxRows → inputChromeRows → body
// height → boxMaxRows cycle.
//
// A result of zero means the body cannot fit: the prompt is then shown but is
// not actionable (see promptActionable).
func (m Model) promptReviewBodyHeight(controlRows int) int {
	avail := m.height - statusLineRows - m.scrollHintRows() - m.dockRows() -
		1 - controlRows - minTranscriptRows
	return max(min(avail, max(m.height/2, 1)), 0)
}

// syncPromptReviewHeights stores the review body height the next render will
// use, so the prompt's own row count and every budget that reads it agree.
// Callers must run it before querying a row budget.
func (m *Model) syncPromptReviewHeights() {
	if m.approvalModel != nil {
		m.approvalModel.SetBodyHeight(m.promptReviewBodyHeight(approvalControlRows()))
	}
	if m.sudoAuthModel != nil {
		m.sudoAuthModel.SetBodyHeight(m.promptReviewBodyHeight(sudoControlRows()))
	}
}

// promptFits reports whether a pending prompt with controlRows pinned rows is
// on screen and can accept a decision: the terminal is at least the supported
// minimum, no full-frame dock owns the frame, and the review body has at least
// one row.
func (m Model) promptFits(controlRows int) bool {
	if m.rawWidth < minTerminalWidth || m.rawHeight < minTerminalHeight {
		return false
	}
	if m.dock.FullFrameOpen() {
		return false
	}
	return m.promptReviewBodyHeight(controlRows) >= 1
}

// promptActionable reports whether the pending approval is on screen and can
// accept a decision.
func (m Model) promptActionable() bool { return m.promptFits(approvalControlRows()) }

// sudoActionable reports whether the pending privileged-auth prompt is on
// screen and can accept a decision. A prompt the user cannot see must never
// read a password byte or authenticate.
func (m Model) sudoActionable() bool { return m.promptFits(sudoControlRows()) }

// handleApprovalPageKey pages the approval's review body on PgUp/PgDn. Only the
// page keys move the review; Ctrl+U/Ctrl+D, End and the mouse keep scrolling
// the transcript, and the edit sub-mode keeps every key for the textarea.
func (m *Model) handleApprovalPageKey(msg tea.Msg) bool {
	am := m.approvalModel
	if am == nil || m.editingApproval() {
		return false
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	var direction int
	switch k.String() {
	case "pgup":
		direction = -1
	case "pgdown":
		direction = 1
	default:
		return false
	}
	am.review.Page(promptReviewRows(am.reviewLines(), am.contentWidth()), am.bodyHeight, direction)
	return true
}

// handleSudoPageKey pages the privileged prompt's review body on PgUp/PgDn.
// Only the page keys move the review; Ctrl+U/Ctrl+D, End and the mouse keep
// scrolling the transcript, and every other key belongs to the masked field.
func (m *Model) handleSudoPageKey(msg tea.Msg) bool {
	sm := m.sudoAuthModel
	if sm == nil {
		return false
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	var direction int
	switch k.String() {
	case "pgup":
		direction = -1
	case "pgdown":
		direction = 1
	default:
		return false
	}
	sm.review.Page(promptReviewRows(sm.reviewLines(), sm.contentWidth()), sm.bodyHeight, direction)
	return true
}

func (m *Model) updateViewportHeight() bool {
	m.syncPromptReviewHeights()
	m.input.MaxHeight = m.maxInputHeight()
	newViewportHeight := max(m.height-transcriptFrameRows-m.scrollHintRows()-m.breadcrumbRows()-m.drillCloseRows()-m.boxRows()-m.dockRows()-m.inputAreaRows()-statusLineRows, 1)
	if newViewportHeight == m.viewport.Height() {
		return false
	}
	m.viewport.SetHeight(newViewportHeight)
	return true
}

// updateCompletionPopups inspects the current input value and updates the
// command, file, and /set completion popups. Called from every keystroke.
//
// Triggers (F18 R1, R4):
//   - `/` at position 0 with no space in the typed value → cmdPopup filters
//     against the commands registry.
//   - `@` at a word start (preceded by start-of-input or whitespace) with
//     no whitespace after the `@` → filePopup filters against the repo
//     file index.
//   - `/set ` → setPopup filters setting keys, then selectable enum values.
//
// The popups are mutually exclusive. When the input doesn't match a trigger,
// every popup is dismissed.
func (m *Model) updateCompletionPopups() {
	if m.cmdPopup == nil || m.filePopup == nil {
		return
	}
	value := m.input.Value()
	// Esc dismissed the popup; keep it suppressed until the input is cleared.
	// This prevents editing the same trigger from immediately re-showing it.
	if m.completionSuppressed {
		if value == "" {
			m.completionSuppressed = false
		} else {
			return
		}
	}
	// Idempotency guard: the default Update path calls this on every
	// non-handled message (mouse, spinner tick, KeyReleaseMsg, paste
	// echo, etc.) and the popup's update() always resets index to 0.
	// Without this, holding the down arrow would visibly snap the
	// selector back to the top between every key repeat. Skip when the
	// input hasn't actually changed.
	if value == m.lastInputForPopups {
		return
	}
	m.lastInputForPopups = value

	if rest, ok := strings.CutPrefix(value, "/set "); ok {
		m.updateSetCompletionPopup(rest)
		m.cmdPopup.dismiss()
		m.filePopup.dismiss()
		return
	}

	cmdTrigger, cmdQuery := m.commandTrigger(value)
	if cmdTrigger {
		if len(m.cmdPopup.items) == 0 {
			// Lazily build the command items from the session the first
			// time the user starts typing "/", and rebuild whenever the
			// cache holds nothing: startNewSession clears it because the
			// command list follows the session, and an empty list leaves
			// it empty so the next trigger retries. Commands includes
			// hidden commands — they are runnable and belong in the popup.
			cmds := m.sess.Commands()
			items := make([]completionItem, 0, len(cmds))
			for _, c := range cmds {
				items = append(items, completionItem{
					Text:        c.Name,
					Description: c.Description,
					Kind:        completionCommand,
				})
			}
			m.cmdPopup.items = items
		}
		if cmdQuery == "" {
			// Bare "/" → show every command, unfiltered, so the user can
			// see what's available before typing.
			m.cmdPopup.showAll()
		} else {
			m.cmdPopup.update(cmdQuery)
		}
		m.filePopup.dismiss()
		if m.setPopup != nil {
			m.setPopup.dismiss()
		}
		return
	}

	fileTrigger, fileQuery := m.fileTrigger(value)
	if fileTrigger {
		m.populateFileIndexIfNeeded()
		if len(m.fileIndex) == 0 {
			m.fileIndex = []completionItem{{
				Text:     "(no indexed files — run /index)",
				Kind:     completionFile,
				Disabled: true,
			}}
		}
		if m.filePopup.items == nil || len(m.filePopup.items) == 0 {
			m.filePopup.items = m.fileIndex
		}
		if fileQuery == "" {
			// Bare "@" → show every file item, unfiltered.
			m.filePopup.showAll()
		} else {
			m.filePopup.update(fileQuery)
		}
		m.cmdPopup.dismiss()
		if m.setPopup != nil {
			m.setPopup.dismiss()
		}
		return
	}

	m.cmdPopup.dismiss()
	m.filePopup.dismiss()
	if m.setPopup != nil {
		m.setPopup.dismiss()
	}
}

func (m *Model) updateSetCompletionPopup(rest string) {
	reg := m.settingsRegistry()
	items := []completionItem{}
	query := rest

	if keyEnd := strings.IndexAny(rest, " \t\n"); keyEnd >= 0 {
		key := rest[:keyEnd]
		query = strings.TrimSpace(rest[keyEnd:])
		if _, _, options, err := reg.Describe(key); err == nil {
			for _, option := range options {
				items = append(items, completionItem{Text: option, Kind: completionSetting})
			}
		}
	} else {
		for _, key := range reg.Keys() {
			_, current, _, _ := reg.Describe(key)
			items = append(items, completionItem{
				Text:        key,
				Description: current,
				Kind:        completionSetting,
			})
		}
	}

	m.setPopup = newCompletionPopup(items)
	if query == "" {
		m.setPopup.showAll()
		return
	}
	m.setPopup.update(query)
}

// commandTrigger returns (true, query) when value is a slash command in
// progress: starts with "/", has no whitespace (so we're still typing
// the command name, not its arguments), and is non-empty after the "/".
//
// Argument completion is stateful: only inputs carrying the exact prefix
// of a just-accepted command (e.g. "/plan ") re-trigger the popup. There
// is no per-command argument metadata, so the armed popup shows the
// command list itself. Inputs that merely begin with an English word that
// happens to be a command name ("run the tests") never trigger — the old
// registry-prefix loop did exactly that and was removed.
func (m *Model) commandTrigger(value string) (bool, string) {
	if m.cmdArgMode {
		if strings.HasPrefix(value, m.cmdArgPrefix) {
			return true, ""
		}
		m.cmdArgMode = false
		m.cmdArgPrefix = ""
	}
	if !strings.HasPrefix(value, "/") {
		return false, ""
	}
	// "/plan " is committed — no longer a trigger.
	if strings.Contains(value, " ") || strings.Contains(value, "\n") {
		return false, ""
	}
	rest := strings.TrimPrefix(value, "/")
	if rest == "" {
		// A bare "/" shows the full command list (query "").
		return true, ""
	}
	return true, rest
}

// fileTrigger returns (true, query) when value contains an @-reference
// at a word start (preceded by start-of-input or whitespace) and the
// current word after "@" has no whitespace.
func (m *Model) fileTrigger(value string) (bool, string) {
	idx := strings.LastIndex(value, "@")
	if idx < 0 {
		return false, ""
	}
	// "@" must be at a runner-compatible trigger boundary.
	if idx > 0 {
		prev := value[idx-1]
		if !isAtFileBoundary(prev) {
			return false, ""
		}
	}
	// No whitespace between the "@" and end of value (otherwise the
	// user has already moved past the trigger and onto the next word).
	after := value[idx+1:]
	if containsRunnerWhitespace(after) {
		return false, ""
	}
	return true, after
}

func isAtFileBoundary(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return false
}

// populateFileIndexIfNeeded lazy-loads the repo file index on the first
// @-keystroke. Skipped when the model was constructed with WithFileIndex
// (eager seed); otherwise the session supplies the paths.
func (m *Model) populateFileIndexIfNeeded() {
	if m.fileIndexLoaded {
		return
	}
	paths, err := m.sess.FileIndex(m.ctx)
	if err != nil {
		// No way to populate; mark loaded so we don't keep retrying.
		m.fileIndexLoaded = true
		return
	}
	m.fileIndex = buildFileIndexItems(paths)
	m.fileIndexLoaded = true
}

// activeCompletionPopup returns whichever popup is currently visible
// (/set takes precedence when both somehow show), or nil when none is up.
// Used by the keypress switch to route Up/Down/Tab/Esc.
func (m *Model) activeCompletionPopup() *completionPopup {
	if m.setPopup != nil && m.setPopup.isVisible() {
		return m.setPopup
	}
	if m.cmdPopup != nil && m.cmdPopup.isVisible() {
		return m.cmdPopup
	}
	if m.filePopup != nil && m.filePopup.isVisible() {
		return m.filePopup
	}
	return nil
}

func (m *Model) dismissCompletionPopups() {
	if m.cmdPopup != nil {
		m.cmdPopup.dismiss()
	}
	if m.filePopup != nil {
		m.filePopup.dismiss()
	}
	if m.setPopup != nil {
		m.setPopup.dismiss()
	}
}

// completionShouldSubmit reports whether Enter on the active popup should
// accept the selection and immediately submit the resulting input (true
// for command popups and setting-value popups), or accept only and keep
// editing (false for file-path popups and setting-key popups).
//
// Setting keys need a value after them ("/set shell.allow_network <value>"),
// so selecting a key with Enter keeps editing. Setting values are complete
// commands ready to submit. File paths are typically part of a larger
// message, so Enter on a file popup keeps editing too.
//
// Must be called before acceptCompletion (which dismisses the popup).
func (m *Model) completionShouldSubmit(p *completionPopup) bool {
	if p == nil || len(p.filtered) == 0 || p.index < 0 || p.index >= len(p.filtered) {
		return false
	}
	chosen := p.filtered[p.index]
	if chosen.Disabled {
		return false
	}
	if chosen.Kind == completionCommand {
		return true
	}
	if chosen.Kind == completionSetting {
		// Distinguish setting-key from setting-value completion by
		// checking whether the part after "/set " already contains
		// a space. "/set shell" → key (no space after the key, accept
		// only). "/set shell.allow_network " → value (space after
		// the key, submit the complete command).
		v := m.input.Value()
		if strings.HasPrefix(v, "/set ") {
			rest := v[len("/set "):]
			return strings.ContainsAny(rest, " \t\n")
		}
		return false
	}
	// File paths keep editing.
	return false
}

// acceptCompletion accepts the active popup's current selection, replacing
// the trigger token in the input with the popup's acceptedText. Returns
// true when a popup was visible (and is now dismissed).
func (m *Model) acceptCompletion() bool {
	p := m.activeCompletionPopup()
	if p == nil {
		return false
	}
	p.accept()
	m.completionSuppressed = false
	accepted := p.acceptedText
	if accepted == "" {
		// Disabled / placeholder items produce no accepted text — just
		// dismiss the popup without mutating the input.
		m.dismissCompletionPopups()
		m.updateViewportHeight()
		return true
	}
	value := m.input.Value()
	newValue := replaceTriggerToken(value, accepted)
	if p == m.setPopup {
		newValue = replaceSetCompletionToken(value, accepted)
	}
	// Arm argument completion for the accepted command: while the input
	// keeps this exact prefix, commandTrigger stays hot (see
	// commandTrigger). File and setting accepts never arm it.
	if p == m.cmdPopup && strings.HasPrefix(newValue, "/") {
		m.cmdArgMode = true
		m.cmdArgPrefix = newValue
	}
	m.input.SetValue(newValue)
	// Move the cursor to the end of the inserted text so the user can
	// keep typing args / a trailing space directly.
	m.input.MoveToEnd()
	m.dismissCompletionPopups()
	m.updateViewportHeight()
	return true
}

func replaceSetCompletionToken(value, replacement string) string {
	if !strings.HasPrefix(value, "/set ") {
		return value
	}
	index := strings.LastIndexAny(value, " \t\n")
	if index < 0 {
		return value
	}
	return value[:index+1] + replacement
}

// replaceTriggerToken finds the most recent trigger token in value
// (either the leading "/<cmd...>" word or the last "<sep>@<path...>"
// word at a word boundary) and replaces it with replacement. The cursor
// stays in the same word after replacement.
func replaceTriggerToken(value, replacement string) string {
	if value == "" {
		return replacement
	}
	// Command trigger: value starts with "/".
	if strings.HasPrefix(value, "/") && !strings.ContainsAny(value, " \t\n") {
		return replacement
	}
	// File trigger: find the last "@<...>" at a word start, and consume
	// any immediately-preceding "@"s as part of the trigger. F-SEC-28.
	idx := strings.LastIndex(value, "@")
	if idx < 0 {
		return value
	}
	// Walk back over any preceding "@"s in the same run.
	start := idx
	for start > 0 && value[start-1] == '@' {
		start--
	}
	if start > 0 {
		prev := value[start-1]
		if !isAtFileBoundary(prev) {
			return value
		}
	}
	return value[:start] + replacement
}

// popOldestSteering returns and removes the oldest queued steering
// message. Used by the F16 R2 follow-up path so each Enter pops one
// item at a time; the runner's loop-top drain handles any remainder.
func (m *Model) popOldestSteering() (string, bool) {
	return m.sess.PopSteering(m.ctx)
}

// drilledInto returns the subagent whose transcript is currently drilled
// into (top of viewStack), or false when the viewport shows the
// orchestrator's own transcript.
func (m *Model) drilledInto() (acpwire.Subagent, bool) {
	if len(m.viewStack) == 0 {
		return acpwire.Subagent{}, false
	}
	return m.viewStack[len(m.viewStack)-1], true
}

// drillCrumb is the drilled-in child's identity line: the orchestrator, the
// child's role and label, and its route.
func drillCrumb(v acpwire.Subagent) string {
	label := v.Label
	if v.Role != "" {
		label = v.Role + " — " + label
	}
	if v.Model != "" {
		if v.Provider != "" {
			label += fmt.Sprintf(" · %s @ %s", v.Model, v.Provider)
		} else {
			label += " · " + v.Model
		}
	}
	if v.Fallback {
		label += " (fallback)"
	}
	return lipgloss.NewStyle().Foreground(accentColor).Render(glyph.Brand+" orchestrator") +
		mutedStyle().Render(" "+glyph.Running+" ") +
		lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(glyph.Agent+" "+label)
}

// renderDrillFrame draws a drilled-in child's transcript as a closed coral
// frame: the crumb in the top border with the way back on its right, the
// scroll hint and the viewport lines inside "│ … │", then the closing rule. The
// frame closes the region, so the first row under it — the tray or the console
// — opens a new outline.
//
// It lives here rather than in view.go because the boundary test keeps
// view.go off the agent side (P2 C1).
func (m Model) renderDrillFrame(v acpwire.Subagent) string {
	border := lipgloss.NewStyle().Foreground(accentColor)
	inner := max(m.leftWidth-4, 1)
	body := func(row string) string {
		row = ansi.Truncate(row, inner, "…")
		row += strings.Repeat(" ", max(inner-ansi.StringWidth(row), 0))
		return border.Render("│ ") + row + border.Render(" │")
	}

	lines := make([]string, 0, m.viewport.Height()+3)
	lines = append(lines, chrome.BorderLine("╭─ ", drillCrumb(v), mutedStyle().Render("↑/Esc to go back"), "╮", m.leftWidth, accentColor))
	if m.scrollHintRows() > 0 {
		lines = append(lines, body(mutedStyle().Render("↑ scrolled — End to follow")))
	}
	for _, line := range strings.Split(m.paintSelection(m.viewport.View()), "\n") {
		lines = append(lines, body(line))
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", max(m.leftWidth-2, 1))+"╯"))
	return chrome.PaintBand(strings.Join(lines, "\n"), m.leftWidth, theme.Current().TranscriptBG())
}

// drillIntoSubagent pushes the subagent with that ID onto the view stack, so
// its transcript replaces the orchestrator's. It reports false and changes
// nothing for an ID that names no subagent in Status, or one without Drill.
func (m *Model) drillIntoSubagent(id int64) bool {
	for _, v := range m.sess.Status().Subagents {
		if v.ID != id || v.Drill == nil {
			continue
		}
		m.viewStack = append(m.viewStack, v)
		m.syncTranscriptWidth()
		m.clearSelection()
		m.invalidateTranscript()
		m.viewportFollow = true
		return true
	}
	return false
}

// drillIntoLatestRunningSubagent pushes the most recently registered
// running subagent onto the view stack, so inspecting live work does not
// require a mouse. Returns false when no subagent is running.
func (m *Model) drillIntoLatestRunningSubagent() bool {
	subs := m.sess.Status().Subagents
	for i := len(subs) - 1; i >= 0; i-- {
		if subs[i].Status != acpwire.SubagentRunning {
			continue
		}
		if m.drillIntoSubagent(subs[i].ID) {
			return true
		}
	}
	return false
}

// popDrill pops one level of subagent drill-down, returning false when the
// viewport was already showing the orchestrator's transcript.
func (m *Model) popDrill() bool {
	if len(m.viewStack) == 0 {
		return false
	}
	m.viewStack = m.viewStack[:len(m.viewStack)-1]
	m.syncTranscriptWidth()
	m.clearSelection()
	m.invalidateTranscript()
	return true
}

// breadcrumbRows reports the rows the drill-down breadcrumb occupies above
// the transcript: 1 while drilled into a subagent, 0 otherwise. It is a
// row of the left column like any other and must be in the height budget
// (see scrollHintRows).
func (m Model) breadcrumbRows() int {
	if len(m.viewStack) > 0 {
		return 1
	}
	return 0
}

// drillCloseRows reports the row a drilled-in frame's closing border takes
// below the viewport: 1 while drilled in, 0 otherwise. The crumb row becomes
// the frame's top border (breadcrumbRows), so only the bottom rule is new.
func (m Model) drillCloseRows() int {
	if len(m.viewStack) > 0 {
		return 1
	}
	return 0
}

// transcriptLeft is the cells the transcript frame's left border occupies on
// every row: 2 inside a drilled-in frame, 0 in the borderless orchestrator
// view.
func (m Model) transcriptLeft() int {
	if len(m.viewStack) > 0 {
		return 2
	}
	return 0
}

// syncTranscriptWidth sets the viewport width for the current drill state and
// invalidates the transcript when it has to change: a drilled-in child is
// framed, so its body is four cells narrower than the column.
func (m *Model) syncTranscriptWidth() {
	width := max(m.leftWidth, 1)
	if len(m.viewStack) > 0 {
		width = max(m.leftWidth-4, 1)
	}
	if width == m.viewport.Width() {
		return
	}
	m.viewport.SetWidth(width)
	m.invalidateTranscript()
}

// openRunPreflight opens the cast list panel for the given kind ("sdd" or
// "swarm") and waits for the user to confirm before starting the run. pf is
// the plan's preflight for "sdd", nil for "swarm".
func (m *Model) openRunPreflight(kind, goal string, pf *acpwire.PlanPreflight) {
	cfg := m.sess.Config()
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	roles := routing.SwarmCastRoles
	title := "Start swarm run?"
	meta := []string{
		"goal: " + strutil.Truncate(goal, 56, true),
		fmt.Sprintf("fix rounds: %d · token budget: %d",
			cfg.Swarm.Budget.MaxFixRounds, cfg.Swarm.Budget.MaxTotalTokens),
	}
	if kind == "sdd" {
		roles = routing.SDDCastRoles
		title = "Start plan run?"

		// Lead with what the run will do to the repository. The model
		// roster matters less than the fact that this is an autonomous,
		// multi-commit run against a branch the user has not seen.
		tasks := "unknown task count"
		if plans, err := m.sess.Plans(m.ctx); err == nil {
			for _, c := range plans {
				if c.Path != goal {
					continue
				}
				if c.Error == "" {
					tasks = fmt.Sprintf("%d tasks", c.Tasks)
					if c.LedgerError == "" && c.Done > 0 {
						tasks += fmt.Sprintf(" · resuming at %d", c.Done+1)
					}
				}
			}
		}
		// Use the shared inspection for strategy selection and metadata.
		worktree := "off"
		if cfg.SDD.AutoWorktree {
			worktree = "on"
		}
		meta = []string{
			fmt.Sprintf("%s · %s", strutil.Truncate(filepath.Base(goal), 40, true), tasks),
			"one commit per task, on a branch — review and merge it yourself",
			fmt.Sprintf("worktree: %s · fix rounds: %d · verify timeout: %dms",
				worktree, cfg.SDD.MaxFixRounds, cfg.SDD.VerifyTimeoutMS),
		}
		// The session's preflight already inspected the plan: render what it
		// reports instead of re-inspecting here.
		if pf != nil {
			meta = append(meta, fmt.Sprintf("deterministic ops: %d · fallback ops: %d · est. model calls: %d",
				pf.Inspection.DetOps, pf.Inspection.AgentOps, pf.Inspection.EstCalls))
			for _, tr := range pf.Inspection.Tasks {
				meta = append(meta, fmt.Sprintf("task %d: %s · %d det · %d agent · %d call(s)",
					tr.TaskN, tr.Title, tr.DetOps, tr.AgentOps, tr.EstCalls))
			}
		}
	}
	rows := make([]castlist.Row, 0, len(roles))
	for _, entry := range router.Cast(roles) {
		row := castlist.Row{
			Title: strings.ReplaceAll(string(entry.Role), "_", " "),
			Role:  entry.Role,
		}
		if entry.Err != nil {
			row.Err = strutil.Truncate(entry.Err.Error(), 44, true)
		} else {
			row.Detail = entry.Route.Preset.Provider + "/" + entry.Route.Preset.Model
			row.Badge = entry.Route.Preset.Name
		}
		rows = append(rows, row)
	}
	if kind == "sdd" {
		// A nil preflight leaves the gate unresolved: the row warns instead of
		// claiming commands the run does not have.
		gate := acpwire.VerifyGate{}
		if pf != nil {
			gate = pf.Verify
		}
		rows = append(rows, verifyGateRow(gate))
	}
	m.pendingRun = &pendingAgentRun{goal: goal, kind: kind}
	panel := castlist.New(title, rows, meta, "agent")
	// Provide the config's model presets so the castlist's in-panel
	// override picker can offer them.
	presetNames := make([]string, 0, len(cfg.Models.Presets))
	for n := range cfg.Models.Presets {
		presetNames = append(presetNames, n)
	}
	sort.Strings(presetNames)
	pickerItems := make([]picker.Item, 0, len(presetNames))
	for _, name := range presetNames {
		preset := cfg.Models.Presets[name]
		pickerItems = append(pickerItems, picker.Item{Label: name, Detail: preset.Provider + "/" + preset.Model, Value: name})
	}
	panel.SetPickerItems(pickerItems)
	if kind == "sdd" && pf != nil {
		// The session reports the effective strategy: the explicit one when
		// /sdd named one, and otherwise adaptive or agent by blocks. It also
		// owns the disabled reasons.
		options := []castlist.StrategyOption{
			{Value: acpwire.StrategyAgent},
			{Value: acpwire.StrategyAdaptive},
			{Value: acpwire.StrategyStrict},
		}
		if !pf.Inspection.HasBlocks {
			options[1].DisabledReason = "no executable blocks found"
			options[2].DisabledReason = "no executable blocks found"
		} else if pf.Inspection.StrictBlocked {
			options[2].DisabledReason = "fallback or unresolved work remains"
		}
		initial := pf.Inspection.Strategy
		if initial == "" {
			initial = acpwire.StrategyAgent
		}
		panel.SetStrategyOptions(options)
		panel.SetStrategy(initial)
	}
	m.dock.Open(panel)
}

// verifyGateRow renders the verify-gate row for the /sdd preflight from the
// gate the session resolved, so the UI always shows what will run.
func verifyGateRow(v acpwire.VerifyGate) castlist.Row {
	row := castlist.Row{Title: "verify gate"}
	if !v.Known {
		row.Warn = "no build or test command configured — press f to propose one"
		return row
	}
	detail := strings.TrimSpace(v.Build + " · " + v.Test)
	if v.Detected {
		detail += " (detected)"
	}
	row.Detail = strutil.Truncate(detail, 44, true)
	return row
}

// roleNames converts the cast list's per-run overrides to the wire's
// role-name keys; nil when empty.
func roleNames(overrides map[routing.AgentRole]string) map[string]string {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(overrides))
	for role, preset := range overrides {
		out[string(role)] = preset
	}
	return out
}

// beginTurn marks a run as started on the model and returns its context: the
// provider notice is cleared, busy and the turn's start are set, the held
// window starts, the suggestion is reset, and agentCancel cancels the context.
func (m *Model) beginTurn() context.Context {
	// Optimistic dismissal: a fresh prompt means the user is trying again, so
	// clear any stale provider notice from an earlier failed turn. If this
	// turn also fails, handleAgentFinished re-sets it.
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
	m.busy = true
	m.turnStartedAt = m.now()
	m.startHeldWindow()
	// A fresh prompt means the user is acting on their own — clear any
	// stale ghost suggestion so it never survives into the new turn, and
	// bump the generation counter so an in-flight LLM fallback result is
	// discarded.
	m.suggestion = ""
	m.suggestionDismissed = false
	m.suggestionGen++
	agentCtx, cancel := context.WithCancel(m.ctx)
	m.agentCancel = cancel
	return agentCtx
}

// startSessionRun starts a run the session owns. The session takes the run's
// work membership, its stop group and the idle reset at its end, so the model
// only marks the turn started and holds the context that a local cancel
// reaches. refusal is the note a refused start writes.
func (m *Model) startSessionRun(refusal string, run func(context.Context) error) (tea.Model, tea.Cmd) {
	if m.endStage != endIdle {
		m.note("The conversation is ending; this request was not started.")
		m.busy = false
		m.refreshViewport()
		return *m, nil
	}
	ctx := m.beginTurn()
	return *m, tea.Batch(sessionRunCmd(ctx, refusal, run), tickCmd(), spinnerTickCmd())
}

// sessionRunCmd runs run under ctx and reports its end.
func sessionRunCmd(ctx context.Context, refusal string, run func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		return agentFinishedMsg{err: run(ctx), refusal: refusal}
	}
}

// startPrompt starts a prompt turn through the session.
func (m *Model) startPrompt(text string, queued bool) (tea.Model, tea.Cmd) {
	sess := m.sess
	return m.startSessionRun("Cannot start work", func(ctx context.Context) error {
		_, err := sess.Prompt(ctx, acpwire.PromptParams{Text: text, Queued: queued})
		return err
	})
}

// refusedStart reports whether err says the run never started: CodeBusy,
// CodeQuiescing, CodeNoRunner or CodeSessionClosed.
func refusedStart(err error) bool {
	code, ok := acpclient.CodeOf(err)
	if !ok {
		return false
	}
	switch code {
	case acpwire.CodeBusy, acpwire.CodeQuiescing, acpwire.CodeNoRunner, acpwire.CodeSessionClosed:
		return true
	}
	return false
}

// startWatchResumeRun checks the auto-resume conditions (a built runner,
// not busy, no open dock panel, config gate on) and, when all hold,
// starts the auto-resume turn, returning its cmd. It returns nil when
// any guard fails or the session refuses the start (ending, quiescing) —
// the caller proceeds with its normal tail. The *m assignment is what makes
// the post-start state (busy flag, suggestion clear, agentCancel) stick for
// the value-receiver callers. handleWatchMsg passes name/repeat from the
// event; handleAgentFinished from the latch.
func (m *Model) startWatchResumeRun(name string, repeat bool) tea.Cmd {
	if !m.sess.Status().AgentReady || m.busy || m.dock.IsOpen() || !m.sess.Config().Watch.ResumeEnabled {
		return nil
	}
	sess := m.sess
	model, cmd := m.startSessionRun("Cannot start work", func(ctx context.Context) error {
		_, err := sess.Prompt(ctx, acpwire.PromptParams{
			WatchResume: &acpwire.WatchResume{Name: name, Repeat: repeat},
		})
		return err
	})
	if cmd == nil {
		return nil
	}
	*m = model.(Model)
	return cmd
}

// flushQueuedSteering delivers text the user queued while a turn was running.
// The runner drains the queue at every loop-top, so anything still queued when
// the turn ends was typed after that final drain — during the final answer —
// and would otherwise sit in the queue until the user pressed Enter on an
// empty composer. One entry is popped and started as the next turn; the
// runner's loop-top drain delivers any remainder, exactly as the Enter
// follow-up path does.
//
// It returns nil when there is nothing to deliver or the turn cannot start (no
// runner, an ending in progress, a quiescing session). A start the session
// refuses puts the text back on the steering queue agent side, so it is never
// silently lost.
func (m *Model) flushQueuedSteering() tea.Cmd {
	if !m.sess.Status().AgentReady || m.busy || m.endStage != endIdle {
		return nil
	}
	followUp, ok := m.popOldestSteering()
	if !ok {
		return nil
	}
	model, cmd := m.startPrompt(followUp, true)
	if cmd == nil {
		return nil
	}
	*m = model.(Model)
	return cmd
}

// startSDDAuthoring begins an authoring turn for /sdd new. It builds the
// scoped authoring runner, resolves the candidate path, and starts the
// async command. On success the completion handler opens the review panel;
// it never starts an SDD run.
func (m *Model) startSDDAuthoring(parsed parsedSDDArgs) (tea.Model, tea.Cmd) {
	if m.endStage != endIdle {
		// An accepted ending owns the conversation. Plan authoring is an
		// ordinary operation and registers its own work, so it must not start
		// into a frozen membership; reporting it here keeps the user's
		// feedback immediate and specific.
		m.note("The conversation is ending; plan authoring was not started.")
		m.refreshViewport()
		return m, nil
	}
	if !m.sess.Status().AgentReady {
		m.note("Plan authoring is not available (agent failed to initialise).")
		m.refreshViewport()
		return m, nil
	}
	if m.busy {
		return m, nil
	}
	goal := parsed.Goal
	design := goal
	if parsed.FromLastPlan {
		page, err := m.sess.Messages(m.ctx, acpwire.MessageQuery{
			Role: acpwire.RoleAssistant, Final: true, Last: 1,
		})
		if err != nil || len(page.Messages) == 0 {
			m.note("No approved plan found in the last assistant turn. Load the marshal-writing-plans skill, produce an approved plan, then run /sdd new --from-last-plan.")
			m.refreshViewport()
			return m, nil
		}
		design = page.Messages[0].Content
		goal = "Convert the approved plan into an executable SDD plan."
	}
	if strings.TrimSpace(goal) == "" {
		m.note("Usage: /sdd new <goal>")
		m.refreshViewport()
		return m, nil
	}
	// The session resolves the draft path, builds the author and runs it, all
	// agent side (decision 6). The TUI only chooses the design and starts the
	// run on the run path, so authoring gets the same admission, stop group
	// and idle reset as every other run (decision 12).
	sess := m.sess
	ctx := m.beginTurn()
	return *m, tea.Batch(
		planAuthorCmd(ctx, sess, acpwire.AuthorPlanParams{Goal: goal, Design: design}),
		tickCmd(),
		spinnerTickCmd(),
	)
}

// agentFinishedMsg reports a run's end. refusal is the note a refused start
// writes, as "Cannot start work"; it is empty for swarm and SDD runs, which
// the TUI refuses before it starts them.
type agentFinishedMsg struct {
	err     error
	refusal string
}
type agentTickMsg struct{}
type spinnerTickMsg struct{}

// suggestionMsg carries the result of a Phase 2 LLM fallback suggestion
// call. gen is the generation counter captured when the call started; a
// result whose gen is older than the model's current counter is stale and
// discarded.
type suggestionMsg struct {
	suggestion string
	gen        int
}

// runSuggestionFallbackCmd returns a tea.Cmd that invokes the suggestion
// provider for the given last assistant message and reports via
// suggestionMsg. The generation counter is captured at call time so a late
// result can be discarded if the user typed or started a new turn.
func runSuggestionFallbackCmd(sess acpclient.Session, lastMsg string, gen int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s, err := sess.Suggest(ctx, lastMsg)
		if err != nil || strings.TrimSpace(s) == "" {
			return suggestionMsg{suggestion: "", gen: gen}
		}
		return suggestionMsg{suggestion: strings.TrimSpace(s), gen: gen}
	}
}

// verifyProposeMsg carries the result of a one-shot proposal task that
// inspects the repo and proposes build/test commands for the verify gate.
type verifyProposeMsg struct {
	build, test string
	err         error
}

// startVerifyProposal dispatches a one-shot sub-agent that inspects the
// repo and proposes build/test commands, reporting via verifyProposeMsg. It
// runs in the background while the preflight dock stays open; the result is
// surfaced on the verify-gate row for the user to approve or edit.
func (m *Model) startVerifyProposal() tea.Cmd {
	sess, ctx := m.sess, m.ctx
	return func() tea.Msg {
		proposed, err := sess.VerifyProposal(ctx)
		if err != nil {
			return verifyProposeMsg{err: err}
		}
		return verifyProposeMsg{build: proposed.Build, test: proposed.Test}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return agentTickMsg{}
	})
}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// spinnerLabel returns the formatted label with a leading spinner glyph, or
// just the label when the spinner frame is empty. This avoids leading-space
// jitter during the 200ms gate window when activeSpinnerFrame returns "".
func spinnerLabel(spinner, label string) string {
	if spinner == "" {
		return label
	}
	return spinner + " " + label
}

// activeSpinnerFrame returns the current spinner frame glyph if the activity
// has been running for at least 200ms, or "" when the activity just started.
// This avoids a flash of the spinner glyph before the user can perceive the
// activity. For ActivityIdle it always returns "". During an SDD run it
// always returns "": the now bar owns the only spinner on screen, so
// transcript activity rows render their static glyph with elapsed time.
func (m *Model) activeSpinnerFrame(kind string) string {
	if kind == acpwire.ActivityIdle {
		return ""
	}
	st := m.sess.Status()
	if st.SDD.Active {
		return ""
	}
	act := st.Activity
	if m.now().Sub(acpwire.Time(act.StartedAt)) < 200*time.Millisecond {
		return ""
	}
	return m.spinnerFrame
}

// turnSpinnerFrame returns the current spinner glyph for the pinned turn
// spinner, or "" when idle or within the first 200ms of the turn. The 200ms
// gate mirrors activeSpinnerFrame: it avoids a glyph flash on turns too fast
// for the user to perceive. Keyed on turnStartedAt rather than
// Activity.StartedAt so it does not reset at every phase boundary.
func (m Model) turnSpinnerFrame() string {
	if !m.busy || m.turnStartedAt.IsZero() {
		return ""
	}
	if m.now().Sub(m.turnStartedAt) < 200*time.Millisecond {
		return ""
	}
	return m.spinnerFrame
}

// cancelTurn stops the in-flight agent turn, if any. Shared by double
// Ctrl+C and the /stop command. The steering queue is NOT cleared here — that happens
// in handleAgentFinished so the finishing goroutine can observe the
// cancellation state and avoid double-processing.
//
// The order is the contract: the session's run is stopped FIRST, through the
// session, so the command service marks and stops the commands still attached
// to this turn — and refuses the stopped turn any further admission — and only
// then does the agent's own wait/provider context get cancelled. Cancelling
// first would leave a foreground command running with no stop request against
// it. Every run the TUI starts is the session's since plan 5d, so there is no
// TUI-owned registration left to stop.
func (m *Model) cancelTurn() bool {
	if m.agentCancel == nil && !m.busy {
		return false
	}
	m.cancelling = true
	_ = m.sess.Stop(m.ctx, "turn stopped")
	if m.agentCancel != nil {
		m.agentCancel()
		m.agentCancel = nil
	}
	m.refreshViewport()
	return true
}

// beginEnd accepts one interactive end request. It is the single entry point
// for every ending path — /exit, /quit, Ctrl+C, /new, /clear and a resume —
// and it warns before anything is stopped.
//
// The request is a PENDING INTENT: nothing about the target (a resume ID, a new
// session, the announcement) is mutated or announced until the runtime accepts
// the membership the user was shown.
func (m *Model) beginEnd(intent *endIntent) tea.Cmd {
	// Already ending: a repeated quit/Ctrl+C is a retry of the settlement (or
	// an idempotent no-op while the warning is up), never a second warning and
	// never a bypass of the one on screen.
	switch m.endStage {
	case endAwaitingDecision:
		// Refresh the pending intent so a later acceptance acts on the latest
		// request (e.g. a resume picked after a first quit press), then leave
		// the warning on screen. Only the exit question is latched: a
		// replacement keeps its own target.
		if intent != nil && (m.end == nil || m.end.kind == intent.kind) {
			m.applyEndIntent(intent)
		}
		return nil
	case endStopping:
		// The stop/join pass already ran and failed, or is still running: retry
		// it. WaitForEnd is idempotent and never latches success, so this
		// either settles the remaining work or reports the failure again.
		return m.settleEnd()
	case endPostmortem:
		// An in-flight analysis pass owns the exit: it reports through
		// endAnalysisFinishedMsg, so a repeated quit request must not race it
		// with a second question or a second extraction.
		if m.endAnalyzing {
			return nil
		}
		// Likewise while the extraction that precedes it is still running: the
		// report is written by that command, and quitting ahead of it would
		// discard the session's only chance to record itself.
		if m.endExtracting {
			return nil
		}
		// The postmortem question was answered already: a repeated quit must not
		// ask it again.
		if m.endAnswered {
			return m.finishQuit()
		}
		return m.afterSettlement()
	case endFinishing:
		return m.finishQuit()
	}

	m.applyEndIntent(intent)
	// A surface the warning will displace is remembered first, so the user's
	// Esc/keep choice returns them to it: a child parked behind an approval or a
	// gate must not be stranded by an ending they declined.
	if m.dock.IsOpen() {
		m.endDisplaced = m.dock.Panel()
	}
	// The runtime's live membership is authoritative: what a warning renders
	// and what an accepted end stops cannot drift apart. Cached counts and
	// m.busy are deliberately not consulted — a detached job the TUI never
	// tracked would otherwise be destroyed without ever being shown.
	var snap = m.sess.ActiveWork()
	if snapEmpty(snap) {
		// Nothing to stop. No warning is warranted, and the runtime freezes
		// admission for the rest of this pass.
		return m.acceptEnd(nil)
	}
	m.endSnapshot = snap
	m.endStage = endAwaitingDecision
	m.openActiveWorkWarning()
	return nil
}

// handleActiveWorkDone handles the warning panel's answer. "Stop" submits the
// displayed IDs to the runtime's atomic acceptance; "Keep" discards the intent
// and preserves every piece of work exactly as it was. A DoneMsg that arrives
// once the end has moved on (e.g. repeated Enter on a settled warning) is
// ignored, so no panel can bypass or stack the warning.
func (m *Model) handleActiveWorkDone(msg activeworkpanel.DoneMsg) (tea.Model, tea.Cmd) {
	if m.endStage == endPostmortem || m.endStage == endFinishing {
		// The warning already did its job: this is the settled path, and a
		// stray answer must not re-open anything.
		return *m, nil
	}
	if m.endStage == endStopping {
		// The stop/join pass is running: a second answer is idempotent, not a
		// second destructive decision.
		return *m, nil
	}
	if !msg.Stop {
		m.dock.CloseNow()
		return m.declineEnd()
	}
	// The warning STAYS OPEN while work stops, showing what remains: the user
	// asked to stop this set and must be able to see it settle. The stage moves
	// before submission so a repeated answer cannot be a second decision.
	m.endStage = endStopping
	return *m, m.acceptEnd(msg.IDs)
}

// handlePostmortemDone applies the exit-postmortem answer. Extraction is
// dispatched here as a command — never run inline — and the report is built from
// the settled membership, so it sees the final command and child evidence.
func (m *Model) handlePostmortemDone(msg postmortempanel.DoneMsg) (tea.Model, tea.Cmd) {
	m.endAnswered = true
	switch msg.Result {
	case postmortempanel.ResultWithAgent:
		// Extraction runs first, off the render loop; the scoped analysis pass
		// follows it in handlePostmortemWritten.
		return *m, m.extractPostmortemCmd(true)
	case postmortempanel.ResultExtractOnly:
		return *m, m.extractPostmortemCmd(false)
	default: // ResultDeclined
		m.refreshViewport()
		return *m, m.finishQuit()
	}
}

// runExitAnalysis runs the postmortem's agent pass as a SCOPED exit-analysis
// operation through the end controller. The runtime owns the operation's
// temporary elevation (system access and editing approval) and its cleanup, so
// the model never elevates and a failure can never leave an elevated ordinary
// prompt behind. The detached work the pass launches is joined during the
// operation's own phase cleanup, and it cannot recursively trigger the
// active-work warning: analysis work is a separate epoch, not ordinary work.
func (m *Model) runExitAnalysis(path string) tea.Cmd {
	sess := m.sess
	goal := postmortemAgentGoal(path)
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		return endAnalysisFinishedMsg{err: sess.RunExitAnalysis(ctx, goal)}
	}
}

// handleEndAnalysisFinished reports the exit-analysis outcome and quits. It
// NEVER returns the user to an ordinary prompt: the analysis runs after the
// extraction landed, so even a refused or failed pass finishes the exit.
func (m *Model) handleEndAnalysisFinished(msg endAnalysisFinishedMsg) (tea.Model, tea.Cmd) {
	m.endAnalyzing = false
	if msg.err != nil {
		m.note("Postmortem analysis failed: " + firstLine(msg.err.Error()))
	}
	m.refreshViewport()
	return *m, m.finishQuit()
}

// declineEnd discards the pending intent and preserves the work unchanged. A
// surface the warning displaced is restored, so a child parked behind an
// approval or a gate is never stranded by an ending the user declined.
func (m *Model) declineEnd() (tea.Model, tea.Cmd) {
	displaced := m.endDisplaced
	m.endDisplaced = nil
	m.endWarning = nil
	m.end = nil
	m.endSnapshot = acpwire.EndWork{}
	m.endStage = endIdle
	if displaced != nil {
		// Nothing was stopped, so a pending approval or gate is still pending
		// and its surface is still meaningful. It comes back without a close
		// notification: it was retained, never released.
		m.dock.SwapRetained(displaced)
	}
	m.note("Kept open. Active work continues.")
	m.refreshViewport()
	return *m, nil
}

// handleEndSettled applies the outcome of one stop/join pass. On failure the
// pending intent and the stopping stage are kept — the next quit request retries
// the pass — and the error is reported as it is, never as if the work had
// settled.
//
// Only the FIRST settled pass advances the end: a retry that lands after the
// ending has moved on (a repeated quit enqueued a second claim while the first
// was still joining) must not run the postmortem policy twice.
func (m *Model) handleEndSettled(msg endSettledMsg) (tea.Model, tea.Cmd) {
	if m.endStage != endStopping {
		return *m, nil
	}
	if msg.err != nil {
		m.endStage = endStopping
		m.note(fmt.Sprintf("Could not stop all work: %v · press Ctrl+C again to retry.", firstLine(msg.err.Error())))
		m.refreshEndWarning()
		return *m, nil
	}
	// The work settled, so the warning has nothing left to report: close it
	// before the postmortem question or the exit takes the dock.
	if panel, ok := m.dock.Panel().(*activeworkpanel.Panel); ok && panel == m.endWarning {
		m.dock.CloseNow()
	}
	m.endWarning = nil
	m.endStage = endPostmortem
	return *m, m.afterSettlement()
}

// openActiveWorkWarning shows the active-work warning. It is independent of
// approval mode and of postmortem settings: neither Auto mode nor
// postmortem.on_exit = never disables it.
func (m *Model) openActiveWorkWarning() {
	warning, ok := m.endWarning.(*activeworkpanel.Panel)
	if !ok {
		replacing := m.end != nil && m.end.kind == acpwire.EndIntentReplace
		warning = activeworkpanel.New(replacing, endWorkItems(m.endSnapshot))
		m.endWarning = warning
	} else {
		warning.Replace(endWorkItems(m.endSnapshot))
	}
	// A notice explains a previous refusal; a freshly opened warning has nothing
	// to explain, and a caller that wants one sets it after this returns.
	warning.SetNotice("")
	// The surface the warning displaces is retained (endDisplaced), not
	// released: declining the ending puts it back, so the exchange is silent.
	m.dock.SwapRetained(warning)
	m.refreshViewport()
}

// endWorkItems renders a snapshot's membership as warning rows. An attached
// command is indented under the operation it belongs to (its Group), so the
// list shows one stoppable thing per line where the runtime reports one.
func endWorkItems(snap acpwire.EndWork) []activeworkpanel.Item {
	items := make([]activeworkpanel.Item, 0, len(snap.Items))
	for _, item := range snap.Items {
		label := item.Label
		if label == "" {
			label = item.ID
		}
		if item.Group != "" {
			label = "  " + label
		}
		items = append(items, activeworkpanel.Item{ID: item.ID, Label: label})
	}
	return items
}

// applyEndIntent stores the pending intent, keeping the target only while the
// request is still unaccepted. A nil intent (a plain exit) is stored as an
// exit with no target.
func (m *Model) applyEndIntent(intent *endIntent) {
	if intent == nil {
		intent = &endIntent{kind: acpwire.EndIntentExit}
	}
	if m.end != nil && m.end.kind == intent.kind {
		// Keep the first press's target only when the new request names none.
		if intent.name == "" && intent.resumeID == "" {
			return
		}
	}
	m.end = intent
}

// snapshotEmpty reports whether a membership holds no ordinary work. A session
// without an end service reports an empty membership: there is no runtime work
// to warn about.
func snapEmpty(snap acpwire.EndWork) bool { return len(snap.Items) == 0 }

// acceptEnd submits the decision to freeze ordinary admission and stop the
// displayed work. displayedIDs are the opaque IDs the user was actually shown
// (nil means "warn about nothing", which is the no-work path). A refusal — the
// membership grew while the warning was up — refreshes the warning and asks
// again, keeping the user's selection and saying what appeared.
func (m *Model) acceptEnd(displayedIDs []string) tea.Cmd {
	intent := acpwire.EndIntentExit
	if m.end != nil && m.end.kind != "" {
		intent = m.end.kind
	}
	decision, err := m.sess.AcceptEnd(m.ctx, acpwire.EndAcceptParams{
		Intent:           intent,
		DisplayedIDs:     displayedIDs,
		RequireDisplayed: true,
	})
	if err != nil {
		m.note(fmt.Sprintf("Cannot end the conversation: %v", err))
		m.refreshViewport()
		return nil
	}
	if !decision.Accepted {
		// The membership expanded beyond what the user saw. Nothing was
		// stopped and nothing was frozen: refresh the warning from the
		// membership the refusal reported and ask again. The user's own
		// selection is preserved — re-seating it on the safe choice would make
		// the exit unreachable for a membership that keeps growing — and the
		// panel says what changed, so the next Enter is an explicit
		// re-confirmation of the set now on screen.
		m.endSnapshot = decision.Work
		m.endStage = endAwaitingDecision
		m.renderEndWarningNotice(expansionNotice(len(decision.Expanded)))
		return nil
	}
	m.endSnapshot = decision.Work
	// The displaced surface is deliberately NOT restored: the runtime is
	// settling this conversation, and a panel that outlived it would be a
	// handle into work that is being stopped.
	m.releaseEndDisplaced()
	return m.settleEnd()
}

// releaseEndDisplaced permanently discards the surface the ending warning
// displaced. The end has been accepted or there is nothing to settle, so that
// panel is never restored: a CloseAware one is notified here instead of being
// kept alive to the exit.
func (m *Model) releaseEndDisplaced() {
	displaced := m.endDisplaced
	m.endDisplaced = nil
	if displaced != nil {
		m.dock.ReleaseRetained(displaced)
	}
}

// settleEnd runs the stop/join pass outside the render loop, keeping the
// warning up (refreshed from the runtime) while it runs. A failure keeps the
// pending intent and the stopping stage, reports the error honestly, and
// treats the next quit request as a retry — it never advances as if the work
// had settled.
func (m *Model) settleEnd() tea.Cmd {
	m.endStage = endStopping
	m.refreshEndWarning()
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	sess := m.sess
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, endSettlementTimeout)
		defer cancel()
		if err := sess.WaitForEnd(ctx); err != nil {
			return endSettledMsg{err: err}
		}
		return endSettledMsg{}
	}
}

// refreshEndWarning rewrites the open warning from the runtime's current
// membership, so work that started or finished while the prompt was up is
// shown as it now is. It re-opens the warning if another surface took the dock
// while the user was deciding — the warning is the question being answered, so
// nothing may quietly replace it — and it is a no-op once the end has moved on.
func (m *Model) refreshEndWarning() {
	if m.endWarning == nil || (m.endStage != endAwaitingDecision && m.endStage != endStopping) {
		return
	}
	m.endSnapshot = m.sess.ActiveWork()
	m.renderEndWarning()
}

// renderEndWarning writes the current snapshot into the warning panel, creating
// it if this end does not hold one and re-asserting it over any surface that
// took the dock. It does not re-query the runtime: a caller that already has an
// authoritative membership (a refused acceptance) renders exactly that.
func (m *Model) renderEndWarning() { m.renderEndWarningNotice("") }

// renderEndWarningNotice is renderEndWarning with an explanatory line above the
// choices. A refusal uses it to say what appeared, so the re-ask is explicit
// rather than a silent re-seat of the user's selection.
func (m *Model) renderEndWarningNotice(notice string) {
	if panel, ok := m.dock.Panel().(*activeworkpanel.Panel); ok && panel == m.endWarning {
		panel.Replace(endWorkItems(m.endSnapshot))
		panel.SetNotice(notice)
		m.refreshViewport()
		return
	}
	// The warning is the question being answered, so nothing else may hold the
	// dock while it is up.
	m.openActiveWorkWarning()
	if panel, ok := m.endWarning.(*activeworkpanel.Panel); ok {
		panel.SetNotice(notice)
		m.refreshViewport()
	}
}

// expansionNotice explains a refused acceptance: the membership grew after the
// user chose, so the panel is asking again about a set that now includes the
// new work.
func expansionNotice(n int) string {
	if n == 1 {
		return "1 new item appeared after you chose — review and confirm again"
	}
	return fmt.Sprintf("%d new items appeared after you chose — review and confirm again", n)
}

// afterSettlement applies the configured postmortem policy and finishes the
// end. It runs only after the accepted end's work has settled, so extraction
// sees the final command and child evidence. It is entered exactly once per
// accepted end: the settled handler refuses to run it again.
func (m *Model) afterSettlement() tea.Cmd {
	m.endStage = endPostmortem
	if m.end == nil || m.end.kind != acpwire.EndIntentExit {
		// A replacement never runs an exit postmortem; it goes straight to its
		// new/resume behavior.
		return m.finishReplacement()
	}
	switch m.postmortemOnExit() {
	case "never":
		m.endAnswered = true
		return m.finishQuit()
	case "always":
		m.endAnswered = true
		// Extraction runs off the render loop, then the end finishes.
		return m.extractPostmortemCmd(false)
	default: // "prompt"
		// A session the user never typed into has nothing to postmortem.
		if !m.sessionHasUserTurn() {
			m.endAnswered = true
			return m.finishQuit()
		}
		m.openPostmortemPrompt()
		return nil
	}
}

// openPostmortemPrompt shows the exit-postmortem question. The prompt is the
// first point a postmortem question may appear: the active-work warning has
// already been answered.
func (m *Model) openPostmortemPrompt() {
	m.dock.Open(postmortempanel.New())
	m.refreshViewport()
}

// postmortemConsentPanel returns the active exit-postmortem question, or nil
// when the dock is showing anything else (including nothing). It is the only
// place that decides whether a consent gate applies, so an unrelated panel can
// never be affected by it.
func (m Model) postmortemConsentPanel() *postmortempanel.Panel {
	panel, ok := m.dock.Panel().(*postmortempanel.Panel)
	if !ok {
		return nil
	}
	return panel
}

// reviewPanel returns the active generated-plan review, or nil when the dock
// is showing anything else (including nothing). The panel-to-host messages
// carry their originating panel, so an identity check against this value is
// what stops a delayed message from acting on a replacement review.
func (m Model) reviewPanel() *sddreview.Panel {
	panel, ok := m.dock.Panel().(*sddreview.Panel)
	if !ok {
		return nil
	}
	return panel
}

// candidatePhrase names the review's generated plan candidate for a transcript
// message. The path is escaped before it is rendered, and a review that carried
// no path falls back to a phrase that does not trail off into "at .".
func candidatePhrase(path string) string {
	if path == "" {
		return "the generated plan candidate"
	}
	return "the generated plan candidate at " + strutil.ShowControls(path)
}

// postmortemConsentKey reports whether k is an affirmative answer to the exit
// postmortem question: yes, or the Enter that confirms the selected choice.
// Declining keys are deliberately absent — the question must stay answerable
// at every geometry, so only consent is ever gated.
func postmortemConsentKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "y", "Y", "enter":
		return true
	}
	return false
}

// postmortemDeclineKey reports whether k can only decline the exit postmortem
// question. It exists so the below-minimum gate can tell a key that answers the
// visible modal question from one aimed at the hidden request behind it.
func postmortemDeclineKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "n", "N", "esc":
		return true
	}
	return false
}

// finishReplacement completes an accepted replacement: a new session or a
// resume. The old work has settled and no postmortem runs, and nothing from the
// old conversation is transferred.
func (m *Model) finishReplacement() tea.Cmd {
	intent := m.end
	newArgs := m.endNewArgs()
	m.end = nil
	m.endSnapshot = acpwire.EndWork{}
	if intent != nil && intent.resumeID != "" {
		// Resume restarts the process with the requested session; the resume ID
		// is recorded only now that the replacement is accepted.
		m.resumeSession = intent.resumeID
		m.endStage = endFinishing
		m.note(fmt.Sprintf("Resuming session %s...", intent.resumeID))
		m.refreshViewport()
		return m.finishQuit()
	}
	m.endStage = endFinishing
	// A new session is built by the runtime and swapped into the model. The swap
	// runs only after an accepted replacement: the runtime froze the old
	// conversation at acceptance, so the new session is built on a settled
	// membership and no old work is transferred.
	return m.newSessionEffect(newArgs)
}

// finishQuit is the shared terminal shutdown body: stop the in-flight turn and
// LSP lookups, clear pending state, and return tea.Quit. Every ending path
// resolves here once its work has settled and its postmortem decision is made.
//
// It deliberately does NOT stop the conversation's work: that is the runtime's
// settlement step (WaitForEnd), which persists each outcome while the database
// is still open. Cancelling here would race it and would discard the evidence
// the postmortem extraction needs.
//
// m.busy is intentionally not reset here — tea.Quit is returned immediately
// and the program is exiting, so the agentFinishedMsg path that normally
// clears busy via state.EndWork() will not run.
func (m *Model) finishQuit() tea.Cmd {
	if m.callInFlight {
		// A quit waits for the call in flight (P3 R6): cancel it so a call
		// that respects its context returns, drop everything queued behind
		// it, and finish when its result is handled. A call that ignores its
		// context is waited for, as a frozen Update waited for it before.
		if m.callCancel != nil {
			m.callCancel()
			m.callCancel = nil
		}
		m.calls = nil
		m.callQuitWaiting = true
		return nil
	}
	// Nothing converted is running, so nothing waits behind it.
	m.calls = nil
	m.endStage = endFinishing
	if m.agentCancel != nil {
		m.agentCancel()
		m.agentCancel = nil
	}
	if m.lspCancel != nil {
		m.lspCancel()
		m.lspCancel = nil
	}
	// Closing the session settles every pending request and shuts it down; the
	// accepted end's settlement already stopped the work, so nothing is stopped
	// again here.
	_ = m.sess.Close(m.ctx)
	// Release the dock's surfaces once on the way out: the active panel and any
	// surface parked behind a temporary exchange (the Send now composer's, the
	// ending warning's) are permanently gone now, so a panel that cancels
	// resources on close gets its chance before the program stops.
	m.dock.ReleaseRetained(m.dock.Panel())
	m.dock.ReleaseRetained(m.composerDisplaced)
	m.dock.ReleaseRetained(m.endDisplaced)
	return tea.Quit
}

// postmortemOnExit returns the configured exit postmortem policy. The config
// loader deliberately does not reject bad values, so any unrecognised value
// falls back to "prompt" here at use time.
func (m *Model) postmortemOnExit() string {
	onExit := m.sess.Config().Postmortem.OnExit
	switch onExit {
	case "always", "never":
		return onExit
	default:
		return "prompt"
	}
}

// sessionHasUserTurn reports whether the session holds any user-role message.
// The exit prompt is skipped for a session the user never typed into.
func (m *Model) sessionHasUserTurn() bool {
	page, err := m.sess.Messages(m.ctx, acpwire.MessageQuery{Role: acpwire.RoleUser, Last: 1})
	if err != nil {
		return false
	}
	return page.Total > 0
}

// extractPostmortemCmd runs the exit postmortem's extraction+write step and
// reports the outcome back through postmortemWrittenMsg. Extraction walks the
// audit log and writes a file, so it belongs in a command rather than on the
// render loop; the session builds and writes the report agent side, and only
// the settled session it reads is captured here.
//
// analysis requests the scoped exit-analysis pass once the report has landed.
func (m *Model) extractPostmortemCmd(analysis bool) tea.Cmd {
	sess := m.sess
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Latch the extraction so a repeated quit request during it neither quits
	// ahead of the report nor starts a second one.
	m.endExtracting = true
	return func() tea.Msg {
		path, err := sess.Postmortem(ctx)
		return postmortemWrittenMsg{path: path, err: err, analysis: analysis}
	}
}

// handlePostmortemWritten applies the outcome of the extraction that ran off the
// render loop. A failure is reported as a transcript line and never blocks the
// exit; on success the requested scoped analysis pass follows, and otherwise the
// end finishes.
func (m *Model) handlePostmortemWritten(msg postmortemWrittenMsg) (tea.Model, tea.Cmd) {
	m.endExtracting = false
	if msg.err != nil {
		m.endAnalyzing = false
		m.note("Postmortem failed: " + msg.err.Error())
		m.refreshViewport()
		return *m, m.finishQuit()
	}
	m.note("Postmortem written to " + msg.path)
	if !msg.analysis {
		// The user asked for extraction only: quit rather than leave the
		// session hanging.
		m.endAnalyzing = false
		m.refreshViewport()
		return *m, m.finishQuit()
	}
	m.endAnalyzing = true
	m.refreshViewport()
	return *m, m.runExitAnalysis(msg.path)
}

// postmortemAgentGoal mirrors the AgentGoal the /postmortem --agent command
// hands back to the agent. The exit panel's "include agent analysis" path
// never goes through that command handler, so it builds the same goal here.
func postmortemAgentGoal(path string) string {
	return "Run the postmortem skill: review this session's transcript and append semantic observations into the agent_observations field of " + path + ". Extraction only — record observations, do not summarize or rank."
}

// settingsBlockReason returns a message when runtime work, a pending
// decision, or another dock surface makes a settings write unsafe.
//
// A running background command is deliberately not a reason to block: the
// command service survives the reload, so applying settings changes only what
// FUTURE launches resolve. Blocking on jobs would refuse a save the running
// work cannot observe.
func (m Model) settingsBlockReason() string {
	if m.busy {
		return settingsBusyMessage
	}
	if m.hasPendingApproval() {
		return "Resolve the pending tool approval to save."
	}
	pending := m.sess.Pending()
	if pending.Question != nil {
		return "Answer the pending question to save."
	}
	if pending.Sudo != nil {
		return "Authenticate or deny the pending privileged command to save."
	}
	if m.dock.IsOpen() {
		if _, browser := m.dock.Panel().(*settings.BrowserPanel); browser {
			return ""
		}
		return "Close the picker to save."
	}
	return ""
}

// handleAgentFinished handles an agentFinishedMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentFinished(msg agentFinishedMsg) (Model, tea.Cmd) {
	// The exit postmortem's agent pass no longer runs here: it is a scoped
	// exit-analysis operation owned by the end controller (runExitAnalysis),
	// so it reports through endAnalysisFinishedMsg and never re-enters the
	// ordinary transcript path.
	// suggestionCmd is the Phase 2 LLM fallback returned by computeSuggestion
	// on the success path; nil otherwise.
	var suggestionCmd tea.Cmd
	// A run the session refused never started: no work is registered, no turn
	// ran, and the model goes idle exactly as a finished turn does. The note
	// names what refused it.
	if msg.refusal != "" && refusedStart(msg.err) {
		m.busy = false
		m.turnStartedAt = time.Time{}
		m.agentCancel = nil
		m.cancelling = false
		m.note(msg.refusal + ": " + msg.err.Error())
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	}
	// A cancelled turn's error is an artifact of the cancellation, not a
	// provider fault. It does not reliably wrap context.Canceled: a stream
	// aborted mid-flight surfaces as a wrapped transport error ("provider
	// %q: chat request failed: ..."), which used to pin a bogus error banner
	// under the transcript until the next successful turn.
	cancelled := m.cancelling
	if cancelled {
		// ClearSteering drops the ordinary queued entries but preserves the
		// ones an accepted, undelivered Send now request is still responsible
		// for: the user asked explicitly for those to be delivered, and a UI
		// cleanup must not silently revoke the request.
		m.sess.ClearSteering(m.ctx)
		m.note("Agent turn cancelled.")
		_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
		m.cancelling = false
	}
	m.busy = false
	m.turnStartedAt = time.Time{}
	m.agentCancel = nil
	// The changed files are refreshed off-thread by the refresh returned
	// below. An open sheet's turn and fleet values are refreshed with it,
	// off the UI thread, so they land a moment after the turn ends.
	sheetOpen := m.sheetOpen()
	if msg.err != nil && !cancelled && !errors.Is(msg.err, context.Canceled) {
		// SDD human gate: open the gate panel and wait for the user's answer.
		if acpclient.IsCode(msg.err, acpwire.CodeHumanGateRequired) {
			gate := m.sess.Status().SDDGate
			if gate.Question != "" {
				// One surface: the panel carries the question, its context,
				// and the key hints. Nothing goes to the transcript here.
				m.dock.Open(gatepanel.New(gate))
				m.updateViewportHeight()
				m.refreshViewport()
				return m, nil
			}
		}
		_ = m.sess.SetNotice(m.ctx, noticeForError(msg.err, "turn"))
		m.note("✘ Turn failed: " + firstLine(msg.err.Error()))
		m.successPulse = false
	} else if msg.err == nil {
		// A completed turn proves the provider is reachable again — clear
		// any stale provider notice from an earlier failed turn.
		_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
		m.successPulse = true
		m.successPulseAt = m.now()
		// A completed turn is the single point where the final assistant
		// message is in the session state — compute the next-prompt
		// suggestion here. Only on the success path: a failed or cancelled
		// turn must not leave a stale ghost. The returned cmd is the Phase 2
		// LLM fallback (nil when not applicable).
		suggestionCmd = m.computeSuggestion()
	}
	m.updateViewportHeight()
	m.refreshViewport()
	flushCmd := m.flushPendingModelOptions()
	// The session rebases the changed files onto the active root's HEAD, so
	// committed agent work stops inflating the diff.
	cmds := []tea.Cmd{tickCmd(), flushCmd, refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true, Stats: sheetOpen, Fleet: sheetOpen})}
	if suggestionCmd != nil {
		cmds = append(cmds, suggestionCmd)
	}
	// Issue blast-radius lookups for any edit rows the turn just settled.
	// tea.Batch drops nil entries, so this is a no-op with no finder or no
	// newly-seen resolved symbols.
	cmds = append(cmds, m.callerQueryCmds())
	// A turn that ends with steering still queued was typed during the final
	// answer, after the runner's last loop-top drain. Deliver it now as the
	// next turn instead of leaving it stranded in the queue: the only other
	// consumer is the blank-Enter follow-up, which the user has no reason to
	// press once the turn is visibly over. A cancelled turn already cleared
	// the queue above, and a failed turn keeps its text queued so the user can
	// retry deliberately.
	if msg.err == nil && !cancelled {
		if cmd := m.flushQueuedSteering(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// Auto-resume latch (spec §4): a resume fire landed in the
	// final-answer window of the turn that just ended — the model never
	// saw the report, and the turn-end residual drain armed the latch.
	// The session is idle now: consume the intent and start the resumed
	// turn; its cmd rides the existing batch. When the wake is
	// suppressed (dock open, gate off), the consumed intent is dropped
	// by design: the report stays persisted, repeat watches retry on
	// their next fire, and once-watches lose the wake (spec edge
	// cases). The run path's beginTurn clears the ghost suggestion and bumps
	// the suggestion generation, so no computeSuggestion gating is needed.
	if name, repeat, ok := m.sess.TakeWatchResume(m.ctx); ok {
		if cmd := m.startWatchResumeRun(name, repeat); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// clearSuggestionIfPrefixBroken clears the active suggestion when the typed
// input no longer prefixes it (the user typed something that diverges from
// the suggested text). This prevents a stale ghost from resurrecting if the
// user deletes back to an empty input.
func (m *Model) clearSuggestionIfPrefixBroken() {
	if m.suggestion == "" {
		return
	}
	value := m.input.Value()
	if value != "" && !strings.HasPrefix(m.suggestion, value) {
		m.suggestion = ""
		m.suggestionDismissed = false
	}
}

// computeSuggestion derives the next-prompt suggestion from the final
// assistant message of the just-completed turn and stores it on the model.
// It resets the dismissed flag so a fresh turn can surface a new ghost.
//
// The Phase 2 LLM fallback is gated on the deterministic rules'
// *confidence*, not on their silence. Gating on silence meant a single
// false positive from the action-proposal rule permanently suppressed the
// model. Now: ConfidenceHigh is a plain yes/no question a model cannot
// improve on, so no call is made; ConfidenceLow shows its text
// immediately and still asks for a better one; ConfidenceNone shows
// nothing while the model is asked. The fallback only runs when
// [tui] suggestions is "llm" and a provider is wired — "rules" renders Low
// and High and never calls out, which is the shipped default.
func (m *Model) computeSuggestion() tea.Cmd {
	m.suggestion = ""
	m.suggestionDismissed = false
	suggestions := m.sess.Config().TUI.Suggestions
	if suggestions == "off" {
		return nil
	}
	page, err := m.sess.Messages(m.ctx, acpwire.MessageQuery{Role: acpwire.RoleAssistant, Last: 1})
	if err != nil || len(page.Messages) == 0 {
		return nil
	}
	last := page.Messages[len(page.Messages)-1]

	// Show whatever the rules produced right away. ConfidenceNone yields
	// "", so nothing is shown until the fallback returns.
	s, conf := extractSuggestion(last.Content)
	m.suggestion = s
	if conf == ConfidenceHigh {
		return nil
	}
	if suggestions != "llm" || !m.sess.Status().AgentReady {
		return nil
	}
	// The generation counter is bumped so a stale result from an earlier
	// turn is discarded. It covers the ConfidenceLow case unchanged: a
	// replacement arriving after the user has started typing is exactly
	// what it was built for.
	m.suggestionGen++
	return runSuggestionFallbackCmd(m.sess, last.Content, m.suggestionGen)
}

// handlePlanAuthorFinished handles the completion of an authoring turn. On
// success it opens the review panel; on error it reports the error and keeps
// any generated artifact. It never starts an SDD run.
func (m Model) handlePlanAuthorFinished(msg planAuthorFinishedMsg) (Model, tea.Cmd) {
	m.busy = false
	m.turnStartedAt = time.Time{}
	m.agentCancel = nil
	// A refused start never reached the session's run path: report it the way
	// every other refusal is reported and stay idle.
	if msg.refusal != "" && refusedStart(msg.err) {
		m.cancelling = false
		m.note(msg.refusal + ": " + msg.err.Error())
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	}
	// A stopped authoring is not a failure. The stop already reported the
	// cancel, and the run's context error is that stop; clearing cancelling
	// here also re-arms Ctrl+C as a stop rather than a quit (decision 11).
	if m.cancelling || msg.result.StopReason == acpwire.StopReasonCancelled {
		m.cancelling = false
		_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
		m.note("Agent turn cancelled.")
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	}
	if msg.err != nil {
		_ = m.sess.SetNotice(m.ctx, noticeForError(msg.err, "plan-author"))
		m.note("✘ Plan authoring failed: " + firstLine(msg.err.Error()))
		m.updateViewportHeight()
		m.refreshViewport()
		return m, nil
	}
	_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
	if msg.result.AutoAccepted {
		m.note("Plan accepted in Full Auto: " + msg.result.PlanPath)
	} else {
		m.dock.Open(sddreview.New(msg.result))
	}
	m.updateViewportHeight()
	m.refreshViewport()
	return m, nil
}

// handleChanged handles a changedMsg: the session signalled that its status
// may have changed. It repaints, follows the workspace's base ref, acts on a
// new watch resume, and re-arms the pump.
func (m Model) handleChanged(msg changedMsg) (Model, tea.Cmd) {
	// A pump from a replaced session is ignored and not re-armed, so it ends.
	if msg.ch != m.sess.Changed() {
		return m, nil
	}
	st := m.sess.Status()
	cmds := []tea.Cmd{pumpChanged(msg.ch)}

	// The sheet's changed files follow the workspace's active root.
	if st.Workspace != m.workspaceSeen {
		m.workspaceSeen = st.Workspace
		if st.Workspace.ActiveRoot != "" {
			cmds = append(cmds, refreshSheetCmd(m.ctx, m.sess, acpwire.SheetRefresh{Rebase: true}))
		}
	}

	// A resume-bearing watch fire wakes the session once per sequence.
	if r := st.WatchResume; r != nil && r.Seq > m.watchResumeSeq {
		m.watchResumeSeq = r.Seq
		if cmd := m.startWatchResumeRun(r.Name, r.Repeat); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	m.syncWork()
	m.refreshViewport()
	if cmd := m.flushPendingModelOptions(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// ── Send now ────────────────────────────────────────────────────────────

// sendNowUnavailable is the explanation a fail-closed Send now renders: the
// draft is kept, and the user is told which condition is missing rather than
// being left wondering why the key did nothing.
const sendNowUnavailable = "Send now is unavailable: the runtime reports no supported, active turn to interrupt."

// isSendNowKey reports whether a keypress is the global Send now action.
// Bubble Tea v2 reports Alt+Enter as "alt+enter"; the modifier-plus-code form
// is checked too because a terminal that does not extend the key still
// delivers the modifier bit.
func isSendNowKey(k tea.KeyPressMsg) bool {
	if k.String() == "alt+enter" {
		return true
	}
	return k.Code == tea.KeyEnter && k.Mod.Contains(tea.ModAlt)
}

// sendNowTarget resolves the turn a Send now action may target. It reports
// false when the turn is not eligible (idle, closing, unsupported) or the TUI
// is not in a turn.
func (m *Model) sendNowTarget() (acpwire.SteeringState, bool) {
	snap := m.sess.Status().Steering.State
	if !m.busy || !snap.Active || !snap.Supported {
		return acpwire.SteeringState{}, false
	}
	return snap, true
}

// rejectSendNow explains a refusal in the transcript and keeps whatever draft
// produced it. A refusal never mutates the queue, the turn or the operation.
func (m *Model) rejectSendNow(reason string) {
	m.note(reason)
	m.refreshViewport()
}

// submitSendNow performs the bounded, memory-only session call for one
// request. clear runs only after acceptance, which is what makes the accepted
// text — and only the accepted text — disappear. On rejection the caller's
// draft survives untouched and the reason is rendered.
func (m *Model) submitSendNow(request acpwire.SendNowRequest, clear func()) bool {
	if request.RequestID == "" {
		// The request ID identifies one transport submission; a retry reuses
		// it, so it is minted here and never reused for different text.
		request.RequestID = rand.Text()
	}
	if _, err := m.sess.SendNow(m.ctx, request); err != nil {
		m.rejectSendNow("Send now was rejected: " + err.Error() + " Your text was kept.")
		return false
	}
	if clear != nil {
		clear()
	}
	m.refreshViewport()
	return true
}

// clearChatDraft clears the accepted ordinary-chat draft. It mirrors the
// Enter handler's reset so an accepted Send now leaves the composer exactly as
// an accepted Enter would.
func (m *Model) clearChatDraft() {
	m.resetInput()
	m.completionSuppressed = false
	m.lastInputForPopups = ""
	m.cmdArgMode = false
	m.cmdArgPrefix = ""
	m.clearFinishedRun()
	m.dismissCompletionPopups()
	m.updateViewportHeight()
	m.viewportFollow = true
}

// surfaceOwnsInput reports whether something other than the plain chat editor
// owns the keyboard: a dock panel (including the access panel), a pending
// human prompt, the command-edit sub-mode, or a /doctor repair's masked key
// field. Alt+Enter then opens the temporary composer instead of submitting,
// and never reads the owner's value.
func (m *Model) surfaceOwnsInput() bool {
	if m.dock.IsOpen() || m.editingApproval() || m.doctorSecret != nil {
		return true
	}
	pending := m.sess.Pending()
	return m.hasPendingApproval() ||
		pending.Question != nil ||
		pending.SkillGate != nil ||
		pending.Sudo != nil
}

// focusedPromptKey returns the key of the pending prompt that owns the input,
// in the same order the update routing resolves them.
func (m *Model) focusedPromptKey() (promptRef, bool) {
	p := m.sess.Pending()
	if p.Sudo != nil && p.Sudo.Key != "" {
		return promptRef{kind: promptSudo, key: p.Sudo.Key}, true
	}
	if p.SkillGate != nil && p.SkillGate.Key != "" {
		return promptRef{kind: promptSkill, key: p.SkillGate.Key}, true
	}
	if len(p.Approvals) > 0 && p.Approvals[0].Key != "" {
		return promptRef{kind: promptApproval, key: p.Approvals[0].Key}, true
	}
	if p.Question != nil && p.Question.Key != "" {
		return promptRef{kind: promptQuestion, key: p.Question.Key}, true
	}
	return promptRef{}, false
}

// beginSendNow is the global Alt+Enter action. A plain chat editor submits its
// draft (or flushes the queue) directly — the completion popup and the command
// parser never get a look at the key, so "/clear" is steering text, not a
// command. Any other surface owning input opens the blank composer instead,
// which never reads the value that surface holds. It is only reached when no
// composer is active (Update guards on that), so there is nothing to close.
func (m *Model) beginSendNow() (tea.Model, tea.Cmd) {
	snap, ok := m.sendNowTarget()
	// The snapshot is the runtime's own view of the turn, so an
	// unsupported/idle/closing runtime is refused before the draft is touched.
	if !ok {
		m.rejectSendNow(sendNowUnavailable)
		return m, nil
	}
	if m.surfaceOwnsInput() {
		m.openSteeringComposer()
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if len(m.pastes) > 0 {
		text = m.expandPastes(text)
	}
	if strings.TrimSpace(text) == "" && len(m.sess.Status().Steering.Queue) == 0 {
		m.rejectSendNow("Nothing to send now: the input is empty and no steering is queued.")
		return m, nil
	}
	m.submitSendNow(acpwire.SendNowRequest{
		SessionID: m.sess.ID(),
		TurnID:    snap.TurnID,
		Text:      text,
	}, func() { m.clearChatDraft() })
	return m, nil
}

// openSteeringComposer opens the temporary Send now composer over whatever
// holds the input. It records the surface it displaced — at most one panel,
// plus that panel's canonical request identity and the pre-composer focus —
// so closing the composer can put the user back where they were. Opening it
// cancels nothing and reads no existing draft.
func (m *Model) openSteeringComposer() {
	snap, ok := m.sendNowTarget()
	if !ok {
		m.rejectSendNow(sendNowUnavailable)
		return
	}
	m.composerDisplaced = nil
	m.composerDisplacedAccess = ""
	m.composerDisplacedPrompt = promptRef{}
	m.composerInputFocused = m.input.Focused()
	if panel := m.dock.Panel(); panel != nil {
		if ap, ok := panel.(*accesspanel.Panel); ok {
			m.composerDisplacedAccess = ap.Request().RequestID
		}
		m.composerDisplaced = panel
	} else if key, ok := m.focusedPromptKey(); ok {
		m.composerDisplacedPrompt = key
	}
	composer := steerpanel.New(m.sess.ID(), snap.TurnID)
	m.steeringComposer = composer
	m.composerID = composer.ID()
	m.composerSessionID = m.sess.ID()
	m.composerTurnID = snap.TurnID
	// The displaced panel is retained, not released: closing the composer puts
	// it back, so the exchange is silent.
	m.dock.SwapRetained(composer)
	// The base editor keeps its text but must not render a cursor while the
	// composer owns the input.
	m.input.Blur()
	m.updateViewportHeight()
	m.refreshViewport()
}

// closeSteeringComposer closes the composer identified by id and restores the
// surface it displaced. Any other id is ignored: a late Esc from a composer
// that was already replaced must not close the current one.
func (m *Model) closeSteeringComposer(id string) {
	if m.steeringComposer == nil || id != m.composerID {
		return
	}
	m.steeringComposer = nil
	m.composerID = ""
	m.composerSessionID = ""
	m.composerTurnID = ""
	m.restoreComposerSurface()
	if m.composerInputFocused {
		m.input.Focus()
	}
	m.updateViewportHeight()
	m.refreshViewport()
}

// restoreComposerSurface puts back the panel the composer displaced, but only
// while that surface still exists. A request cancelled by the interruption, or
// a prompt that settled while the composer was up, retires instead of being
// resurrected: the user no longer has that decision to make.
func (m *Model) restoreComposerSurface() {
	panel := m.composerDisplaced
	if m.composerDisplacedAccess != "" {
		if snap, pending := m.accessRequestSnapshot(); !pending || snap.RequestID != m.composerDisplacedAccess {
			// The request was canceled, satisfied or replaced. Existing
			// reconciliation re-presents a replacement from scratch.
			panel = nil
		}
	}
	if key := m.composerDisplacedPrompt; key.key != "" && !m.promptStillPending(key) {
		m.retireStalePromptModels(key)
	}
	// The composer is permanently gone once it closes, whether or not the
	// surface it displaced survives.
	retired := m.composerDisplaced
	m.composerDisplaced = nil
	m.composerDisplacedAccess = ""
	m.composerDisplacedPrompt = promptRef{}
	if panel != nil {
		// The restored surface takes the slot silently; the composer it
		// replaces is what has been permanently discarded.
		m.dock.ReleaseRetained(m.dock.SwapRetained(panel))
		return
	}
	// The displaced surface retired instead of being resurrected, so it is
	// discarded too; CloseNow notifies the composer and clears the slot.
	m.dock.ReleaseRetained(retired)
	m.dock.CloseNow()
}

// promptStillPending reports whether ref still names the live pending request
// of its kind.
func (m *Model) promptStillPending(ref promptRef) bool {
	p := m.sess.Pending()
	switch ref.kind {
	case promptQuestion:
		return p.Question != nil && p.Question.Key == ref.key
	case promptSkill:
		return p.SkillGate != nil && p.SkillGate.Key == ref.key
	case promptSudo:
		return p.Sudo != nil && p.Sudo.Key == ref.key
	default:
		return len(p.Approvals) > 0 && p.Approvals[0].Key == ref.key
	}
}

// retireStalePromptModels drops a form bound to a prompt that settled while the
// composer held the input, so a late keypress cannot drive a dead dialog.
func (m *Model) retireStalePromptModels(ref promptRef) {
	switch ref.kind {
	case promptQuestion:
		m.questionModel = nil
	case promptSkill:
		m.skillGateModel = nil
	case promptSudo:
		m.sudoAuthModel = nil
	default:
		m.approvalModel = nil
		// An edit bound to the settled request must not survive it: the
		// replacement (if any) gets its own editor or the approval form.
		m.retireApprovalEdit()
	}
}

// handleComposerSubmit accepts the literal text the composer emitted, but only
// when the message names the composer that is actually up and that composer's
// conversation and turn are still current. A stale message retains its text
// and changes nothing; a stale target never retargets the new turn.
func (m *Model) handleComposerSubmit(msg steerpanel.SubmitMsg) (tea.Model, tea.Cmd) {
	if m.steeringComposer == nil || msg.ComposerID != m.composerID {
		// A message from a composer that was already closed or replaced. It
		// has no side effect at all.
		return m, nil
	}
	if msg.SessionID != m.composerSessionID || m.sess.ID() != m.composerSessionID {
		m.rejectSendNow("Send now targets a different conversation now. The composer text was kept.")
		return m, nil
	}
	snap, ok := m.sendNowTarget()
	if !ok {
		m.rejectSendNow(sendNowUnavailable + " The composer text was kept.")
		return m, nil
	}
	if msg.TurnID != m.composerTurnID || snap.TurnID != m.composerTurnID {
		m.rejectSendNow("The turn this composer targets has ended. The composer text was kept.")
		return m, nil
	}
	if strings.TrimSpace(msg.Text) == "" && len(m.sess.Status().Steering.Queue) == 0 {
		m.rejectSendNow("Nothing to send now: the composer is empty and no steering is queued.")
		return m, nil
	}
	id := m.composerID
	m.submitSendNow(acpwire.SendNowRequest{
		SessionID: msg.SessionID,
		TurnID:    msg.TurnID,
		Text:      msg.Text,
	}, func() { m.closeSteeringComposer(id) })
	return m, nil
}

// handleComposerClose restores the displaced surface. Esc closes the composer
// and nothing else: no interruption, no queue mutation, no submission.
func (m *Model) handleComposerClose(msg steerpanel.CloseMsg) (tea.Model, tea.Cmd) {
	m.closeSteeringComposer(msg.ComposerID)
	return m, nil
}

// steeringStatusSegment renders the bounded Send now status beside the queue
// marker. It is a status segment, not a task card: the action is a property of
// the running turn, and the transcript keeps rendering through the stop.
func (m Model) steeringStatusSegment(r *acpwire.SteeringReceipt) (string, bool) {
	if r == nil {
		return "", false
	}
	if !m.busy && r.Status != acpwire.SteeringUndelivered {
		// A settled turn keeps only the truthful undelivered disclosure: once
		// the turn is over, "delivered" and "interrupting" describe a request
		// that no longer has a live operation behind it.
		return "", false
	}
	switch r.Status {
	case acpwire.SteeringInterrupting:
		return "send now: interrupting", true
	case acpwire.SteeringWaitingSafe:
		return "send now: waiting for a safe boundary", true
	case acpwire.SteeringWaitingResume:
		return "send now: waiting for resume", true
	case acpwire.SteeringDelivered:
		return "send now: delivered", true
	case acpwire.SteeringUndelivered:
		return "send now: undelivered", true
	default:
		// Accepted: the interruption has not started yet, and claiming more
		// than that would be a lie about work that is still running.
		return "send now: accepted", true
	}
}

// ── Live work ──────────────────────────────────────────────────────────────

// workReceiptTickWindow mirrors viewmodel's bounded receipt window: a settled
// item is shown for this long from EndedAt, and the idle tick repaints while one
// is inside it. It is a repaint bound only — it never extends a receipt.
const workReceiptTickWindow = 5 * time.Second

// workAvailable reports whether the session serves work.
func (m Model) workAvailable() bool {
	_, ok := m.sess.Work()
	return ok
}

// syncWork applies the session's work snapshot: it records the previous
// progress observations, keeps the snapshot and hands it to an open Tasks
// panel. A session that serves no work leaves the model's snapshot alone.
func (m *Model) syncWork() {
	snap, ok := m.sess.Work()
	if !ok {
		return
	}
	if snap.ConversationID == m.workSnapshot.ConversationID &&
		snap.Revision == m.workSnapshot.Revision {
		return
	}
	m.notePreviousProgress(snap)
	m.workSnapshot = snap
	if m.tasksPanel != nil {
		m.tasksPanel.setWork(true, m.sess.ID(), snap)
	}
}

// resetWork starts the work view over for the session the model holds: the
// session's snapshot as the seed, no previous observations, no expanded card,
// and an open Tasks panel's Work tab cleared. It does nothing for a session
// that serves no work.
func (m *Model) resetWork() {
	if !m.workAvailable() {
		return
	}
	snap, _ := m.sess.Work()
	m.workSnapshot = snap
	m.workPrevious = nil
	m.workExpanded = ""
	if m.tasksPanel != nil {
		m.resetTasksPanelWork()
	}
}

// resetTasksPanelWork clears the panel's Work tab after a session rebind: its
// cards and outstanding requests belonged to the conversation that just ended,
// so a late reply must not be able to apply to the new one.
func (m *Model) resetTasksPanelWork() {
	p := m.tasksPanel
	if p == nil {
		return
	}
	p.pending = map[string]workPending{}
	p.items = nil
	p.nextCursor = ""
	p.stale = false
	p.selectedKey = ""
	p.detail = nil
	p.detailOpen = false
	p.err = ""
	p.setWork(m.workAvailable(), m.sess.ID(), m.workSnapshot)
}

// notePreviousProgress records each running item's previous progress
// observation, so the next snapshot can be measured against it. Keys absent
// from next are dropped: a settled item has no rate to show.
func (m *Model) notePreviousProgress(next acpwire.WorkSnapshot) {
	prev := make(map[string]*acpwire.WorkProgress, len(next.Items))
	for _, it := range next.Items {
		if it.Progress == nil {
			continue
		}
		var old *acpwire.WorkProgress
		for i := range m.workSnapshot.Items {
			if m.workSnapshot.Items[i].TaskKey == it.TaskKey {
				old = m.workSnapshot.Items[i].Progress
				break
			}
		}
		if old != nil && old.ObservedAt < it.Progress.ObservedAt {
			prev[it.TaskKey] = old
			continue
		}
		if p, ok := m.workPrevious[it.TaskKey]; ok {
			prev[it.TaskKey] = p
		}
	}
	m.workPrevious = prev
}

// handleWorkRequest services one Tasks-panel Work action. Only the currently
// open panel's request acts: a request from a panel that was closed or replaced
// is dropped. The service calls are returned as tea.Cmds so they run outside
// Update and never block input.
func (m Model) handleWorkRequest(msg workRequestMsg) (Model, tea.Cmd) {
	if !m.tasksOpen() || msg.panelID != m.tasksPanel.panelID {
		return m, nil
	}
	if msg.conversationID != m.tasksPanel.conversationID {
		return m, nil
	}
	switch msg.kind {
	case workReqExpand:
		// Fire-and-forget kinds are serviced in this step, so release the
		// panel's booking here: every booked request must be reconciled by
		// request ID, or the pending map only ever grows.
		delete(m.tasksPanel.pending, msg.requestID)
		// Space toggles (spec: "Space expands/collapses the selected live
		// card"): the card that is already expanded collapses on the second
		// press, matching the now bar's card-header gesture.
		if m.workExpanded == msg.taskKey {
			m.workExpanded = ""
		} else {
			m.workExpanded = msg.taskKey
		}
		m.refreshViewport()
		return m, nil
	case workReqOrigin:
		delete(m.tasksPanel.pending, msg.requestID)
		return m.jumpToWorkOrigin(msg.taskKey)
	case workReqPrompt:
		return m.navigateToWorkPrompt(msg)
	case workReqList:
		return m, m.workListCmd(msg)
	case workReqInspect:
		return m, m.workInspectCmd(msg)
	case workReqStop:
		return m, m.workStopCmd(msg)
	}
	return m, nil
}

// workListCmd issues one list request off the Update thread, capturing the
// exact conversation, panel and request identity so the reply can be validated.
func (m Model) workListCmd(msg workRequestMsg) tea.Cmd {
	if !m.workAvailable() {
		return nil
	}
	sess := m.sess
	return func() tea.Msg {
		page, err := sess.ListWork(context.Background(), msg.query)
		return workPageMsg{
			conversationID: msg.conversationID,
			panelID:        msg.panelID,
			requestID:      msg.requestID,
			query:          msg.query,
			page:           page,
			err:            err,
		}
	}
}

// workInspectCmd issues one inspect request off the Update thread.
func (m Model) workInspectCmd(msg workRequestMsg) tea.Cmd {
	if !m.workAvailable() {
		return nil
	}
	sess := m.sess
	raw := msg.raw
	return func() tea.Msg {
		detail, err := sess.InspectWork(context.Background(), msg.taskKey, raw)
		return workDetailMsg{
			conversationID: msg.conversationID,
			panelID:        msg.panelID,
			requestID:      msg.requestID,
			detail:         detail,
			err:            err,
		}
	}
}

// workStopCmd issues one stop request off the Update thread. The captured
// revision, key and conversation travel with the reply so a stale acceptance can
// never describe the wrong item.
func (m Model) workStopCmd(msg workRequestMsg) tea.Cmd {
	if !m.workAvailable() {
		return nil
	}
	sess := m.sess
	return func() tea.Msg {
		stop, err := sess.StopWork(context.Background(), msg.taskKey, "requested from the Tasks panel")
		return workActionMsg{
			conversationID: msg.conversationID,
			taskKey:        msg.taskKey,
			resultRevision: msg.resultRevision,
			requestID:      msg.requestID,
			kind:           workReqStop,
			stop:           stop,
			err:            err,
		}
	}
}

// boxStopMsg is the reply to a stop asked for from the live box. It carries no
// panel identity: the box is not the Tasks panel, and its reply must not be
// validated as one.
type boxStopMsg struct {
	taskKey string
	stop    acpwire.WorkStopResult
	err     error
}

// boxStopCmd issues one stop request from the live box off the Update thread.
// It does not reuse workStopCmd: that reply is addressed to the Tasks panel and
// is dropped unless the panel that asked is still open.
func (m Model) boxStopCmd(taskKey string) tea.Cmd {
	if !m.workAvailable() {
		return nil
	}
	sess := m.sess
	return func() tea.Msg {
		stop, err := sess.StopWork(context.Background(), taskKey, "requested from the live box")
		return boxStopMsg{taskKey: taskKey, stop: stop, err: err}
	}
}

// handleBoxStop flashes what became of a stop asked for from the box: the
// error, or the refusal's summary, or that the request went out.
func (m Model) handleBoxStop(msg boxStopMsg) (Model, tea.Cmd) {
	text := "Stop requested"
	switch {
	case msg.err != nil:
		text = msg.err.Error()
	case !msg.stop.Accepted:
		text = msg.stop.Summary
	}
	cmd := m.setFlash(text)
	return m, cmd
}

// handleWorkPage applies one list reply, but only while the panel and request
// that asked for it are still current. A reply from a closed or replaced panel
// is dropped, so it can never reopen or corrupt it.
func (m Model) handleWorkPage(msg workPageMsg) (Model, tea.Cmd) {
	if !m.tasksOpen() || msg.panelID != m.tasksPanel.panelID {
		return m, nil
	}
	if msg.conversationID != m.tasksPanel.conversationID {
		return m, nil
	}
	if m.tasksPanel.applyPage(msg.requestID, msg.page, msg.err) {
		m.refreshViewport()
	}
	return m, nil
}

// handleWorkDetail applies one inspect reply with the same identity checks.
func (m Model) handleWorkDetail(msg workDetailMsg) (Model, tea.Cmd) {
	if !m.tasksOpen() || msg.panelID != m.tasksPanel.panelID {
		return m, nil
	}
	if msg.conversationID != m.tasksPanel.conversationID {
		return m, nil
	}
	if m.tasksPanel.applyDetail(msg.requestID, msg.detail, msg.err) {
		m.refreshViewport()
	}
	return m, nil
}

// handleWorkAction applies one stop or acknowledgement reply. The stop result
// describes the accepted request only; the item's actual state settles from the
// next source snapshot. Errors stay visible and never change the item's
// capability or attention. The reply carries no panel ID (contract C5): the
// request ID is the panel-scoped identity, so it is validated by its prefix.
func (m Model) handleWorkAction(msg workActionMsg) (Model, tea.Cmd) {
	if !m.tasksOpen() || !strings.HasPrefix(msg.requestID, m.tasksPanel.panelID+".") {
		return m, nil
	}
	if msg.conversationID != m.tasksPanel.conversationID {
		return m, nil
	}
	p := m.tasksPanel
	// The reply has found its panel; release the booked request so a slow
	// reply cannot leave a pending entry behind for the panel's lifetime.
	delete(p.pending, msg.requestID)
	switch msg.kind {
	case workReqStop:
		switch {
		case msg.err != nil:
			p.err = msg.err.Error()
		case !msg.stop.Accepted:
			p.err = msg.stop.Summary
		default:
			p.err = ""
		}
	}
	m.refreshViewport()
	return m, nil
}

// workLaneAging reports whether the pinned work bar is showing anything whose
// appearance changes with time: a live card's elapsed age, or a terminal item
// still inside its bounded receipt window. It keeps the idle tick alive so
// those repaint, and nothing more.
func (m Model) workLaneAging() bool {
	now := m.now()
	for _, it := range m.workSnapshot.Items {
		if livebox.WorkActive(it) {
			return true
		}
		if it.EndedAt != 0 && now.Sub(acpwire.Time(it.EndedAt)) <= workReceiptTickWindow {
			return true
		}
	}
	return false
}

// workItemByKey finds one item in the current projection by TaskKey.
func (m Model) workItemByKey(taskKey string) (acpwire.WorkItem, bool) {
	for _, it := range m.workSnapshot.Items {
		if it.TaskKey == taskKey {
			return it, true
		}
	}
	return acpwire.WorkItem{}, false
}

// jumpToWorkOrigin resolves a work item's stored reference and navigates to it.
// A retained transcript node is scrolled to and highlighted; when the node is
// gone the bounded retained detail is opened with unavailable text instead of a
// dead jump.
func (m *Model) jumpToWorkOrigin(taskKey string) (Model, tea.Cmd) {
	item, ok := m.workItemByKey(taskKey)
	if !ok {
		m.note("That work item is no longer available.")
		m.refreshViewport()
		return *m, nil
	}
	// A retained child session is the most specific reference: drill into the
	// actual child while it is still registered. This is the same target Ctrl+F
	// reaches.
	if childID := item.Reference.ChildSessionID; childID != "" {
		for _, v := range m.sess.Status().Subagents {
			if v.Drill != nil && v.Drill.SessionID == childID {
				// The child's state can outlive the wire's retention window,
				// in which case there is nothing to drill into and the jump
				// falls through to the retained node or the unavailable
				// detail rather than doing nothing.
				if m.drillIntoSubagent(v.ID) {
					m.invalidateTranscript()
					m.refreshViewport()
					return *m, nil
				}
				break
			}
		}
	}
	if key := item.Reference.NodeKey; key != "" {
		if id, ok := m.browseNodeForKey(key); ok {
			m.browsing = true
			m.cursor = id
			m.viewportFollow = false
			m.invalidateTranscript()
			m.refreshViewport()
			m.scrollToCursor()
			return *m, nil
		}
	}
	m.openWorkDetailUnavailable(item)
	return *m, nil
}

// browseNodeForKey resolves a stored node key to the navigable node the
// transcript is currently showing, if any.
func (m Model) browseNodeForKey(key string) (nodeID, bool) {
	for _, it := range m.browseItems {
		if it.id.Key == key {
			return it.id, true
		}
	}
	return nodeID{}, false
}

// openWorkDetailUnavailable opens the panel's bounded retained detail for an
// item whose referenced transcript position is gone, with the unavailable text
// the reader can act on.
func (m *Model) openWorkDetailUnavailable(item acpwire.WorkItem) {
	p := m.tasksPanel
	if p == nil {
		m.note("The referenced transcript position is no longer available.")
		m.refreshViewport()
		return
	}
	d := acpwire.WorkDetail{
		Item:        item,
		Unavailable: "The referenced transcript position is no longer retained.",
	}
	p.detail = &d
	p.detailOpen = true
	m.dock.Open(p)
	m.refreshViewport()
}

// promptKeyByID finds the pending request carrying id, in the same order
// focusedPromptKey resolves them.
func (m Model) promptKeyByID(id string) (promptRef, bool) {
	if id == "" {
		return promptRef{}, false
	}
	p := m.sess.Pending()
	if p.Sudo != nil && p.Sudo.Key == id {
		return promptRef{kind: promptSudo, key: id}, true
	}
	if p.SkillGate != nil && p.SkillGate.Key == id {
		return promptRef{kind: promptSkill, key: id}, true
	}
	if len(p.Approvals) > 0 && p.Approvals[0].Key == id {
		return promptRef{kind: promptApproval, key: id}, true
	}
	if p.Question != nil && p.Question.Key == id {
		return promptRef{kind: promptQuestion, key: id}, true
	}
	return promptRef{}, false
}

// navigateToWorkPrompt navigates to the pending request a work item names, by
// its canonical request ID. It closes the Tasks panel so the inline prompt owns
// the frame, and it never approves or answers the request: that decision stays
// with the user's own prompt surface.
func (m *Model) navigateToWorkPrompt(msg workRequestMsg) (Model, tea.Cmd) {
	if _, ok := m.promptKeyByID(msg.requestID); !ok {
		m.note("That request is no longer pending.")
		m.refreshViewport()
		return *m, nil
	}
	m.dock.CloseNow()
	m.tasksPanel = nil
	m.reconcileAccess()
	m.refreshViewport()
	return *m, nil
}

// openWork opens the Work list, or refreshes it when it is already the open
// panel. It refuses to open a panel with no live work, leaving a notice
// instead.
func (m *Model) openWork() {
	if m.tasksOpen() {
		m.refreshViewport()
		return
	}
	if !m.workLaneActive() {
		m.note("No work in this session.")
		m.refreshViewport()
		return
	}
	m.sheetPanel = nil
	m.tasksPanel = newTasksPanel(m)
	m.dock.Open(m.tasksPanel)
	m.refreshViewport()
}

// handleAgentTick handles an agentTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleAgentTick(msg agentTickMsg) (Model, tea.Cmd) {
	if !m.busy && m.successPulse && m.now().Sub(m.successPulseAt) >= successPulseDuration {
		m.successPulse = false
	}
	noticePending := false
	if n := m.sess.Status().Notice; n != nil {
		if m.now().Sub(acpwire.Time(n.SetAt)) >= noticeBannerDuration {
			_ = m.sess.DismissNotice(m.ctx)
			// The banner is rendered into the transcript; dismissing it
			// must repaint the viewport or the stale banner stays on screen
			// until an unrelated transcript change.
			m.refreshViewport()
		} else {
			noticePending = true
		}
	}
	if !m.busy && !m.successPulse && !noticePending {
		// The pinned work bar shows live ages and a bounded receipt window, so
		// while either is on screen the tick keeps repainting against EndedAt.
		// It never extends a receipt and never persists anything: the tick only
		// repaints.
		if m.workLaneAging() {
			m.updateViewportHeight()
			m.refreshViewport()
			return m, tea.Batch(tickCmd(), m.callerQueryCmds())
		}
		// Even when idle, newly-settled edit rows may still need their
		// blast-radius lookups issued; the query cmds no-op when there is
		// nothing left to ask.
		return m, m.callerQueryCmds()
	}
	m.updateViewportHeight()
	m.refreshViewport()
	return m, tea.Batch(tickCmd(), m.callerQueryCmds())
}

// handleSpinnerTick handles a spinnerTickMsg, shared by Update and
// handleRuntimeMessage.
func (m Model) handleSpinnerTick(msg spinnerTickMsg) (Model, tea.Cmd) {
	// A parent turn can finish while background children are still running.
	// Their cards share this animation clock, so keep ticking until they settle.
	if !m.busy && !m.sess.Status().SubagentRunning {
		return m, nil
	}
	m.spinnerFrame = m.spinner.Next()
	// The spinner tick is at 80ms (smoother than the 150ms layout tick);
	// the activity strip and the in-progress thinking/tool rows read
	// m.spinnerFrame via activeSpinnerFrame, so the viewport must
	// re-render here or the animation stays at the 150ms cadence.
	m.refreshViewport()
	return m, spinnerTickCmd()
}

// handleRuntimeMessage processes agent/steering/tick messages so they
// reach the parent model even when an overlay is open. This keeps
// parent state (busy, job count, steering, activity) current while the
// overlay remains visible.
func (m Model) handleRuntimeMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While an ending is awaiting the user's decision (or stopping), live work
	// events keep flowing: the warning reflects the runtime's current
	// membership, so work that started or finished while the prompt was open is
	// shown as it now is. The warning is never replaced by another surface
	// here, and a late approval/gate event cannot take the dock from it.
	if m.endStage == endAwaitingDecision || m.endStage == endStopping {
		switch msg.(type) {
		case changedMsg, agentTickMsg, agentFinishedMsg:
			refreshed := m
			refreshed.refreshEndWarning()
			out, cmd := refreshed.dispatchRuntimeMessage(msg)
			mm, ok := out.(Model)
			if !ok {
				return out, cmd
			}
			// A handler may have opened a surface of its own — a human gate, a
			// review cancel, an approval. The warning is the surface the user is
			// answering, so it comes back; the new surface is remembered as the
			// one to restore if the user declines, so a child parked behind it
			// is not stranded.
			if mm.endStage == endAwaitingDecision || mm.endStage == endStopping {
				if _, isWarning := mm.dock.Panel().(*activeworkpanel.Panel); !isWarning && mm.dock.IsOpen() {
					mm.endDisplaced = mm.dock.Panel()
				}
				mm.refreshEndWarning()
			}
			return mm, cmd
		}
	}
	return m.dispatchRuntimeMessage(msg)
}

// dispatchRuntimeMessage routes the parent-model runtime messages to their
// handlers. It is separate from handleRuntimeMessage so the ending path can
// refresh its warning around the same routing.
func (m Model) dispatchRuntimeMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case agentFinishedMsg:
		return m.handleAgentFinished(msg)
	case planAuthorFinishedMsg:
		return m.handlePlanAuthorFinished(msg)
	case changedMsg:
		return m.handleChanged(msg)
	case agentTickMsg:
		return m.handleAgentTick(msg)
	case spinnerTickMsg:
		return m.handleSpinnerTick(msg)
	case suggestionMsg:
		return m.handleSuggestionMsg(msg)
	case callersMsg:
		return m.handleCallers(msg)
	case detailMsg:
		return m.handleDetail(msg)
	}
	return m, nil
}

// handleSuggestionMsg applies a Phase 2 LLM fallback suggestion result,
// discarding it if a newer generation has superseded it (the user typed or
// started a new turn).
func (m Model) handleSuggestionMsg(msg suggestionMsg) (Model, tea.Cmd) {
	if msg.gen < m.suggestionGen {
		return m, nil
	}
	if msg.suggestion == "" {
		return m, nil
	}
	m.suggestion = msg.suggestion
	m.suggestionDismissed = false
	m.refreshViewport()
	return m, nil
}

func (m *Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	m.leaveBrowse() // running a command ends browsing
	// /send-now is recognized from the raw line before shlex.Split can
	// reinterpret its payload: the argument is literal steering text, so
	// quotes, newlines and interior whitespace must survive verbatim. The
	// token boundary is exact and the command must be registered, so an
	// unknown /send-now-foo is never swallowed.
	if m.isSendNowCommand(raw) {
		return m.sendNowCommand(raw)
	}
	parts, err := shlex.Split(raw)
	if err != nil {
		m.note("Invalid command syntax.")
		m.refreshViewport()
		return m, nil
	}
	if len(parts) == 0 {
		return m, nil
	}
	name := strings.TrimPrefix(parts[0], "/")
	var args []string
	if len(parts) > 1 {
		args = parts[1:]
	}

	// Retired legacy mode commands return an actionable explanation and never
	// change access: hidden aliases must not survive as a second mode surface.
	if explanation, ok := retiredInteractionCommands[name]; ok {
		m.note(explanation)
		m.refreshViewport()
		return m, nil
	}

	if len(m.sess.Commands()) == 0 {
		m.note("Command registry not available.")
		m.refreshViewport()
		return m, nil
	}
	info, ok := m.lookupCommand(name)
	if !ok {
		m.note(fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", name))
		m.refreshViewport()
		return m, nil
	}

	// Bare picker-backed commands open a modal instead of running the
	// handler; with arguments (or when there is nothing to pick) they fall
	// through to the handler unchanged.
	if len(args) == 0 {
		switch info.Name {
		case "rewind":
			if items := m.rewindPickerItems(); len(items) > 0 {
				m.openPicker("rewind", "Rewind to turn", "starts a new branch", items, "")
				m.refreshViewport()
				return m, nil
			}

		}
	}

	// Plugin-defined prompt commands replay the enter-key path: their body
	// becomes a user turn (steering when the agent is busy), with any
	// arguments appended verbatim to preserve quoting/whitespace.
	if info.PromptBody != "" {
		goal := info.PromptBody
		if len(parts) > 1 {
			rest := raw[len(parts[0]):]
			rest = strings.TrimSpace(rest)
			if rest != "" {
				goal += "\n\n" + rest
			}
		}
		if m.busy {
			m.sess.Steer(m.ctx, goal)
			m.refreshViewport()
			return m, nil
		}
		if !m.sess.Status().AgentReady {
			m.note("Agent runner is not available.")
			m.refreshViewport()
			return m, nil
		}
		mm, promptCmd := m.startPrompt(goal, false)
		return mm, promptCmd
	}

	if info.HasHandler {
		res, err := m.sess.Command(m.ctx, info.Name, args)
		if err != nil {
			m.note(err.Error())
			m.refreshViewport()
			return m, nil
		}
		if res.Doc != nil {
			m.openDocPanel(res.Doc)
		} else if res.Text != "" {
			m.note(res.Text)
		}
		// AgentGoal hands a follow-up turn back to the agent after Text is
		// rendered (see CommandResult.AgentGoal). Mirror the PromptBody
		// branch's busy/steering/runner idiom exactly.
		if res.AgentGoal != "" {
			if m.busy {
				m.sess.Steer(m.ctx, res.AgentGoal)
				m.refreshViewport()
				return m, nil
			}
			if m.sess.Status().AgentReady {
				mm, goalCmd := m.startPrompt(res.AgentGoal, false)
				return mm, goalCmd
			}
		}
	}

	if info.TUIOnly {
		effect, ok := tuiCommandEffects[info.Name]
		if !ok {
			m.note("Command /" + info.Name + " is not available in this build.")
			m.refreshViewport()
			return m, nil
		}
		return effect(m, args)
	}

	m.refreshViewport()
	return m, nil
}

// openConnect opens the connect overlay for adding/reconnecting a provider.
func (m *Model) openConnect(_ string) {
	opts := connect.Opts{
		Cfg:         m.sess.Config(),
		Discovered:  m.discovered,
		OpenBrowser: m.openBrowser,
		Host:        m.host,
	}
	// Providers are saved to the user-global config; the summary screen's
	// "save to:" line should name that file.
	opts.CfgPath = m.sess.Status().UserConfigPath
	m.connectModel = connect.New(opts)
	m.connectModel.SetSize(m.leftWidth, m.height)
	m.dock.Open(connect.Panel{Model: m.connectModel})
}

// openModels opens the connect overlay scoped to the first provider for model
// selection. When no providers are configured it falls through to openConnect.
// Returns a tea.Cmd batch that probes all connected (and uncached) providers.
func (m *Model) openModels() tea.Cmd {
	names := m.sortedProviderNames()
	if len(names) == 0 {
		m.openConnect("/")
		return nil
	}
	m.connectModel = connect.New(connect.Opts{
		Cfg:              m.sess.Config(),
		Discovered:       m.discovered,
		SkipToIntroModel: true,
		AllProviders:     true,
		ScopedProvider:   names[0],
		OpenBrowser:      m.openBrowser,
		Host:             m.host,
	})
	m.connectModel.SetSize(m.leftWidth, m.height)
	m.dock.Open(connect.Panel{Model: m.connectModel})
	return m.probeProviders(names)
}

// modelsProbeFieldID is the FieldID on the /models discovery probe this
// model dispatches. Setup probes in the connect wizard use "connect", so a
// result carrying this id is always host discovery.
const modelsProbeFieldID = "models"

// modelsProbeResultMsg carries a /models discovery completion together with
// the provider and the attempt that dispatched it. Discovery runs in the
// parent model, once per provider, so only that provider's newest attempt may
// move session discovery, the disk cache or an open picker. A global
// generation would wrongly drop another provider's legitimate result.
type modelsProbeResultMsg struct {
	Provider   string
	Generation uint64
	Result     probe.ResultMsg
}

// probeProviders dispatches a probe for each named provider that has no
// cached entry and whose endpoint is permitted by the remote-provider
// gate. Codex refreshes even with a cached entry: its catalog is versioned
// and account-specific, so a saved list may omit newly available models.
// Used by openModels and by the manual refresh key.
func (m *Model) probeProviders(names []string) tea.Cmd {
	var cmds []tea.Cmd
	cfg := m.sess.Config()
	for _, n := range names {
		pc := cfg.Providers[n]
		if cached, ok := m.discovered[n]; ok && len(cached) > 0 && pc.Type != "openai_codex" {
			continue
		}
		if !probe.IsLocalhost(pc.BaseURL) && !cfg.Privacy.RemoteProvidersAllowed {
			continue
		}
		// Count the attempt only when a probe is really dispatched, so a
		// skipped provider cannot invalidate the attempt that did run.
		if m.modelsProbeGen == nil {
			m.modelsProbeGen = map[string]uint64{}
		}
		m.modelsProbeGen[n]++
		owner, gen := n, m.modelsProbeGen[n]
		inner := probe.Provider(m.host, "models", n, pc)
		cmds = append(cmds, func() tea.Msg {
			res, ok := inner().(probe.ResultMsg)
			if !ok {
				return nil
			}
			return modelsProbeResultMsg{Provider: owner, Generation: gen, Result: res}
		})
	}
	return tea.Batch(cmds...)
}

// handleModelsProbeResult applies a /models discovery completion only when it
// belongs to the provider's newest dispatched attempt. Session discovery, the
// disk cache and an open picker are all shared, so a late older attempt must
// move none of them — a stale success would resurrect an outdated catalog and
// a stale failure would evict a good cached one. Only a live connect panel
// receives the inner result, and Task 2 restricts it to the all-provider
// model-selection step.
func (m *Model) handleModelsProbeResult(msg modelsProbeResultMsg) (tea.Model, tea.Cmd) {
	if msg.Provider == "" || msg.Generation == 0 || msg.Result.Provider != msg.Provider {
		return m, nil // unattributable result
	}
	if m.modelsProbeGen[msg.Provider] != msg.Generation {
		return m, nil // a newer attempt for this provider supersedes it
	}
	if _, ok := m.sess.Config().Providers[msg.Provider]; !ok {
		// The provider was removed while the probe was in flight. Its result
		// must not seed a cache entry (or a session map key) for a provider
		// that no longer exists.
		return m, nil
	}
	if msg.Result.Err == nil {
		msg.Result.Models = schema.PreserveThinkingOptions(msg.Result.Models, m.discovered[msg.Provider])
		m.discovered[msg.Provider] = msg.Result.Models
		m.persistDiscovered(msg.Provider, msg.Result.Models)
		if panel, ok := m.dock.Panel().(*modeloptions.Panel); ok {
			preset := m.sess.Config().Models.Presets[panel.PresetName()]
			if preset.Provider == msg.Provider {
				panel.SetThinkingOptions(m.modelThinkingOptions(panel.PresetName()))
			}
		}
	}
	// Failures carry no models, but the picker still needs to note them
	// against the provider, so both outcomes are forwarded while a panel is
	// open.
	if _, ok := m.dock.Panel().(connect.Panel); ok {
		return m, m.dock.Update(msg.Result)
	}
	return m, nil
}

// handleWizardProbeResult applies a setup probe's completion to the host's
// shared model state, but only when the live connect panel is the very wizard
// instance that launched it and that attempt is still current. A stale
// result — skipped, superseded by a retry, cancelled, or belonging to a
// replaced wizard — must touch neither the session discovery map nor the disk
// cache, whether it succeeded or failed. Otherwise a late failure could evict
// a good cached catalog, and a late success could resurrect models the user
// already moved past.
func (m *Model) handleWizardProbeResult(wr connect.WizardProbeResult) (tea.Model, tea.Cmd) {
	panel, ok := m.dock.Panel().(connect.Panel)
	if !ok || wr.Owner == nil || panel.Model != wr.Owner {
		return m, nil // the wizard was closed or replaced
	}
	if !wr.Owner.AcceptsWizardProbe(wr) {
		return m, nil // a stale attempt within the live wizard
	}
	if wr.Result.Err == nil && wr.Result.Provider != "" {
		wr.Result.Models = schema.PreserveThinkingOptions(wr.Result.Models, m.discovered[wr.Result.Provider])
		m.discovered[wr.Result.Provider] = wr.Result.Models
		m.persistDiscovered(wr.Result.Provider, wr.Result.Models)
	}
	return m, m.dock.Update(wr)
}

func (m *Model) persistDiscovered(name string, models []schema.ModelInfo) {
	_ = m.host.SaveModels(m.ctx, name, models)
}

// evictDiscovered removes one provider's entry from the host's cache.
// Used by the manual refresh key — the in-session map is already cleared
// by the caller. Failures are silent for the same reason as persist.
func (m *Model) evictDiscovered(name string) {
	_ = m.host.ForgetModels(m.ctx, name)
}

// sortedProviderNames returns provider names sorted alphabetically.
func (m *Model) sortedProviderNames() []string {
	providers := m.sess.Config().Providers
	names := make([]string, 0, len(providers))
	for k := range providers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// openDocPanel opens a command's wire doc as a docked doc panel. Its rows act
// through the session the doc came from, so a command's row actions run agent
// side.
func (m *Model) openDocPanel(doc *acpwire.WireDoc) {
	if doc == nil {
		return
	}
	m.dock.Open(docpanel.New(*doc, sessionDocActions(m.ctx, m.sess)))
	m.refreshViewport()
}

// lookupCommand finds the command whose name matches, as the registry's Lookup
// does: case-insensitively, by the lower-cased name.
func (m *Model) lookupCommand(name string) (acpwire.CommandInfo, bool) {
	name = strings.ToLower(name)
	for _, c := range m.sess.Commands() {
		if c.Name == name {
			return c, true
		}
	}
	return acpwire.CommandInfo{}, false
}

// openPicker opens a command modal. The picked value is delivered as
// picker.PickedMsg and re-enters dispatchCommand for pickerCommand, so
// command semantics stay in one place.
func (m *Model) openPicker(cmdName, title, footer string, items []picker.Item, prefilter string) {
	p := picker.New(title, footer, items)
	if prefilter != "" {
		p.SetFilter(prefilter)
	}
	m.dock.Open(p)
	m.pickerCommand = cmdName
}

// endNewArgs returns the /new arguments the pending replacement carries. It
// rebuilds the argv form the command effect received so the replacement runs
// through exactly one path.
func (m *Model) endNewArgs() []string {
	if m.end == nil || m.end.name == "" {
		return nil
	}
	return strings.Fields(m.end.name)
}

// newSessionEffect runs the replacement's session-swap half after acceptance.
// It exists on the model so the accepted-end path and the /new command effect
// share one implementation without either owning the other's lifecycle.
func (m *Model) newSessionEffect(args []string) tea.Cmd {
	name := strings.TrimSpace(strings.Join(args, " "))
	return startNewSession(m, name)
}

// ResumeSession returns the session ID the user chose to resume, or "" if
// the program ended normally. It is read by the app.Run program runner.
func (m Model) ResumeSession() string { return m.resumeSession }

// beginResume records a pending REPLACEMENT for the requested session. The
// resume ID is applied and announced only after the runtime accepts the work
// membership an end would stop, so a declined resume leaves both the current
// conversation and the resume target untouched.
func (m *Model) beginResume(id string) (tea.Model, tea.Cmd) {
	id = strings.TrimSpace(id)
	if id == "" {
		m.refreshViewport()
		return m, nil
	}
	// A session swap is not a quit: never offer the exit postmortem here.
	return m, m.beginEnd(&endIntent{kind: acpwire.EndIntentReplace, resumeID: id})
}

// openMemory opens the docked memory browser and reports whether it did. The
// entries come from the session, so the browser lists what the agent side
// sees. fromKey marks the Ctrl+K shortcut, which is silent: without a
// database it does nothing rather than noting why.
func (m *Model) openMemory(fromKey bool) bool {
	sess, ctx := m.sess, m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := sess.Memories(ctx)
	if err != nil && acpclient.IsCode(err, acpwire.CodeNoDatabase) {
		if !fromKey {
			m.note("Memory browser not available (no database configured).")
		}
		return false
	}
	// A read failure still opens the browser: the footer reports it, as a
	// failed listing did before the browser went through the seam.
	m.dock.Open(memory.NewPanel(entries, err, func(id int64) error {
		return sess.DeleteMemory(ctx, id)
	}))
	return true
}

// openSessionPicker opens a picker listing previous sessions for this
// project. The picked session ID is passed to beginResume.
func (m *Model) openSessionPicker(prefilter string) {
	st := m.sess.Status()
	sessions, err := m.host.ListSessions(m.ctx, st.WorkingDir, 100)
	if err != nil {
		if acpclient.IsCode(err, acpwire.CodeNoDatabase) {
			m.note("Session list is not available (no database).")
			return
		}
		m.note(fmt.Sprintf("Could not list sessions: %v", err))
		return
	}
	items := make([]picker.Item, 0, len(sessions))
	for _, s := range sessions {
		label := s.Title
		if label == "" {
			label = s.SessionID
		}
		// The database keeps the time in UTC; render it the same way so the
		// picker shows the same clock time it always did.
		detail := acpwire.Time(s.UpdatedAt).UTC().Format("2006-01-02 15:04") + fmt.Sprintf(" · %d messages", s.MessageCount)
		badge := ""
		if s.SessionID == m.sess.ID() {
			badge = "● now"
		}
		items = append(items, picker.Item{
			Label:  label,
			Detail: detail,
			Badge:  badge,
			Value:  s.SessionID,
		})
	}
	if len(items) == 0 {
		m.note("No previous sessions found for this project.")
		return
	}
	m.openPicker("sessions", "Resume session", "pick a previous conversation", items, prefilter)
}

// applyInteraction submits one interaction patch through the session and
// returns the committed state. The TUI only renders that result: it never
// mutates the runner, policy engine, force class, config or session state
// itself.
func (m *Model) applyInteraction(patch acpwire.InteractionPatch) (acpwire.Interaction, bool) {
	committed, err := m.sess.SetInteraction(m.ctx, patch)
	if err != nil {
		m.note(fmt.Sprintf("Could not change interaction state: %v", err))
		m.refreshViewport()
		return acpwire.Interaction{}, false
	}
	m.syncedAccessMode = committed.Mode
	return committed, true
}

// modeReceipt reports a committed mode change. Ask explicitly says that work
// already running continues: changing the mode is forward-looking, not a
// cancellation or restart.
func (m *Model) modeReceipt(state acpwire.Interaction) {
	var msg string
	switch state.Mode {
	case string(interaction.ModeAsk):
		msg = "Switched to Ask mode. New actions need approval."
		if m.busy || m.sess.Status().RunningJobs > 0 {
			msg += " Work already running continues."
		}
	case string(interaction.ModeAct):
		msg = "Switched to Act mode. Routine workspace work proceeds without asking."
	default:
		msg = fmt.Sprintf("Switched to %s mode.", state.Mode)
	}
	if state.Auto {
		msg += " Auto is on."
	}
	if state.System {
		msg += " System access is on."
	}
	m.note(msg)
	m.refreshViewport()
}

// setInteractionMode applies one canonical Ask/Act transition. Auto is cleared
// by interaction.Apply whenever Mode is patched; System is preserved.
func (m *Model) setInteractionMode(mode interaction.Mode) {
	value := string(mode)
	if state, ok := m.applyInteraction(acpwire.InteractionPatch{Mode: &value}); ok {
		m.modeReceipt(state)
	}
}

// setAuto applies the /auto transition table. An explicit on enters Act even
// from Ask; off clears Auto and preserves the current mode and System.
func (m *Model) setAuto(args []string) {
	on := false
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "on":
			on = true
		case "off":
			on = false
		default:
			m.note("Usage: /auto [on|off]")
			m.refreshViewport()
			return
		}
	} else {
		on = !m.sess.Status().Interaction.Auto
	}
	if _, ok := m.applyInteraction(acpwire.InteractionPatch{Auto: &on}); !ok {
		return
	}
	if on {
		m.note("Full Auto enabled: operations and System access are preapproved; no questions.")
	} else {
		m.note("Auto disabled; the current mode and System access are unchanged.")
	}
	m.refreshViewport()
}

// setSystem applies the /system on|off|toggle transition. Mode and Auto are
// preserved; /system unlock remains a separate control handled by the caller.
func (m *Model) setSystem(args []string) {
	on := false
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "on":
			on = true
		case "off":
			on = false
		default:
			m.note("Usage: /system [on|off|unlock]")
			m.refreshViewport()
			return
		}
	} else {
		on = !m.sess.Status().Interaction.System
	}
	if _, ok := m.applyInteraction(acpwire.InteractionPatch{System: &on}); !ok {
		return
	}
	if on {
		m.note("System access enabled for this session.")
	} else {
		m.note("System access disabled.")
	}
	m.refreshViewport()
}

// cycleMode toggles Ask and normal Act. Both Tab directions toggle, and Auto is
// never restored implicitly because a Mode patch clears it.
func (m *Model) cycleMode(forward bool) {
	_ = forward // Tab and Shift+Tab both toggle Ask ↔ Act.
	current := interaction.Mode(m.sess.Status().Interaction.Mode)
	next := interaction.ModeAct
	if current == interaction.ModeAct {
		next = interaction.ModeAsk
	}
	m.setInteractionMode(next)
}

// cycleModel advances (forward=true) or reverses the active model preset,
// wrapping around. Order matches modelPickerItems() (provider then name).
// Session-only: delegates to switchModelPreset.
func (m *Model) cycleModel(forward bool) {
	if m.busy {
		m.note("Busy — switch the model after this turn completes.")
		m.refreshViewport()
		return
	}
	names := m.sortedPresetNames()
	if len(names) == 0 {
		m.note("No model presets configured. Use /connect to add a provider or /profiles to assign a discovered model.")
		m.refreshViewport()
		return
	}
	cur := m.sess.Status().ActiveRoute.Preset
	idx := slices.Index(names, cur)
	if idx < 0 {
		idx = 0 // legacy/unknown route → start at the first preset
	}
	step := 1
	if !forward {
		step = -1
	}
	target := names[(idx+step+len(names))%len(names)]
	m.switchModelPreset(target)
	m.refreshViewport()
}

// openSDDPlanPicker lists the plans a run can execute, with each plan's
// task count and resume state resolved up front. A plan that will not parse
// is listed with its error rather than hidden, and a partly-finished run is
// pinned to the top as an explicit resume.
func (m *Model) openSDDPlanPicker() {
	cfg := m.sess.Config()
	candidates, err := m.sess.Plans(m.ctx)
	if err != nil {
		candidates = nil
	}

	var items []picker.Item
	// Resumable runs first: re-picking the plan is how a stopped run is
	// continued, and that was previously undiscoverable.
	for _, c := range candidates {
		if !c.Resumable {
			continue
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("Resume %s — task %d/%d", c.Slug, c.Done+1, c.Tasks),
			Detail: fmt.Sprintf("%d/%d done · from the ledger", c.Done, c.Tasks),
			Badge:  "resume",
			Value:  c.Path,
		})
	}
	for _, c := range candidates {
		item := picker.Item{Label: c.Name, Value: c.Path}
		switch {
		case c.Error != "":
			item.Detail = c.Error
			item.Badge = "unreadable"
		case c.LedgerError != "":
			item.Detail = fmt.Sprintf("%d tasks · ledger unreadable", c.Tasks)
			item.Badge = "unreadable"
		case c.Resumable:
			item.Detail = fmt.Sprintf("%d tasks · %d done", c.Tasks, c.Done)
		default:
			item.Detail = fmt.Sprintf("%d tasks", c.Tasks)
		}
		items = append(items, item)
	}

	if len(candidates) == 0 {
		items = append(items, picker.Item{
			Label:  "Write a starter plan",
			Detail: "creates a template in " + cfg.SDD.PlansDir,
			Value:  sddScaffoldPlanValue,
		})
	}
	items = append(items, picker.Item{
		Label:  "Custom plan path...",
		Detail: "type or paste a path",
		Value:  sddCustomPlanPathValue,
	})

	p := picker.New("Pick a plan to run", "subagent-driven development", items)
	p.SetAllowCustom(true)
	m.dock.Open(p)
	m.pickerCommand = "sdd-plan"
}

// openSDDCustomPlanPathPicker opens a dedicated picker for typing or pasting
// a plan path that was not auto-detected in the SDD plans directory.
func (m *Model) openSDDCustomPlanPathPicker() {
	p := picker.New("Custom plan path", "type or paste a path below", []picker.Item{})
	p.SetAllowCustom(true)
	m.dock.Open(p)
	m.pickerCommand = "sdd-plan"
}

// applyConnectDone persists the provider and model chosen through the
// connect overlay. It writes the provider entry to cfg.Providers,
// materializes a preset for the provider/model pair, sets the default profile
// to the single-model profile name with the new preset as active, and saves.
func (m *Model) applyConnectDone(msg connect.DoneMsg) {
	if msg.Provider == "" || msg.Model == "" {
		return
	}
	// One read of the session config: newCfg is mutated below, cfg is the
	// pre-edit snapshot the saves at the end diff against.
	cfg := m.sess.Config()
	newCfg := cfg
	if newCfg.Providers != nil {
		copied := make(map[string]config.ProviderConfig, len(newCfg.Providers)+1)
		for k, v := range newCfg.Providers {
			copied[k] = v
		}
		newCfg.Providers = copied
	} else {
		newCfg.Providers = map[string]config.ProviderConfig{}
	}
	if msg.ProviderCfg.Type != "" {
		// pc is an explicit copy: ProviderConfig is a value struct, and both
		// this map entry and the SaveUserConfigProviders call below
		// must observe the same cleared APIKeyEnv.
		pc := msg.ProviderCfg
		// A literal key is persisted to the user config below. Ensure the
		// saved entry does not retain a stale api_key_env reference from the
		// provider template, which would shadow the saved literal key during
		// layered config merge.
		if pc.APIKey != "" {
			pc.APIKeyEnv = ""
		}
		newCfg.Providers[msg.Provider] = pc
	}
	if msg.EnabledRemote {
		newCfg.Privacy.RemoteProvidersAllowed = true
	}
	// Selecting a model synthesizes a preset plus a profile binding every
	// role to it. The router resolves through profiles only; there is no
	// legacy pair and no implicit "empty default means fall through".
	if newCfg.Models.Presets != nil {
		presets := make(map[string]routing.ModelPreset, len(newCfg.Models.Presets)+1)
		for k, v := range newCfg.Models.Presets {
			presets[k] = v
		}
		newCfg.Models.Presets = presets
	} else {
		newCfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	// Merge the presets the connect wizard prepared for every reported model.
	// A preset the setup's provenance manages carries refreshed catalog
	// values, so it replaces the live entry; an unmanaged entry is the user's
	// own and is kept (Apply refuses to mutate those anyway). The active
	// model's preset is materialized below, which only ever fills or grows a
	// figure, so it cannot undo a refresh.
	var managed map[string]bool
	if len(msg.Setup.Managed) > 0 {
		managed = make(map[string]bool, len(msg.Setup.Managed))
		for _, name := range msg.Setup.Managed {
			managed[name] = true
		}
	}
	for name, preset := range msg.Presets {
		if _, exists := newCfg.Models.Presets[name]; exists && !managed[name] {
			continue
		}
		newCfg.Models.Presets[name] = preset
	}
	presetName, err := presetflow.Materialize(&newCfg, msg.Provider, msg.Model, msg.ProviderCfg.BaseURL, presetflow.Limits{
		ContextWindow: msg.ContextWindow,
		ToolCalling:   msg.ToolCalling,
	})
	if err != nil {
		m.note(fmt.Sprintf("✗ Failed to materialize model preset: %v", err))
		return
	}

	newCfg.Profile.Default = singleModelProfileName
	newCfg.Profile.ActivePreset = presetName
	// The "single" profile is synthesized in memory by RoutingConfig when no
	// explicit agent_profiles entry matches the default profile. When
	// ActivePreset is set, RoutingConfig always (re)synthesizes from it,
	// overwriting any stale entry left by a legacy migration. Do not persist
	// it here; that keeps the config file limited to provider + model pair.

	// Providers and presets are user-global; SaveProjectConfig never writes
	// them. Persist both to the user config first — credentials included.
	// If that fails, configure nothing rather than leaving a provider entry
	// pointing at a key that was never saved. The in-memory config keeps the
	// key so the reloaded runtime can use it.
	// Baseline: the session's config, read once above before any mutation.
	// The provider/preset maps were copied into newCfg before mutation, so
	// that snapshot still carries the pre-edit membership: entries another
	// marshal process persisted after this process loaded are on disk but in
	// neither the write nor the baseline, and the merge preserves them
	// instead of clobbering (the cross-process regression fixed here).
	//
	// The userHome gate stays even though the adapter resolves the same path
	// itself: it keeps the miss reported as a missing home directory rather
	// than as a failed save, matching every other user-global write here.
	if _, err := m.userHome(); err != nil {
		m.note(fmt.Sprintf("✗ Failed to locate home directory: %v", err))
		return
	}
	if err := m.host.SaveUserProviders(m.ctx, newCfg.Providers, cfg.Providers, nil); err != nil {
		m.note(fmt.Sprintf("✗ Failed to save provider: %v", err))
		return
	}
	if err := m.host.SaveUserPresets(m.ctx, newCfg.Models.Presets, cfg.Models.Presets); err != nil {
		m.note(fmt.Sprintf("✗ Failed to save model preset: %v", err))
		return
	}
	// The consent answer is a user-global preference; persist it only when the
	// wizard actually asked (nil means the user has never answered).
	if msg.EnrichmentConsent != nil {
		if err := m.host.SaveUserValue(m.ctx, "privacy.models_dev_enrichment", *msg.EnrichmentConsent); err != nil {
			m.note(fmt.Sprintf("⚠ Could not save models.dev preference: %v", err))
		}
	}
	// Provenance is written after the presets: if it fails, the presets are
	// already saved and the next refresh fails safe rather than overwriting a
	// user edit. An unreadable sidecar gave no raw value, so there is nothing
	// to save and every preset stays unmanaged.
	if len(msg.Setup.Provenance) > 0 {
		if err := m.host.SaveProvenance(m.ctx, msg.Setup.Provenance); err != nil {
			m.note(fmt.Sprintf("⚠ Presets saved, but models.dev provenance was not: %v", err))
		}
	}

	saveErr, reloadErr := m.persistAndReload(newCfg)
	switch {
	case saveErr != nil:
		m.note(fmt.Sprintf("✗ Failed to save model: %v", saveErr))
	case reloadErr != nil:
		m.note(fmt.Sprintf("✗ Failed to switch model: %v", reloadErr))
	default:
		// A successful switch proves provider construction works with the
		// new config — clear any stale provider/config notice.
		_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeProvider)
		_ = m.sess.ClearNotice(m.ctx, acpwire.NoticeConfig)
		m.note(fmt.Sprintf("✓ Switched to model: %s (%s)", msg.Model, msg.Provider))
	}
}

// switchModelPreset applies a model switch by binding every role to the given
// preset through a single-model profile. The change is persisted before
// the runtime is asked to reload, matching the /set and settings.ChangedMsg
// contracts.
func (m *Model) switchModelPreset(presetName string) {
	// One read of the session config: newCfg is mutated below, cfg is the
	// pre-edit snapshot the user-config save at the end diffs against.
	cfg := m.sess.Config()
	newCfg := cfg
	provider, model, ok := strings.Cut(presetName, "/")
	if !ok || provider == "" || model == "" {
		m.note(fmt.Sprintf("✗ Invalid preset pair: %s", presetName))
		return
	}
	if _, ok := newCfg.Providers[provider]; !ok {
		m.note(fmt.Sprintf("✗ Provider %q is not configured", provider))
		return
	}
	// Ensure an override exists if the user explicitly named a preset.
	presetCreated := false
	if _, ok := newCfg.Models.Presets[presetName]; !ok {
		if newCfg.Models.Presets == nil {
			newCfg.Models.Presets = map[string]routing.ModelPreset{}
		}
		local := routing.IsLocalProvider(newCfg.Providers[provider].BaseURL)
		// Seed the same documented pair presetflow.Materialize persists, so
		// a preset created here and one created by /connect carry identical
		// explicit figures instead of one relying on resolution-time defaults.
		defCtx, defOut := routing.DefaultModelLimits(local)
		newCfg.Models.Presets[presetName] = routing.ModelPreset{
			Name:            presetName,
			Provider:        provider,
			Model:           model,
			ContextWindow:   defCtx,
			MaxOutputTokens: defOut,
			LocalOnly:       local,
		}
		presetCreated = true
	}
	preset := newCfg.Models.Presets[presetName]
	newCfg.Profile.Default = singleModelProfileName
	newCfg.Profile.ActivePreset = presetName
	// In-memory profile synthesis in RoutingConfig handles role bindings.
	// When ActivePreset is set, RoutingConfig always (re)synthesizes the
	// "single" profile from it, overwriting any stale entry left by a
	// legacy [agent] model migration.

	// Presets are user-global; persist a newly created override to the user
	// config before the project save ([profile] lives there).
	if presetCreated {
		if _, err := m.userHome(); err != nil {
			m.note(fmt.Sprintf("✗ Failed to locate home directory: %v", err))
			return
		}
		// Baseline: the shared preset map (which just gained the created
		// override). Model switches only ever add a preset, so baseline
		// membership equals pre-switch disk membership plus the new key:
		// the merge keeps presets other marshal processes wrote after this
		// process loaded its snapshot.
		if err := m.host.SaveUserPresets(m.ctx, newCfg.Models.Presets, cfg.Models.Presets); err != nil {
			m.note(fmt.Sprintf("✗ Failed to save model preset: %v", err))
			return
		}
	}

	saveErr, reloadErr := m.persistAndReload(newCfg)
	switch {
	case saveErr != nil:
		m.note(fmt.Sprintf("✗ Failed to save model preset: %v", saveErr))
	case reloadErr != nil:
		m.note(fmt.Sprintf("✗ Failed to switch model: %v", reloadErr))
	default:
		m.note(fmt.Sprintf("✓ Switched to model: %s (%s)", presetName, preset.Model))
	}
}

// resolveModelArg maps a /models argument to a preset name: first an exact
// preset-name match, then a bare model ID (e.g. "glm-5.2") against each
// preset's Model field. Model-ID matches scan presets in sorted order so
// the result is deterministic when several presets share a model.
func (m *Model) resolveModelArg(arg string) (string, bool) {
	presets := m.sess.Config().Models.Presets
	if _, ok := presets[arg]; ok {
		return arg, true
	}
	for _, name := range m.sortedPresetNames() {
		if presets[name].Model == arg {
			return name, true
		}
	}
	return "", false
}

// sortedPresetNames returns model preset names sorted by provider then name,
// matching the order used by modelPickerItems. Shared by the model picker and
// the Alt+M hotkey so they stay in lock-step.
func (m *Model) sortedPresetNames() []string {
	presets := m.sess.Config().Models.Presets
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		pi, pj := presets[names[i]], presets[names[j]]
		if pi.Provider != pj.Provider {
			return pi.Provider < pj.Provider
		}
		return names[i] < names[j]
	})
	return names
}

// modelPickerItems builds sorted picker items from configured model presets.
func (m *Model) modelPickerItems() []picker.Item {
	presets := m.sess.Config().Models.Presets
	names := m.sortedPresetNames()
	current := m.sess.Status().ActiveRoute.Preset
	items := make([]picker.Item, 0, len(names))
	for _, n := range names {
		p := presets[n]
		var badges []string
		if n == current {
			badges = append(badges, "● now")
		}
		if p.LocalOnly {
			badges = append(badges, "local")
		}
		items = append(items, picker.Item{
			Group:  p.Provider,
			Label:  n,
			Detail: p.Provider + "/" + p.Model,
			Badge:  strings.Join(badges, " "),
			Value:  n,
		})
	}
	return items
}

// rewindPickerItems builds picker items from user turns, newest first.
// The most recent turn carries a "● last" badge and is the default cursor target.
func (m *Model) rewindPickerItems() []picker.Item {
	page, err := m.sess.Messages(m.ctx, acpwire.MessageQuery{Role: acpwire.RoleUser})
	if err != nil {
		return nil
	}
	turns := page.Messages
	items := make([]picker.Item, 0, len(turns))
	for i := len(turns) - 1; i >= 0; i-- {
		badge := ""
		if i == len(turns)-1 {
			badge = "● last"
		}
		items = append(items, picker.Item{
			Label:  fmt.Sprintf("turn %d", i+1),
			Detail: strutil.Truncate(strings.ReplaceAll(turns[i].Content, "\n", " "), 50, false),
			Badge:  badge,
			Value:  strconv.Itoa(i + 1),
		})
	}
	return items
}

// modePickerItems builds picker items for the two canonical interaction modes.
// The current mode carries a "● now" badge.
func (m *Model) modePickerItems() []picker.Item {
	current := m.sess.Status().Interaction.Mode
	badge := func(v string) string {
		if v == current {
			return "● now"
		}
		return ""
	}
	return []picker.Item{
		{Label: "Ask", Detail: "new actions need approval", Badge: badge("ask"), Value: "ask"},
		{Label: "Act", Detail: "routine workspace work proceeds", Badge: badge("act"), Value: "act"},
	}
}

func visibleRunes(s string) int {
	return ansi.StringWidth(s)
}

var activeTheme theme.Theme

// Package-level shortcuts for the active theme's slots, named for what they
// mean rather than what they look like.
//
// There used to be two parallel sets here — an appearance-named one (coral,
// gold, teal, orange, mauve) alongside this semantic one — aliasing the same
// slots, so coralColor and accentColor were literally the same value. That let
// call sites pick colors by appearance, which is how a palette drifts, and it
// reintroduced one layer down exactly what the theme package's doc forbids.
var (
	accentColor   color.Color // AccentPrimary
	violetColor   color.Color // AccentSecondary
	tertiaryColor color.Color // AccentTertiary
	borderColor   color.Color // BorderMuted
	userColor     color.Color // UserPrompt
	dimColor      color.Color // FGMuted
	successColor  color.Color // StatusSuccess
	warningColor  color.Color // StatusWarning
	errorColor    color.Color // StatusError

	dimSeparator = " · "
)

func loadTheme(tui config.TUIConfig) {
	activeTheme = theme.LoadWithConfig(tui.Theme, tui.Mode, theme.ParseDepth(tui.Depth), theme.PaletteOverrides(tui.Palette))

	accentColor = activeTheme.AccentPrimary
	violetColor = activeTheme.AccentSecondary
	tertiaryColor = activeTheme.AccentTertiary
	borderColor = activeTheme.BorderMuted
	userColor = activeTheme.UserPrompt
	dimColor = activeTheme.FGMuted
	successColor = activeTheme.StatusSuccess
	warningColor = activeTheme.StatusWarning
	errorColor = activeTheme.StatusError

	theme.Reload(activeTheme)
}

// Style helpers — lazy reads from theme.Current() so theme reloads propagate.
// mutedStyle delegates to the shared theme.MutedStyle so the monochrome
// check lives in one place.
func mutedStyle() lipgloss.Style { return theme.MutedStyle() }
func panelTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGEmphasis).Bold(true)
}

// thinkingLineStyle is muted only. Italic used to be layered on top, but
// terminals render SGR 3 as reverse video or drop it entirely, and muted
// italic was the least legible combination in the UI. The ⚙ glyph already
// marks the line as reasoning.
func thinkingLineStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGMuted)
}
func codeSurfaceStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().BGSurface).
		Padding(0, 1)
}
func toolNameStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}
func keyHintStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Bold(true)
}
func riskLabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Bold(true)
}
func warningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning).Bold(true)
}
func errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusError).Bold(true)
}
func statusOkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess)
}
func statusBusyStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary)
}
func promptPrefixStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentPrimary).Bold(true)
}
func toolBulletStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}
func statusBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().FGDefault)
}
func browserGlyphStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentTertiary)
}

// flags packs boolean/len state into a single uint64 for the hash.
func flags(streamLen int, busy bool, nTodos, nQueued int) uint64 {
	var f uint64
	if busy {
		f |= 1
	}
	f |= uint64(streamLen) << 1
	f |= uint64(nTodos) << 21
	f |= uint64(nQueued) << 41
	return f
}
