package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/diagnostics"
	"marshal/internal/index"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

const (
	defaultMaxOutputBytes = 200000
	defaultTestCommand    = "go test ./..."
)

type FileTracker interface {
	RecordRead(path string, at time.Time) error
	LastReadTime(path string) (time.Time, bool, error)
	RecordWrite(path string, at time.Time) error
}

// LSPSync is the live language-server seam (lsp.Sync): BeginEdit brackets a
// write to report the errors it caused; Rescan announces files changed by
// shell commands. Nil when no language server is configured.
type LSPSync interface {
	BeginEdit(paths []string) (func(ctx context.Context) string, bool)
	Rescan()
}

type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
	SessionState   *session.State
	// WorkspaceState is the state that owns the workspace, as opposed to
	// SessionState which is the agent doing the work. They differ only for
	// agent.run children: a child gets its own SessionState so its active
	// tool call, streamed output, approvals and questions are its own, but
	// shares the parent's WorkspaceState so /undo and /rollback still see
	// its edits — there is one working tree, not one per agent.
	//
	// Unset means "same as SessionState", which is every caller but the
	// subagent factory.
	WorkspaceState *session.State
	// AccessRequests reports that the caller has attached an access resolver
	// and a transport that can present a capability request (the TUI or ACP).
	// It gates mode.request: with it false the tool fails closed instead of
	// publishing a prompt nothing can settle. Defaults false, so a standalone
	// registration never offers escalation.
	AccessRequests bool
	DB             *db.DB
	ProjectID      int64
	FileTracker    FileTracker
	Config         config.Config
	// JobManager is the conversation's command service: the owner every
	// ordinary shell.run/test.run admission goes through, and the target an
	// explicit turn stop reaches. It is injected, never created here, so a
	// rebuild (config reload, child registry, pipeline dispatch) borrows the
	// conversation's one supervisor instead of quietly owning a second one.
	//
	// A filtered read-only registration may omit it, but then those command
	// tools are not registered at all; a registry that does expose them must
	// inject a service, or dispatch fails before launching anything.
	JobManager *JobManager
	// CommandDir, when non-empty, is the working directory a managed command
	// captures at admission, in preference to the session's active root. A
	// pipeline dispatch sets it to its own worktree so its commands run there
	// instead of following a parent-global root that the next dispatch or
	// workspace change would have moved.
	CommandDir string
	// JobBroker, when non-nil, is wired into the JobManager so every change
	// in the running-job count publishes a JobEvent. F19 broker wiring.
	JobBroker *pubsub.Broker[JobEvent]
	// AdditionalRoots extends the allowed workspace surface for tool
	// execution beyond the primary workspace root. Each root is checked
	// when resolving relative paths; paths that escape ALL roots are
	// rejected. May be nil when no extra directories are configured.
	AdditionalRoots []string

	// NamedRoots maps alias prefixes (e.g. "@run") to approved artifact
	// directories. Paths starting with an alias are resolved against the
	// mapped root instead of the workspace root. Used by pipeline
	// dispatches to give workers access to briefs and reports outside the
	// worktree without exposing absolute paths.
	NamedRoots map[string]string

	// Guardrail is invoked by shell.run / test.run after policy
	// evaluation, as a final pre-flight check. Returning a non-nil
	// error aborts the command with a tool error. Typically wired to
	// (*policy.PolicyEngine).GuardrailCheck in app.go. The system flag
	// is the toolset's own session state, so the pre-flight applies the
	// same catastrophic floor as the engine. Optional; when nil, no
	// guardrail check is performed (the policy engine already ran
	// Evaluate upstream in the agent loop).
	Guardrail func(command string, system bool) error

	// SudoPolicy validates a bare command submitted to shell.sudo. Wired to
	// (*policy.PolicyEngine).CheckPrivileged in app.go. Optional; when nil,
	// shell.sudo refuses every command (fail closed).
	SudoPolicy func(command string) error

	// Symbols answers symbol-name lookups for the `symbols` tool: through a
	// language server when one is ready, else through the symbol index. Set
	// unconditionally; the query tolerates a nil language-server handle.
	Symbols SymbolQuerier

	// LSPSync is the live language-server seam behind edit diagnostics and
	// post-command rescans. nil when LSP is unavailable.
	LSPSync LSPSync

	// LSPIndex is an optional LSP-backed symbol provider consulted by the
	// symbol-index build. nil when LSP is unavailable.
	LSPIndex index.LSPSymbols

	// GitOps is the git seam behind workspace.finish. nil defaults to
	// worktree.CLIGitOps; tests inject worktree.FakeGitOps.
	GitOps worktree.GitOps
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Command        string
	Dir            string
	Timeout        time.Duration
	MaxOutputBytes int
	// RetainOutputTail keeps recent output and forwards all observer bytes.
	// The default preserves prefix capture for existing non-managed callers.
	RetainOutputTail bool
	Stdout           io.Writer
	Stderr           io.Writer
	OnStart          func(pid int)
	// Stdin, when non-nil, is written to the child's stdin. The privileged
	// runner uses it to deliver the sudo password over a pipe — never
	// os.Stdin, never a PTY, so the secret cannot be echoed or captured. The
	// caller MUST zero the slice after Run returns.
	Stdin []byte
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Meta is populated by sandbox backends (see internal/sandbox) to
	// record how the command was executed. The default execRunner leaves
	// it zero-valued. It flows through to the audit trail via ToolResult.
	Meta registry.SandboxMeta
}

type toolSet struct {
	root            string
	additionalRoots []string
	namedRoots      map[string]string
	runner          CommandRunner
	testCommand     string
	maxOutputBytes  int
	sessionState    *session.State
	workspaceState  *session.State
	// accessRequests reports whether the caller promised an attached access
	// resolver/transport. Defaults false: a standalone registration (tests,
	// embedded registries) has nobody to answer a capability request, so
	// mode.request fails closed rather than publishing a prompt no transport
	// can settle.
	accessRequests bool
	db             *db.DB
	projectID      int64
	fileTracker    FileTracker
	// jobManager is the conversation's command service, injected through
	// Options. nil for a registry that exposes no ordinary command execution
	// (a filtered read-only scope); such a registry reports a clear owner error
	// on dispatch rather than quietly creating a second supervisor.
	jobManager *JobManager
	// commandDir, when non-empty, is the working directory a managed command
	// captures at admission in preference to the session's active root. A
	// pipeline dispatch names its own worktree so its commands do not follow a
	// parent-global root.
	commandDir  string
	diagnostics *diagnostics.Checker
	registry    *registry.Registry

	webEnabled      bool
	webFetchTimeout time.Duration
	webSearchURL    string
	webSearchKey    string

	// Test-only hooks. nil in production.
	webHTTPClient *http.Client
	ssrfCheck     func(*url.URL) bool

	guardrail func(command string, system bool) error

	// sudoPolicy validates a bare command submitted to shell.sudo. nil means
	// shell.sudo refuses every command (fail closed).
	sudoPolicy func(command string) error

	config config.Config

	// maxSearchableFileBytes caps the size of individual files that
	// repo.search will read from disk. Files larger than this threshold
	// are silently skipped. Defaults to 1 MiB (from IndexingConfig).
	maxSearchableFileBytes int64

	// resolveEmbedder returns the configured Embedder or an error. Injected so
	// tests can supply a fake; production wiring resolves via the router.
	resolveEmbedder func() (embedding.Embedder, error)

	// symbols answers symbol-name lookups for the `symbols` tool. nil when
	// no querier is wired.
	symbols SymbolQuerier

	// lspSync is the live language-server seam: it brackets writes so the
	// errors they caused come back with the edit, and announces files
	// changed by shell commands so open documents are re-synced. nil when
	// no language server is configured.
	lspSync LSPSync

	// lspIndex is an optional LSP-backed symbol provider consulted by the
	// symbol-index build. nil when LSP is unavailable.
	lspIndex index.LSPSymbols

	// gitOps is the git seam behind workspace.finish. Defaults to
	// worktree.CLIGitOps; tests inject worktree.FakeGitOps.
	gitOps worktree.GitOps
}

func RegisterAll(reg *registry.Registry, opts Options) error {
	tools, err := newToolSet(opts)
	if err != nil {
		return err
	}
	tools.registry = reg

	all := []registry.Tool{
		tools.fileReadTool(),
		tools.fileWritePatchTool(),
		tools.fileEditTool(),
		tools.fileWriteTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.gitLogTool(),
		tools.gitBlameTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
		tools.todoWriteTool(),
		tools.questionAskTool(),
		tools.modeRequestTool(),
		tools.toolsSelectTool(),
		tools.workspaceWorktreeTool(),
		tools.workspaceFinishTool(),
	}
	// codebase_search only works with an embedding route; offering it
	// otherwise teaches the model a tool that answers "unavailable".
	if _, err := routing.NewStaticRouter(opts.Config.RoutingConfig()).ResolveEmbedding(); err == nil {
		all = append(all, tools.codebaseSearchTool())
	}
	// shell.sudo is platform-gated (no sudo on Windows) and Deferred: its
	// full contract is disclosed on demand via tools.select.
	all = append(all, tools.sudoTools()...)
	// symbols answers through the querier (language server first, symbol
	// index as fallback); with no querier wired it could only fail.
	if tools.symbols != nil {
		all = append(all, tools.symbolsTool())
	}
	if tools.sessionState != nil {
		// transcript_read reads the live session transcript (and archived
		// generations via the db, when wired). It needs only the session
		// state to exist; the db is consulted lazily by the archived path
		// and by the "query" search that absorbed recall_history.
		all = append(all, tools.transcriptReadTool())
	}
	// agent.run is registered separately by app.Run after the policy engine
	// is constructed; the native toolset does not have access to the engine
	// until then. See internal/app/app.go.
	all = append(all, tools.configReadTool())
	if tools.webEnabled {
		all = append(all, tools.webFetchTool())
		if tools.webSearchURL != "" {
			all = append(all, tools.webSearchTool())
		}
	}
	for _, tool := range all {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}

	return nil
}

// activeRoot returns the directory tools currently operate in: the
// session's active root when a session is wired, else the root captured
// at construction (tests, and sessions that never enter a worktree).
// Reading through the session on each call avoids a second, racy source
// of truth.
//
// The empty-ActiveRoot fallback is defensive: SetWorkspace coerces an
// empty ActiveRoot to ProjectRoot before storing, so the only way to
// observe ActiveRoot == "" here is if a future caller bypasses the
// setter. Falling back to the construction root is the safe choice in
// that case.
func (t *toolSet) activeRoot() string {
	if ws := t.wsState(); ws != nil {
		if w := ws.Workspace(); w.ActiveRoot != "" {
			return w.ActiveRoot
		}
	}
	return t.root
}

// commandRoot returns the working directory a managed command captures at
// admission. A registry given an explicit CommandDir (a pipeline dispatch bound
// to its own worktree) uses it; everything else follows the session's active
// root. The value is read at admission and recorded on the execution, so a
// later workspace change or a sibling dispatch cannot redirect a command that
// already started.
func (t *toolSet) commandRoot() string {
	if t.commandDir != "" {
		return t.commandDir
	}
	return t.activeRoot()
}

// foregroundWait is how long an ordinary shell.run/test.run call waits for its
// command before the command continues in the background. It comes from the
// config this toolset was built with — a reload builds a new toolset — so a
// settings change applies to future calls and never re-times a wait already in
// flight. Config validation rejects a non-positive
// tools.shell.foreground_wait_seconds, so the fallback only covers a toolset
// built without a config (tests, embedded callers).
func (t *toolSet) foregroundWait() time.Duration {
	if seconds := t.config.Tools.Shell.ForegroundWaitSeconds; seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultForegroundWait
}

// systemAccess reports whether this toolset's session has the
// system-access modifier. Nil-safe: no session state means no system
// access — containment is enforced as today.
func (t *toolSet) systemAccess() bool {
	if t.sessionState == nil {
		return false
	}
	return t.sessionState.SystemAccess()
}

// wsState returns the workspace-scoped state: WorkspaceState when set,
// otherwise the per-agent SessionState. Every workspace-scoped call site
// must go through this rather than reading sessionState directly.
func (t *toolSet) wsState() *session.State {
	if t.workspaceState != nil {
		return t.workspaceState
	}
	return t.sessionState
}

func newToolSet(opts Options) (*toolSet, error) {
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("workspace root is required")
	}

	root, err := filepath.Abs(opts.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}

	additionalRoots := make([]string, 0, len(opts.AdditionalRoots))
	for _, r := range opts.AdditionalRoots {
		abs, aerr := filepath.Abs(r)
		if aerr != nil {
			return nil, fmt.Errorf("resolve additional root %q: %w", r, aerr)
		}
		additionalRoots = append(additionalRoots, abs)
	}

	runner := opts.CommandRunner
	if runner == nil {
		runner = execRunner{}
	}

	testCommand := opts.TestCommand
	if testCommand == "" {
		testCommand = defaultTestCommand
	}

	maxOutputBytes := opts.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	// The command service is the conversation's, and only the conversation's
	// owner creates it (app.buildAgentRunnerWithLock, once). A toolset that
	// borrowed one must not replace its count/broker/exit sinks either: those
	// belong to the owner, and a reload or child registry that re-installed them
	// would repoint the parent's evidence at a rebuilding toolset. A nil service
	// is allowed here and reported by the command dispatch itself, before
	// anything is launched.
	jobManager := opts.JobManager

	ts := &toolSet{
		root:            root,
		additionalRoots: additionalRoots,
		namedRoots:      opts.NamedRoots,
		runner:          runner,
		testCommand:     testCommand,
		maxOutputBytes:  maxOutputBytes,
		sessionState:    opts.SessionState,
		workspaceState:  opts.WorkspaceState,
		accessRequests:  opts.AccessRequests,
		db:              opts.DB,
		projectID:       opts.ProjectID,
		fileTracker:     opts.FileTracker,
		jobManager:      jobManager,
		commandDir:      opts.CommandDir,
		diagnostics:     newDiagnosticsChecker(opts),

		guardrail:              opts.Guardrail,
		sudoPolicy:             opts.SudoPolicy,
		webEnabled:             opts.Config.Web.Enabled,
		webFetchTimeout:        opts.Config.Web.FetchTimeout,
		webSearchURL:           opts.Config.Web.SearchURL,
		webSearchKey:           opts.Config.Web.SearchKey,
		ssrfCheck:              isPrivateURL,
		config:                 opts.Config,
		maxSearchableFileBytes: opts.Config.Indexing.MaxSearchableFileBytes,
		resolveEmbedder: func() (embedding.Embedder, error) {
			router := routing.NewStaticRouter(opts.Config.RoutingConfig())
			route, err := router.ResolveEmbedding()
			if err != nil {
				return nil, err // includes routing.ErrEmbeddingNotConfigured
			}
			pc := opts.Config.Providers[route.Preset.Provider]
			return embedding.NewFromConfig(route.Preset.Provider, pc, route.Preset.Model)
		},
		symbols:  opts.Symbols,
		lspSync:  opts.LSPSync,
		lspIndex: opts.LSPIndex,
		gitOps:   opts.GitOps,
	}
	if ts.gitOps == nil {
		ts.gitOps = worktree.CLIGitOps{}
	}
	return ts, nil
}
