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

type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
	SessionState   *session.State
	DB             *db.DB
	ProjectID      int64
	FileTracker    FileTracker
	Config         config.Config
	JobManager     *JobManager
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
	// (*policy.PolicyEngine).GuardrailCheck in app.go. Optional; when
	// nil, no guardrail check is performed (the policy engine already
	// ran Evaluate upstream in the agent loop).
	Guardrail func(command string) error

	// LSP is an optional LSPQuerier for symbol-name-addressed definition,
	// references, and hover tools. nil when LSP is unavailable.
	LSP LSPQuerier

	// LSPSource is an optional diagnostics source consulted before the
	// configured command checkers. nil when LSP is unavailable.
	LSPSource diagnostics.LSPSource

	// LSPIndex is an optional LSP-backed symbol provider consulted during
	// repo.index. nil when LSP is unavailable.
	LSPIndex index.LSPSymbols

	// ConfigPath is the project-local config file path (.marshal/config.toml).
	// Required for config.* tools.
	ConfigPath string
	// UserConfigPath is the global config file path (~/.config/marshal/config.toml).
	// Required for config.* global-scope writes.
	UserConfigPath string
	// ConfigReloader hot-swaps the agent runtime from a new config. When nil,
	// config.* write tools are not registered. Mirrors the configReloader seam
	// wired in app.go.
	ConfigReloader func(config.Config) error
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Command        string
	Dir            string
	Timeout        time.Duration
	MaxOutputBytes int
	Stdout         io.Writer
	Stderr         io.Writer
	OnStart        func(pid int)
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
	db              *db.DB
	projectID       int64
	fileTracker     FileTracker
	jobManager      *JobManager
	diagnostics     *diagnostics.Checker
	registry        *registry.Registry

	webEnabled      bool
	webFetchTimeout time.Duration
	webSearchURL    string
	webSearchKey    string

	// Test-only hooks. nil in production.
	webHTTPClient *http.Client
	ssrfCheck     func(*url.URL) bool

	guardrail func(command string) error

	config config.Config

	// maxSearchableFileBytes caps the size of individual files that
	// repo.search will read from disk. Files larger than this threshold
	// are silently skipped. Defaults to 1 MiB (from IndexingConfig).
	maxSearchableFileBytes int64

	// resolveEmbedder returns the configured Embedder or an error. Injected so
	// tests can supply a fake; production wiring resolves via the router.
	resolveEmbedder func() (embedding.Embedder, error)

	// lsp is an optional LSPQuerier for symbol-name-addressed definition,
	// references, and hover tools. nil when LSP is unavailable.
	lsp LSPQuerier

	// lspIndex is an optional LSP-backed symbol provider consulted during
	// repo.index. nil when LSP is unavailable.
	lspIndex index.LSPSymbols

	configPath     string
	userConfigPath string
	configReloader func(config.Config) error
}

func RegisterAll(reg *registry.Registry, opts Options) error {
	tools, err := newToolSet(opts)
	if err != nil {
		return err
	}
	tools.registry = reg

	all := []registry.Tool{
		tools.fileReadTool(),
		tools.filePageTool(),
		tools.fileWritePatchTool(),
		tools.fileWriteTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
		tools.repoIndexTool(),
		tools.repoMapTool(),
		tools.repoCardTool(),
		tools.symbolsFindTool(),
		tools.todoWriteTool(),
		tools.jobOutputTool(),
		tools.jobKillTool(),
		tools.jobListTool(),
		tools.questionAskTool(),
		tools.askUserTool(),
		tools.modeRequestTool(),
		tools.diagnosticsCheckTool(),
		tools.toolsSelectTool(),
		tools.codebaseSearchTool(),
		tools.jsonQueryTool(),
		tools.csvInspectTool(),
		tools.referencesTool(),
		tools.definitionTool(),
		tools.hoverTool(),
		tools.workspaceWorktreeTool(),
	}
	if tools.sessionState != nil {
		all = append(all,
			tools.scratchpadWriteTool(),
			tools.scratchpadReadTool(),
			tools.scratchpadListTool(),
			tools.scratchpadDeleteTool(),
		)
	}
	if recallToolEnabled(tools.config.Session.Rollover) {
		all = append(all, tools.recallHistoryTool())
	}
	// agent.run is registered separately by app.Run after the policy engine
	// is constructed; the native toolset does not have access to the engine
	// until then. See internal/app/app.go.
	if tools.configReloader != nil {
		all = append(all, tools.configTools()...)
	}
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
	if t.sessionState != nil {
		if ws := t.sessionState.Workspace(); ws.ActiveRoot != "" {
			return ws.ActiveRoot
		}
	}
	return t.root
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

	jobManager := opts.JobManager
	if jobManager == nil {
		maxBg := opts.Config.Tools.Shell.MaxBackgroundJobs
		retention := opts.Config.Tools.Shell.BackgroundRetention
		if maxBg <= 0 {
			maxBg = 25
		}
		if retention <= 0 {
			retention = 8 * time.Hour
		}
		jobManager = NewJobManager(context.Background(), runner, root, maxBg, retention, maxOutputBytes)
	}
	if opts.SessionState != nil {
		jobManager.SetOnChange(opts.SessionState.SetRunningJobsCount)
	}
	if opts.JobBroker != nil {
		jobManager.SetBroker(opts.JobBroker)
	}

	return &toolSet{
		root:            root,
		additionalRoots: additionalRoots,
		namedRoots:      opts.NamedRoots,
		runner:          runner,
		testCommand:     testCommand,
		maxOutputBytes:  maxOutputBytes,
		sessionState:    opts.SessionState,
		db:              opts.DB,
		projectID:       opts.ProjectID,
		fileTracker:     opts.FileTracker,
		jobManager:      jobManager,
		diagnostics:     newDiagnosticsChecker(opts),

		guardrail:              opts.Guardrail,
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
		lsp:            opts.LSP,
		lspIndex:       opts.LSPIndex,
		configPath:     opts.ConfigPath,
		userConfigPath: opts.UserConfigPath,
		configReloader: opts.ConfigReloader,
	}, nil
}
