package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/capture"
	"marshal/internal/db"
	"marshal/internal/diagnostics"
	"marshal/internal/filetrack"
	"marshal/internal/imagepayload"
	"marshal/internal/index"
	"marshal/internal/llm/embedding"
	"marshal/internal/llm/routing"
	"marshal/internal/pathutil"
	"marshal/internal/pubsub"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

const (
	defaultMaxOutputBytes = 200000
	defaultTestCommand    = "go test ./..."
)

// FileTracker is the session's file ledger: one row per path for reads and one
// for writes, keyed by the conversation (not by the agent). A read by any agent
// — the main agent, an agent.run child — satisfies the read-before-write guard
// for all of them, because there is one working tree and one conversation.
type FileTracker interface {
	RecordRead(path string, at time.Time, revision int64) error
	ReadState(path string) (filetrack.ReadState, error)
	RecordWrite(path string, at time.Time, actor filetrack.Actor) error
	WriteState(path string) (filetrack.WriteState, error)
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
	// BackupState owns the /undo evidence a committed write produces. It is a
	// separate owner from WorkspaceState because the two differ for exactly
	// one caller: a shared-mode subagent child works in the parent's captured
	// checkout (its own workspace root) while its rollback evidence must stay
	// in the conversation's undo slot, which is the parent's.
	//
	// Unset means "same as the workspace state", so every caller that has one
	// working tree keeps today's behavior.
	BackupState *session.State
	// AccessRequests reports that the caller has attached an access resolver
	// and a transport that can present a capability request (the TUI or ACP).
	// It gates mode.request: with it false the tool fails closed instead of
	// publishing a prompt nothing can settle. Defaults false, so a standalone
	// registration never offers escalation.
	AccessRequests bool
	DB             *db.DB
	ProjectID      int64
	FileTracker    FileTracker
	// AgentLabel names the agent this toolset belongs to ("main agent", "a
	// subagent", "the plan author", …). It is written into the file ledger with
	// each committed write so a later stale-file error can say which writer
	// invalidated the reader's view. Empty means an unnamed writer.
	AgentLabel string
	// AgentID is the stable writer identity recorded alongside AgentLabel. It
	// falls back to the label when empty.
	AgentID string
	Config  config.Config
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
	// ScratchRoots are the roots this registration treats as ordinary scratch
	// space: readable and writable without a System grant. They are where a
	// session stages build output, test fixtures and throwaway scripts, so
	// treating them as privileged space blocked ordinary work.
	//
	// Nil means the default: the operating system's temporary directory. An
	// explicit list replaces that default entirely, and an empty list grants
	// no scratch root at all — which is how a registration keeps the strict
	// "outside the workspace is outside" boundary.
	ScratchRoots []string

	// NamedRoots maps alias prefixes (e.g. "@run") to approved artifact
	// directories. Paths starting with an alias are resolved against the
	// mapped root instead of the workspace root. Used by pipeline
	// dispatches to give workers access to briefs and reports outside the
	// worktree without exposing absolute paths.
	NamedRoots map[string]string

	// PathGrants are operation-specific root grants supplied by this
	// registration. Each one names a root (and optionally the alias prefix
	// that addresses it) and says whether it may be read, written or both.
	//
	// They narrow, never widen, the legacy roots: an AdditionalRoots entry
	// or a NamedRoots alias is a read/write grant unless a grant here names
	// that same root or alias, in which case only the caller's narrower
	// grant applies. A dynamically added project reference (an active
	// worktree's project root, a pipeline dispatch's repository) is
	// read-only and never an implicit write target.
	PathGrants []pathutil.RootGrant

	// CommandAdmission admits one managed-command attempt. Required on the
	// final execution path; a nil callback fails closed.
	CommandAdmission func(context.Context, policy.CommandAttempt) (*policy.Admission, error)

	// CommandAuthorizer evaluates and approves one privileged attempt. The
	// shell.sudo tool needs it because its own combined prompt collects the
	// command consent, so the tool evaluates the bare payload, binds that
	// prompt's identity to the exact proof, and admits the attempt itself. A
	// nil authorizer fails closed.
	CommandAuthorizer CommandAuthorizer

	// SetupAuthorizer evaluates and admits a fresh worktree's setup hooks
	// (RunSetupPlan). It is the bounded actor's own authorizer — the same live
	// authority and configuration an ordinary shell.run is judged against — and
	// is deliberately separate from CommandAuthorizer, whose prompt collects
	// privileged consent and must never be borrowed to admit a hook. A nil
	// authorizer refuses every hook: setup never launches an unadmitted command.
	SetupAuthorizer *policy.Authorizer

	// WorkspaceUse acquires one shared root-use lease of the cross-process
	// workspace protocol for (root, kind), where kind is a worktree.UseKind.
	// The lease is held for exactly as long as the direct operation runs, and
	// managed commands transfer theirs to the JobManager until terminal
	// settlement. An owned-child lifecycle mutation refuses to run while any
	// shared use is live, so a registration that omits this callback cannot
	// participate in that guarantee.
	//
	// Nil is allowed only for an explicit test or non-git fixture: a production
	// git root never silently omits participation.
	WorkspaceUse func(ctx context.Context, root, kind string) (*worktree.UseLease, error)

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

	// Images is the content-addressed store image.view writes to. nil means
	// "this process serves no images": the tool is not registered at all,
	// rather than registered and failing on every call.
	Images *imagepayload.Store

	// Capture is the desktop-capture surface behind image.capture. The tool is
	// registered only when it is Enabled and Images is wired.
	Capture config.CaptureConfig

	// CaptureRunner runs the capture helper commands. nil means the
	// exec-backed runner; tests inject a fake so a capture never reaches the
	// display session this process happens to run in.
	CaptureRunner capture.Runner

	// CaptureEnv is the ambient session a capture backend detects from. The
	// zero value means runtime.GOOS and os.Getenv; tests pass it explicitly.
	CaptureEnv capture.Env
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

// CommandAuthorizer is the policy seam for a tool whose own prompt collects the
// command approval. It is the evaluator contract this package can rely on
// without depending on how the engine is built; *policy.Authorizer satisfies it.
type CommandAuthorizer interface {
	// Evaluate captures a fresh authority snapshot and evaluates the call.
	Evaluate(ctx context.Context, call policy.Call) (*policy.Authorization, policy.Evaluation, error)
	// ApproveExact binds one settled prompt's identity to the captured proof.
	ApproveExact(auth *policy.Authorization, requestID string) (*policy.Authorization, error)
	// BeginCommand revalidates the proof against live facts and takes the
	// owner's admission lease.
	BeginCommand(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error)
}

type CommandRequest struct {
	Command        string
	Dir            string
	Timeout        time.Duration
	MaxOutputBytes int
	// Environment, when non-nil, is the captured launch environment: the exact
	// "KEY=VALUE" list the child receives, including an intentionally empty
	// list. Nil keeps the legacy behavior — the runner inherits or builds its
	// own environment — which only a direct (unprepared) caller may rely on.
	Environment []string
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
	// scratchRoots are the registration's own scratch roots. nil means the
	// default (the operating system's temporary directory); an explicit list
	// replaces it, and an empty one grants no scratch root at all.
	scratchRoots []string
	// pathGrants are the registration's own root grants. They narrow
	// additionalRoots/namedRoots for the same root or alias; the primary root
	// stays readable and writable.
	pathGrants     []pathutil.RootGrant
	runner         CommandRunner
	testCommand    string
	maxOutputBytes int
	sessionState   *session.State
	workspaceState *session.State
	// backupStateOwner owns /undo evidence when it differs from the workspace
	// state (a shared-mode subagent child). nil means "use the workspace
	// state"; see backupState().
	backupStateOwner *session.State
	// accessRequests reports whether the caller promised an attached access
	// resolver/transport. Defaults false: a standalone registration (tests,
	// embedded registries) has nobody to answer a capability request, so
	// mode.request fails closed rather than publishing a prompt no transport
	// can settle.
	accessRequests bool
	db             *db.DB
	projectID      int64
	fileTracker    FileTracker
	// agentLabel names this toolset's agent in the file ledger, so a stale-file
	// error can say which writer invalidated the reader's view. Empty for a
	// registration that does not carry one.
	agentLabel string
	// agentID is the stable writer identity recorded alongside agentLabel. It
	// falls back to the label when empty.
	agentID string
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

	// commandAdmission admits one managed-command attempt at accepted launch.
	// nil fails closed: there is no unadmitted command execution path.
	commandAdmission func(context.Context, policy.CommandAttempt) (*policy.Admission, error)

	// commandAuthorizer evaluates and approves one privileged attempt, because
	// shell.sudo's combined password prompt collects the command consent. nil
	// fails closed.
	commandAuthorizer CommandAuthorizer

	// setupAuthorizer admits a fresh worktree's setup hooks. nil refuses every
	// hook rather than launching an unadmitted command.
	setupAuthorizer *policy.Authorizer

	// workspaceUse acquires the shared root-use lease a direct tool operation or
	// a managed command runs under. nil when this registration is a non-git or
	// explicit test fixture.
	workspaceUse func(ctx context.Context, root, kind string) (*worktree.UseLease, error)

	// repositoryMu guards repositories, the per-root cache of canonical
	// repository identities. An empty entry records a root that is not a git
	// checkout, so the failed lookup is paid once rather than on every call,
	// and only for repositoryMissTTL: a failure is not an identity.
	repositoryMu     sync.Mutex
	repositories     map[string]string
	repositoryMisses map[string]time.Time

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

	// images is the content-addressed image store behind image.view. nil
	// means no store was configured, and the tool is not registered.
	images *imagepayload.Store

	// capture is the desktop-capture surface behind image.capture, and
	// captureRunner/captureEnv are its injected seams.
	capture       config.CaptureConfig
	captureRunner capture.Runner
	captureEnv    capture.Env

	// captureMu guards captureBack, the backend detected on first use and
	// dropped after a failed capture so the next call re-detects.
	captureMu   sync.Mutex
	captureBack capture.Backend
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
	// The direct-operation tools hold a shared root-use lease for exactly the
	// length of one call. They are the ones that touch a checkout themselves —
	// file reads and writes (including the diagnostics a write runs), searches,
	// the git reads that bypass the command service, and the worktree creation
	// that reads the project root — so an owned-child lifecycle mutation refuses
	// to run while one is in flight, across processes as well as inside this
	// one. Managed commands are not wrapped: they transfer their own leases to
	// the command service until terminal settlement, and workspace.finish takes
	// the exclusive lease instead of a shared one.
	for i := range all {
		if directWorkspaceTool(all[i].Name) {
			all[i] = tools.holdWorkspaceUse(all[i])
		}
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
	// image.view needs somewhere to store what it produces. With no store
	// wired it would be a tool that fails on every call, so it is not
	// registered at all.
	if tools.images != nil {
		all = append(all, tools.imageViewTool())
	}
	// image.capture needs both an enabled capture surface and the image store
	// the images plan wires: without the store it could not return an image.
	if tools.capture.Enabled && tools.images != nil {
		all = append(all, tools.captureTool())
	}
	if tools.sessionState != nil {
		// transcript_read reads the live session transcript (and archived
		// generations via the db, when wired). It needs only the session
		// state to exist; the db is consulted lazily by the archived path
		// and by the "query" search that absorbed recall_history.
		all = append(all, tools.transcriptReadTool())
	}
	// agent.run is registered separately by app.StartRuntime after the policy
	// engine is constructed; the native toolset does not have access to the
	// engine until then. See internal/app/app.go.
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
// The read is also where a worktree that has been removed underneath the
// session is noticed: EnsureWorkspaceRoot returns the session to its project
// root and writes one notice, so the tool runs where the session can work
// instead of failing in the sandbox on a directory that is gone.
//
// The empty-ActiveRoot fallback is defensive: SetWorkspace coerces an
// empty ActiveRoot to ProjectRoot before storing, so the only way to
// observe ActiveRoot == "" here is if a future caller bypasses the
// setter. Falling back to the construction root is the safe choice in
// that case.
func (t *toolSet) activeRoot() string {
	if ws := t.wsState(); ws != nil {
		if w := ws.EnsureWorkspaceRoot(); w.ActiveRoot != "" {
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

// repositoryIdentifier is the git seam's repository identity: the canonical
// common git directory, which every worktree of one repository shares. It is
// asserted rather than added to worktree.GitOps so a test fake that cannot
// identify a repository simply does not participate in cross-process locking.
type repositoryIdentifier interface {
	RepositoryID(dir string) (string, error)
}

// repositoryMissTTL is how long a failed repository-identity lookup for one root
// is trusted as "this is not a git checkout". It bounds both costs: a transient
// git failure cannot be cached as an identity for the life of the process, and a
// genuinely non-git workspace pays one git subprocess per TTL rather than one
// per operation.
var repositoryMissTTL = 5 * time.Second

// repositoryFor returns the canonical repository identity of root, or "" when
// this registration cannot identify one. An empty identity means the root is
// not a git checkout (or the seam is a test fake with no repository), and the
// caller takes no cross-process lease rather than claiming one it cannot
// publish. A successful answer is cached per root: it cannot change for the life
// of a checkout, and the lookup shells out to git. A FAILED lookup is remembered
// for repositoryMissTTL only, because caching a transient failure as "" would
// claim the root is not a checkout and drop the cross-process lease for the life
// of the process.
func (t *toolSet) repositoryFor(root string) string {
	if t == nil || t.gitOps == nil {
		return ""
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	key := worktree.CanonicalPath(root)
	t.repositoryMu.Lock()
	if repository, ok := t.repositories[key]; ok {
		t.repositoryMu.Unlock()
		return repository
	}
	if at, ok := t.repositoryMisses[key]; ok && time.Since(at) < repositoryMissTTL {
		t.repositoryMu.Unlock()
		return ""
	}
	t.repositoryMu.Unlock()

	identifier, ok := t.gitOps.(repositoryIdentifier)
	if !ok {
		return ""
	}
	repository, err := identifier.RepositoryID(root)
	t.repositoryMu.Lock()
	defer t.repositoryMu.Unlock()
	if err != nil {
		if t.repositoryMisses == nil {
			t.repositoryMisses = make(map[string]time.Time)
		}
		t.repositoryMisses[key] = time.Now()
		return ""
	}
	delete(t.repositoryMisses, key)
	t.repositories[key] = repository
	return repository
}

// acquireWorkspaceUse takes one shared root-use lease for a direct operation or
// a managed command. It returns (nil, nil) when this registration does not
// participate — no callback wired, an empty root, or a root that is no git
// checkout — so callers can release unconditionally.
func (t *toolSet) acquireWorkspaceUse(ctx context.Context, root, kind string) (*worktree.UseLease, error) {
	if t == nil || t.workspaceUse == nil {
		return nil, nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	lease, err := t.workspaceUse(ctx, root, kind)
	if err != nil {
		return nil, fmt.Errorf("%s is in use: %w", root, err)
	}
	return lease, nil
}

// releaseWorkspaceUse reports terminal settlement for a direct operation's
// lease. A nil lease (a non-git root) releases nothing.
func releaseWorkspaceUse(lease *worktree.UseLease) {
	if lease != nil {
		_ = lease.Release(true)
	}
}

// releaseWorkspaceUses releases a set of leased roots in reverse acquisition
// order.
func releaseWorkspaceUses(leases []*worktree.UseLease) {
	for i := len(leases) - 1; i >= 0; i-- {
		releaseWorkspaceUse(leases[i])
	}
}

// holdRootUse holds a shared use lease for one direct operation and returns its
// release. The returned function is always safe to call.
func (t *toolSet) holdRootUse(ctx context.Context, root, kind string) (func(), error) {
	lease, err := t.acquireWorkspaceUse(ctx, root, kind)
	if err != nil {
		return nil, err
	}
	return func() { releaseWorkspaceUse(lease) }, nil
}

// directWorkspaceTool reports whether a tool is one that touches a checkout
// itself — outside the command service — and therefore holds its own shared
// root-use lease for the length of one call. Managed commands (shell.run,
// test.run) are excluded because they transfer their leases to the command
// service, and workspace.finish is excluded because it takes the exclusive
// lifecycle lease on its source and target instead of a shared one.
func directWorkspaceTool(name string) bool {
	switch name {
	case "file.read", "file.write", "file.edit", "file.write_patch",
		"repo.search", "codebase_search",
		"git.status", "git.diff", "git.log", "git.blame",
		"workspace.worktree":
		return true
	}
	return false
}

// holdWorkspaceUse wraps a direct-operation handler in a shared root-use lease
// for this registration's bound workspace, so the operation is visible to the
// cross-process protocol for exactly as long as it runs. A registration with no
// lease callback (a test or non-git fixture) is returned unchanged, and a root
// that is no git checkout acquires nothing.
func (t *toolSet) holdWorkspaceUse(tool registry.Tool) registry.Tool {
	inner := tool.Handler
	if inner == nil {
		return tool
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		release, err := t.holdRootUse(ctx, t.activeRoot(), worktree.UseKindTool)
		if err != nil {
			return registry.ToolResult{}, err
		}
		defer release()
		return inner(ctx, call)
	}
	return tool
}

// acquireCommandWorkspaceUses takes the shared leases one managed command needs
// for its captured cwd and workspace. They are acquired in canonical sorted
// order, so two callers naming the same roots in opposite orders cannot
// deadlock, and duplicates of one root (a command that runs at its workspace
// root) are acquired once.
func (t *toolSet) acquireCommandWorkspaceUses(ctx context.Context, roots ...string) ([]*worktree.UseLease, error) {
	canonical := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		canonical[worktree.CanonicalPath(root)] = struct{}{}
	}
	if len(canonical) == 0 {
		return nil, nil
	}
	ordered := make([]string, 0, len(canonical))
	for root := range canonical {
		ordered = append(ordered, root)
	}
	sort.Strings(ordered)
	leases := make([]*worktree.UseLease, 0, len(ordered))
	for _, root := range ordered {
		lease, err := t.acquireWorkspaceUse(ctx, root, worktree.UseKindCommand)
		if err != nil {
			releaseWorkspaceUses(leases)
			return nil, err
		}
		if lease != nil {
			leases = append(leases, lease)
		}
	}
	return leases, nil
}

// acquireTargetLocks takes the exclusive lifecycle lock on every root a
// mutation will rewrite or delete, in canonical sorted order, and returns their
// combined release. It is the native adapter's half of the lifecycle protocol:
// the caller must not already hold a shared lease on these roots, because the
// protocol acquires exclusivity instead of upgrading. Roots this registration
// cannot identify a repository for (a non-git fixture) are skipped.
func (t *toolSet) acquireTargetLocks(ctx context.Context, roots ...string) (func(), error) {
	canonical := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		canonical[worktree.CanonicalPath(root)] = struct{}{}
	}
	ordered := make([]string, 0, len(canonical))
	for root := range canonical {
		ordered = append(ordered, root)
	}
	sort.Strings(ordered)
	releases := make([]func(), 0, len(ordered))
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, root := range ordered {
		repository := t.repositoryFor(root)
		if repository == "" {
			continue
		}
		release, err := worktree.AcquireTargetLock(ctx, repository, root)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
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

// backupState returns the state that owns /undo evidence for a committed
// write. It falls back to the workspace state, which is every caller with one
// working tree; only a shared-mode subagent child names a different owner, so
// its edits stay visible to the conversation's /undo while its own tool
// bindings are still the child's.
func (t *toolSet) backupState() *session.State {
	if t.backupStateOwner != nil {
		return t.backupStateOwner
	}
	return t.wsState()
}

// cloneScratchRoots copies a registration's scratch roots, preserving the
// nil/non-nil distinction that separates "the temporary-directory default"
// from "no scratch root at all".
func cloneScratchRoots(roots []string) []string {
	if roots == nil {
		return nil
	}
	return append([]string{}, roots...)
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
		root:             root,
		additionalRoots:  additionalRoots,
		namedRoots:       opts.NamedRoots,
		scratchRoots:     cloneScratchRoots(opts.ScratchRoots),
		pathGrants:       opts.PathGrants,
		runner:           runner,
		testCommand:      testCommand,
		maxOutputBytes:   maxOutputBytes,
		sessionState:     opts.SessionState,
		workspaceState:   opts.WorkspaceState,
		backupStateOwner: opts.BackupState,
		accessRequests:   opts.AccessRequests,
		db:               opts.DB,
		projectID:        opts.ProjectID,
		fileTracker:      opts.FileTracker,
		agentLabel:       opts.AgentLabel,
		agentID:          opts.AgentID,
		jobManager:       jobManager,
		commandDir:       opts.CommandDir,
		diagnostics:      newDiagnosticsChecker(opts),

		commandAdmission:       opts.CommandAdmission,
		commandAuthorizer:      opts.CommandAuthorizer,
		setupAuthorizer:        opts.SetupAuthorizer,
		workspaceUse:           opts.WorkspaceUse,
		repositories:           make(map[string]string),
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
		images:   opts.Images,

		capture:       opts.Capture,
		captureRunner: opts.CaptureRunner,
		captureEnv:    opts.CaptureEnv,
	}
	if ts.gitOps == nil {
		ts.gitOps = worktree.CLIGitOps{}
	}
	// A zero capture Env means "detect against this process's session"; tests
	// pass one explicitly so a capture never reads the ambient environment.
	if ts.captureEnv.Getenv == nil {
		ts.captureEnv.Getenv = os.Getenv
	}
	if ts.captureEnv.GOOS == "" {
		ts.captureEnv.GOOS = runtime.GOOS
	}
	// nil means the exec-backed runner. DetectWithRunner never falls back on
	// its own, so a runner the caller supplied cannot be silently ignored.
	if ts.captureRunner == nil {
		ts.captureRunner = execCaptureRunner
	}
	return ts, nil
}

// actor is the identity recorded with every committed write: the stable agent
// ID when the toolset carries one, falling back to the label, plus the label
// for display in a stale-file error.
func (t *toolSet) actor() filetrack.Actor {
	id := t.agentID
	if id == "" {
		id = t.agentLabel
	}
	return filetrack.Actor{ID: id, Label: t.agentLabel}
}
