package native

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/diagnostics"
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
	ListReadFiles() ([]string, error)
	ListWrittenFiles() ([]string, error)
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
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Command string
	Dir     string
	Timeout time.Duration
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
	root           string
	runner         CommandRunner
	testCommand    string
	maxOutputBytes int
	sessionState   any
	db             *db.DB
	projectID      int64
	fileTracker    FileTracker
	jobManager     *JobManager
	diagnostics    *diagnostics.Checker
	registry       *registry.Registry

	webEnabled      bool
	webFetchTimeout time.Duration
	webSearchURL    string
	webSearchKey    string

	// Test-only hooks. nil in production.
	webHTTPClient *http.Client
	ssrfCheck     func(*url.URL) bool
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
		jobManager = NewJobManager(execRunner{}, root, maxBg, retention)
	}
	if opts.SessionState != nil {
		if counter, ok := any(opts.SessionState).(interface{ SetRunningJobsCount(int) }); ok {
			jobManager.SetOnChange(counter.SetRunningJobsCount)
		}
	}

	return &toolSet{
		root:           root,
		runner:         runner,
		testCommand:    testCommand,
		maxOutputBytes: maxOutputBytes,
		sessionState:   opts.SessionState,
		db:             opts.DB,
		projectID:      opts.ProjectID,
		fileTracker:    opts.FileTracker,
		jobManager:     jobManager,
		diagnostics:    diagnostics.NewChecker(opts.Config.Diagnostics.Commands),

		webEnabled:      opts.Config.Web.Enabled,
		webFetchTimeout: opts.Config.Web.FetchTimeout,
		webSearchURL:    opts.Config.Web.SearchURL,
		webSearchKey:    opts.Config.Web.SearchKey,
		ssrfCheck:       isPrivateURL,
	}, nil
}
