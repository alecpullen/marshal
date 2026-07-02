package native

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

const (
	defaultMaxOutputBytes = 200000
	defaultTestCommand    = "go test ./..."
)

type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
	SessionState   *session.State
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
}

type toolSet struct {
	root           string
	runner         CommandRunner
	testCommand    string
	maxOutputBytes int
	sessionState   *session.State
}

func RegisterAll(reg *registry.Registry, opts Options) error {
	tools, err := newToolSet(opts)
	if err != nil {
		return err
	}

	for _, tool := range []registry.Tool{
		tools.fileReadTool(),
		tools.fileWritePatchTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
	} {
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

	return &toolSet{
		root:           root,
		runner:         runner,
		testCommand:    testCommand,
		maxOutputBytes: maxOutputBytes,
		sessionState:   opts.SessionState,
	}, nil
}
