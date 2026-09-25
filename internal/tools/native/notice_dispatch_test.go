package native

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

// newDispatchRegistry builds a registry over a fresh temp workspace using the
// real registration path, so tests can exercise Registry.Dispatch end to end.
func newDispatchRegistry(t *testing.T, root string, opts Options) *registry.Registry {
	t.Helper()
	opts.WorkspaceRoot = root
	if opts.CommandRunner == nil {
		opts.CommandRunner = &fakeRunner{}
	}
	reg := registry.New()
	if err := RegisterAll(reg, opts); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg
}

// TestNoticePropagatesThroughDispatch proves that a notice produced inside a
// handler survives the authoritative Registry.Dispatch path (Lookup +
// ValidateArgs + Handler), not just a direct handler call. It uses the
// zero-match coach: an auto-mode regex-shaped query that matches nothing
// yields NoticeZeroMatchCoach.
func TestNoticePropagatesThroughDispatch(t *testing.T) {
	root := t.TempDir()
	// No line matches either alternative of the regex-shaped query.
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nbeta\ngamma\n")

	reg := newDispatchRegistry(t, root, Options{})

	res, err := reg.Dispatch(context.Background(), registry.ToolCall{
		Name: "repo.search",
		Args: []byte(`{"query":"foo|bar"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Notice == nil {
		t.Fatalf("Notice = nil, want non-nil zero-match coach; content=%q", res.Content)
	}
	if res.Notice.Kind != registry.NoticeZeroMatchCoach {
		t.Fatalf("Notice.Kind = %q, want %q", res.Notice.Kind, registry.NoticeZeroMatchCoach)
	}
	if res.Notice.Text == "" {
		t.Fatalf("Notice.Text is empty")
	}
	if !strings.Contains(res.Content, res.Notice.Text) {
		t.Fatalf("Notice.Text %q does not appear in Content %q", res.Notice.Text, res.Content)
	}
	// Auto mode compiles the pattern-shaped query as a regex.
	if got := res.Notice.Data["mode"]; got != "regex" {
		t.Fatalf("Notice.Data[mode] = %v, want %q", got, "regex")
	}
}

// TestNoticeCappedPropagatesThroughDispatch proves the capped-results notice
// also survives Dispatch.
func TestNoticeCappedPropagatesThroughDispatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle 1\nneedle 2\nneedle 3\nneedle 4\n")

	old := hardSearchMaxResults
	hardSearchMaxResults = 2
	defer func() { hardSearchMaxResults = old }()

	reg := newDispatchRegistry(t, root, Options{})

	res, err := reg.Dispatch(context.Background(), registry.ToolCall{
		Name: "repo.search",
		Args: []byte(`{"query":"needle"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Notice == nil {
		t.Fatalf("Notice = nil, want capped-results notice; content=%q", res.Content)
	}
	if res.Notice.Kind != registry.NoticeCappedResults {
		t.Fatalf("Notice.Kind = %q, want %q", res.Notice.Kind, registry.NoticeCappedResults)
	}
	if res.Notice.Text == "" {
		t.Fatalf("Notice.Text is empty")
	}
	if !strings.Contains(res.Content, res.Notice.Text) {
		t.Fatalf("Notice.Text %q does not appear in Content %q", res.Notice.Text, res.Content)
	}
}

// TestNoticeNilOnCleanDispatch guards against notices being attached
// unconditionally: a normal successful search with matches, no cap, and no
// fallback must carry Notice == nil.
func TestNoticeNilOnCleanDispatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nneedle here\n")

	reg := newDispatchRegistry(t, root, Options{})

	res, err := reg.Dispatch(context.Background(), registry.ToolCall{
		Name: "repo.search",
		Args: []byte(`{"query":"needle"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if !strings.Contains(res.Content, "a.txt:2:needle here") {
		t.Fatalf("Content missing substring match:\n%s", res.Content)
	}
	if res.Notice != nil {
		t.Fatalf("Notice = %+v, want nil on a clean dispatch", res.Notice)
	}
}

// TestNoticeOversizeFallbackPropagatesThroughDispatch proves a second notice
// kind (registry.NoticeOversizeFallback) survives Dispatch via file.read on a
// file exceeding the configured MaxOutputBytes.
func TestNoticeOversizeFallbackPropagatesThroughDispatch(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("line padding padding padding\n")
	}
	writeFile(t, filepath.Join(root, "big.txt"), sb.String())

	reg := newDispatchRegistry(t, root, Options{MaxOutputBytes: 1024})

	res, err := reg.Dispatch(context.Background(), registry.ToolCall{
		Name: "file.read",
		Args: []byte(`{"path":"big.txt"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Notice == nil {
		t.Fatalf("Notice = nil, want oversized fallback notice; content=%q", res.Content)
	}
	if res.Notice.Kind != registry.NoticeOversizeFallback {
		t.Fatalf("Notice.Kind = %q, want %q", res.Notice.Kind, registry.NoticeOversizeFallback)
	}
	if res.Notice.Text == "" {
		t.Fatalf("Notice.Text is empty")
	}
	if !strings.Contains(res.Content, res.Notice.Text) {
		t.Fatalf("Notice.Text %q does not appear in Content %q", res.Notice.Text, res.Content)
	}
}
