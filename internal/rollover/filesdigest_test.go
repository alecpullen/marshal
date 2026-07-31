package rollover

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeFileState implements fileStateSource for testing.
type fakeFileState struct {
	written   []string
	read      []string
	status    string
	statusOK  bool
	todos     string
	todoOK    bool
	statusErr error
	todoErr   error
}

func (f *fakeFileState) WrittenFiles() ([]string, error)            { return f.written, nil }
func (f *fakeFileState) ReadFiles() ([]string, error)               { return f.read, nil }
func (f *fakeFileState) GitStatusShort(ctx context.Context) (string, error) {
	if f.statusErr != nil {
		return "", f.statusErr
	}
	if !f.statusOK {
		return "", errNoGit
	}
	return f.status, nil
}
func (f *fakeFileState) OutstandingTodos(ctx context.Context) (string, error) {
	if f.todoErr != nil {
		return "", f.todoErr
	}
	if !f.todoOK {
		return "", nil
	}
	return f.todos, nil
}

func TestFilesDigestProvider_DigestContainsWrittenFiles(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written:  []string{"internal/foo/bar.go", "cmd/marshal/main.go"},
		read:     []string{"internal/baz/qux.go"},
		status:   " M internal/foo/bar.go\n?? cmd/marshal/main.go",
		statusOK: true,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{
		SessionID: "sess-1", GenerationID: "gen-1", Seq: 2,
	})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q", source, SourceStructured)
	}
	for _, want := range []string{"internal/foo/bar.go", "cmd/marshal/main.go", "Generation 2"} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest missing %q\ngot:\n%s", want, digest)
		}
	}
}

func TestFilesDigestProvider_DigestIncludesTodos(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"},
		status:  " M a.go", statusOK: true,
		todos: "a.go:10: TODO refactor this\nb.go:5: FIXME handle nil", todoOK: true,
	}}
	digest, _, err := p.Digest(context.Background(), GenerationHandle{Seq: 0})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if !strings.Contains(digest, "TODO refactor this") || !strings.Contains(digest, "FIXME handle nil") {
		t.Errorf("digest missing TODO/FIXME lines\ngot:\n%s", digest)
	}
}

func TestFilesDigestProvider_NoGitDegradedToFilesOnly(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"}, statusOK: false, todoOK: false,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{Seq: 1})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q (degraded still structured)", source, SourceStructured)
	}
	if strings.Contains(digest, "git status") {
		t.Errorf("degraded digest should not mention git status\ngot:\n%s", digest)
	}
	if !strings.Contains(digest, "a.go") {
		t.Errorf("degraded digest missing written file\ngot:\n%s", digest)
	}
}

func TestFilesDigestProvider_GitErrorFailsDigest(t *testing.T) {
	// A real git error (not "no git") should fail the provider so the
	// controller falls back to the minimal digest, rather than silently
	// producing a digest that claims no changes when the workspace state
	// is actually unknown.
	p := &FilesDigestProvider{state: &fakeFileState{
		written: []string{"a.go"}, statusErr: errors.New("git crashed"),
	}}
	_, _, err := p.Digest(context.Background(), GenerationHandle{Seq: 0})
	if err == nil {
		t.Fatal("expected error when git status fails, got nil")
	}
}

func TestFilesDigestProvider_EmptyStateProducesMinimalStructured(t *testing.T) {
	p := &FilesDigestProvider{state: &fakeFileState{
		status: "", statusOK: true, todoOK: true,
	}}
	digest, source, err := p.Digest(context.Background(), GenerationHandle{Seq: 3})
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if source != SourceStructured {
		t.Errorf("source = %q, want %q", source, SourceStructured)
	}
	if !strings.Contains(digest, "Generation 3") {
		t.Errorf("empty-state digest missing generation header\ngot:\n%s", digest)
	}
}
