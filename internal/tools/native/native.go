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

	// Guardrail is invoked by shell.run / test.run after policy
	// evaluation, as a final pre-flight check. Returning a non-nil
	// error aborts the command with a tool error. Typically wired to
	// (*policy.PolicyEngine).GuardrailCheck in app.go. Optional; when
	// nil, no guardrail check is performed (the policy engine already
	// ran Evaluate upstream in the agent loop).
	Guardrail func(command string) error
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
		tools.diagnosticsCheckTool(),
		tools.toolsSelectTool(),
	}
	if recallToolEnabled(tools.config.Session.Rollover) {
		all = append(all, tools.recallHistoryTool())
	}
	// agent.run is registered separately by app.Run after the policy engine
	// is constructed; the native toolset does not have access to the engine
	// until then. See internal/app/app.go.
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
		runner:          runner,
		testCommand:     testCommand,
		maxOutputBytes:  maxOutputBytes,
		sessionState:    opts.SessionState,
		db:              opts.DB,
		projectID:       opts.ProjectID,
		fileTracker:     opts.FileTracker,
		jobManager:      jobManager,
		diagnostics:     diagnostics.NewChecker(opts.Config.Diagnostics.Commands),

		guardrail:              opts.Guardrail,
		webEnabled:             opts.Config.Web.Enabled,
		webFetchTimeout:        opts.Config.Web.FetchTimeout,
		webSearchURL:           opts.Config.Web.SearchURL,
		webSearchKey:           opts.Config.Web.SearchKey,
		ssrfCheck:              isPrivateURL,
		config:                 opts.Config,
		maxSearchableFileBytes: opts.Config.Indexing.MaxSearchableFileBytes,
	}, nil
}
