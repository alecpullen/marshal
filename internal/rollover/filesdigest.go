package rollover

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errNoGit signals that the workspace is not a git repository (or git is
// unavailable). It is not a fatal error: FilesDigestProvider degrades to a
// files-only digest in that case. A different error (git present but the
// command failed) is fatal and returned from Digest.
var errNoGit = errors.New("not a git repository")

// fileStateSource is the seam FilesDigestProvider reads workspace state
// through. The production implementation (FilesState) queries the session's
// file_reads / file_writes tables and runs git/grep via a CommandRunner;
// tests substitute a fake.
type fileStateSource interface {
	// WrittenFiles returns paths recorded as written this session, in
	// insertion order (most-recent last). An empty slice is valid.
	WrittenFiles() ([]string, error)
	// ReadFiles returns paths recorded as read this session.
	ReadFiles() ([]string, error)
	// GitStatusShort returns `git status --short` stdout. It returns
	// errNoGit when the workspace is not a git repo; any other error is
	// fatal.
	GitStatusShort(ctx context.Context) (string, error)
	// OutstandingTodos returns grep output for TODO/FIXME/XXX markers in
	// tracked source files, or "" when unavailable. Errors are treated
	// as "no todos" (non-fatal).
	OutstandingTodos(ctx context.Context) (string, error)
}

// FilesDigestProvider produces a structured resume digest from the session's
// on-disk file-tracking state and a read-only scan of the working tree,
// without an LLM call. On a non-fatal gap (no git, no todos) it degrades to a
// files-only digest; on a fatal git error it returns an error so the
// controller falls back to the minimal digest.
type FilesDigestProvider struct {
	state fileStateSource
}

// NewFilesDigestProvider constructs a provider backed by the given state
// source.
func NewFilesDigestProvider(state fileStateSource) *FilesDigestProvider {
	return &FilesDigestProvider{state: state}
}

// Name returns the provider label used in verbose logging and digest_source.
func (p *FilesDigestProvider) Name() string { return "files" }

// Digest assembles the structured resume digest. The digest always names the
// generation and lists written/read files; git status and TODO markers are
// included when available. A non-git workspace degrades gracefully; a
// present-but-failing git command fails the whole digest (returned as an
// error) so the controller's minimal fallback takes over rather than
// silently claiming "no changes."
func (p *FilesDigestProvider) Digest(ctx context.Context, h GenerationHandle) (string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Generation %d — resuming from structured on-disk state.\n\n", h.Seq)

	written, werr := p.state.WrittenFiles()
	read, rerr := p.state.ReadFiles()
	if werr != nil {
		return "", "", fmt.Errorf("files digest: written files: %w", werr)
	}
	if rerr != nil {
		return "", "", fmt.Errorf("files digest: read files: %w", rerr)
	}

	b.WriteString("## Files written this session\n")
	if len(written) == 0 {
		b.WriteString("(none)\n")
	} else {
		for _, p := range written {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}

	b.WriteString("\n## Files read this session\n")
	if len(read) == 0 {
		b.WriteString("(none)\n")
	} else {
		// Cap the read list; it can be long and the digest is meant to be short.
		shown := read
		if len(shown) > 20 {
			shown = shown[:20]
		}
		for _, p := range shown {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		if len(read) > 20 {
			fmt.Fprintf(&b, "- ...and %d more\n", len(read)-20)
		}
	}

	status, serr := p.state.GitStatusShort(ctx)
	if serr != nil && !errors.Is(serr, errNoGit) {
		// git is present but the command failed — state is unknown.
		return "", "", fmt.Errorf("files digest: git status: %w", serr)
	}
	if serr == nil {
		b.WriteString("\n## Working tree (git status --short)\n")
		if strings.TrimSpace(status) == "" {
			b.WriteString("clean\n")
		} else {
			b.WriteString(status)
			if !strings.HasSuffix(status, "\n") {
				b.WriteString("\n")
			}
		}
	}

	todos, terr := p.state.OutstandingTodos(ctx)
	if terr == nil && strings.TrimSpace(todos) != "" {
		b.WriteString("\n## Outstanding TODO/FIXME/XXX markers\n")
		b.WriteString(todos)
		if !strings.HasSuffix(todos, "\n") {
			b.WriteString("\n")
		}
	}

	b.WriteString("\nContinue the task from the above. Re-read any file you need; the full transcript is archived (marshal history).\n")
	return b.String(), SourceStructured, nil
}
