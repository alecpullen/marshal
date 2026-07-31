package rollover

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CommandRunner is the local, narrow interface FilesState needs to run
// read-only git commands. It deliberately uses plain Go types (not
// native.CommandRequest/Result) so this package does not import
// internal/tools/native, which would create an import cycle (native's
// tests import internal/agent, and internal/agent/handoff.go imports
// internal/rollover). Callers adapt their native.CommandRunner to this
// interface — see internal/app/app.go.
type CommandRequest struct {
	Command string
	Dir     string
	Timeout time.Duration
}

type CommandResult struct {
	Stdout   string
	ExitCode int
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

// FilesState is the production fileStateSource: it reads the session's
// file_reads/file_writes tables and runs read-only git/grep commands via a
// CommandRunner. The CommandRunner is the same one the native tool set uses
// (sandboxed or not), so this provider inherits the workspace's command
// policy without re-implementing it.
//
// The command-runner interface is deliberately local to this package (plain
// Go types, not native.CommandRequest/Result) so internal/rollover does not
// import internal/tools/native. That avoids an import cycle: native's tests
// import internal/agent, and internal/agent/handoff.go imports
// internal/rollover. Callers adapt their native.CommandRunner to this
// interface — see internal/app/app.go.
type FilesState struct {
	db        *sql.DB
	sessionID string
	runner    CommandRunner
	root      string
}

// NewFilesState constructs a FilesState for the given session and workspace.
func NewFilesState(db *sql.DB, sessionID string, runner CommandRunner, root string) *FilesState {
	return &FilesState{db: db, sessionID: sessionID, runner: runner, root: root}
}

// WrittenFiles returns paths written this session, in insertion order.
func (f *FilesState) WrittenFiles() ([]string, error) {
	return f.queryPaths(`SELECT path FROM file_writes WHERE session_id = ? ORDER BY written_at`)
}

// ReadFiles returns paths read this session, in insertion order.
func (f *FilesState) ReadFiles() ([]string, error) {
	return f.queryPaths(`SELECT path FROM file_reads WHERE session_id = ? ORDER BY read_at`)
}

func (f *FilesState) queryPaths(query string) ([]string, error) {
	rows, err := f.db.Query(query, f.sessionID)
	if err != nil {
		return nil, fmt.Errorf("query paths: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GitStatusShort runs `git status --short` and returns stdout. A non-zero
// exit (typically 128, "not a git repository") is mapped to errNoGit so the
// provider degrades to a files-only digest; any other failure is a real
// error.
func (f *FilesState) GitStatusShort(ctx context.Context) (string, error) {
	res, err := f.runner.Run(ctx, CommandRequest{
		Command: "git status --short",
		Dir:     f.root,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		// 128 is git's "fatal: not a git repository". Treat any non-zero
		// as "no git available" rather than guessing exit codes.
		return "", errNoGit
	}
	return res.Stdout, nil
}

// OutstandingTodos runs a scoped `git grep` for TODO/FIXME/XXX markers. It is
// best-effort: any error (including no-git) returns "" so the digest simply
// omits the section, but context cancellation/deadline is propagated so the
// caller does not block on a cancelled turn. Tracked Go files are the default
// scope.
func (f *FilesState) OutstandingTodos(ctx context.Context) (string, error) {
	res, err := f.runner.Run(ctx, CommandRequest{
		Command: "git grep -nE 'TODO|FIXME|XXX' -- ':*.go'",
		Dir:     f.root,
		Timeout: 30 * time.Second,
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil || res.ExitCode != 0 {
		return "", nil
	}
	return strings.TrimSpace(res.Stdout), nil
}
