package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeSourceGitHubShorthand(t *testing.T) {
	url, name, err := NormalizeSource("github:acme/widgets")
	if err != nil {
		t.Fatalf("NormalizeSource: %v", err)
	}
	if url != "https://github.com/acme/widgets.git" {
		t.Fatalf("url = %q", url)
	}
	if name != "widgets" {
		t.Fatalf("name = %q, want widgets", name)
	}
}

func TestNormalizeSourceGitHubShorthandInvalid(t *testing.T) {
	for _, src := range []string{"github:", "github:onlyone", "github:a/b/c", "github:/b"} {
		if _, _, err := NormalizeSource(src); err == nil {
			t.Fatalf("NormalizeSource(%q) should error", src)
		}
	}
}

func TestNormalizeSourceFullURL(t *testing.T) {
	url, name, err := NormalizeSource("https://example.com/acme/tools.git")
	if err != nil {
		t.Fatalf("NormalizeSource: %v", err)
	}
	if url != "https://example.com/acme/tools.git" {
		t.Fatalf("url = %q", url)
	}
	if name != "tools" {
		t.Fatalf("name = %q, want tools", name)
	}
}

func TestNormalizeSourceLocalPath(t *testing.T) {
	_, name, err := NormalizeSource("/tmp/some-plugin")
	if err != nil {
		t.Fatalf("NormalizeSource: %v", err)
	}
	if name != "some-plugin" {
		t.Fatalf("name = %q, want some-plugin", name)
	}
}

func TestSplitSourceRef(t *testing.T) {
	source, ref, err := SplitSourceRef("github:acme/widgets@v2")
	if err != nil {
		t.Fatalf("SplitSourceRef: %v", err)
	}
	if source != "github:acme/widgets" || ref != "v2" {
		t.Fatalf("got (%q, %q), want (github:acme/widgets, v2)", source, ref)
	}
}

func TestSplitSourceRefNoRef(t *testing.T) {
	source, ref, err := SplitSourceRef("github:acme/widgets")
	if err != nil {
		t.Fatalf("SplitSourceRef: %v", err)
	}
	if source != "github:acme/widgets" || ref != "" {
		t.Fatalf("got (%q, %q)", source, ref)
	}
}

func TestSplitSourceRefURLPassesThrough(t *testing.T) {
	source, ref, err := SplitSourceRef("git@github.com:acme/widgets.git")
	if err != nil {
		t.Fatalf("SplitSourceRef: %v", err)
	}
	if source != "git@github.com:acme/widgets.git" || ref != "" {
		t.Fatalf("got (%q, %q), want pass-through", source, ref)
	}
}

func TestSplitSourceRefEmptyRef(t *testing.T) {
	if _, _, err := SplitSourceRef("github:acme/widgets@"); err == nil {
		t.Fatal("SplitSourceRef should error on a trailing @")
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"widgets", "my-plugin", "plugin_2"}
	for _, n := range valid {
		if !ValidName(n) {
			t.Fatalf("ValidName(%q) = false, want true", n)
		}
	}
	invalid := []string{"", ".", "..", "a/b", `a\b`}
	for _, n := range invalid {
		if ValidName(n) {
			t.Fatalf("ValidName(%q) = true, want false", n)
		}
	}
}

func TestValidateCloneSourceFileOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	// file:// pointing outside workspace should be rejected
	err := ValidateCloneSource("file:///etc/passwd", ws)
	if err == nil {
		t.Fatal("ValidateCloneSource should reject file:// outside workspace")
	}
}

func TestValidateCloneSourceFileInsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	// Create a subdirectory inside workspace
	inside := filepath.Join(ws, "local-repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	// file:// pointing inside workspace should be accepted
	err := ValidateCloneSource("file://"+inside, ws)
	if err != nil {
		t.Fatalf("ValidateCloneSource should accept file:// inside workspace: %v", err)
	}
}

func TestValidateCloneSourceNonFileAlwaysAllowed(t *testing.T) {
	ws := t.TempDir()
	// Non-file:// sources should always pass (git clone handles them)
	for _, src := range []string{
		"https://github.com/acme/widgets.git",
		"git@github.com:acme/widgets.git",
		"github:acme/widgets",
		"ssh://git@github.com/acme/widgets.git",
	} {
		if err := ValidateCloneSource(src, ws); err != nil {
			t.Errorf("ValidateCloneSource(%q) should pass for non-local source: %v", src, err)
		}
	}
}

func TestValidateCloneSourceRejectsNonLocalFileHost(t *testing.T) {
	ws := t.TempDir()
	// A file:// URL with a non-empty, non-localhost host is a remote file
	// share and must be rejected regardless of the path.
	for _, src := range []string{
		"file://attacker-host/etc/passwd",
		"file://evil.example.com/workspace/repo",
	} {
		if err := ValidateCloneSource(src, ws); err == nil {
			t.Errorf("ValidateCloneSource(%q) should reject a non-local file host", src)
		}
	}
	// localhost is a legitimate local file URL and should pass containment.
	inside := filepath.Join(ws, "local-repo")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCloneSource("file://localhost"+inside, ws); err != nil {
		t.Errorf("ValidateCloneSource(file://localhost...) should accept an inside path: %v", err)
	}
}

func TestValidateCloneSourceRejectsSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	// A symlink inside the workspace pointing outside must be rejected even
	// though the lexical path is contained. Plain local paths are a
	// supported interactive install source and are not validated, so only
	// the file:// form is asserted here.
	outside := t.TempDir()
	link := filepath.Join(ws, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := ValidateCloneSource("file://"+link, ws); err == nil {
		t.Error("ValidateCloneSource should reject a file:// symlink pointing outside")
	}
}

func TestScopePaths(t *testing.T) {
	if got := GlobalStoreDir("/home/u"); got != filepath.Join("/home/u", ".config", "marshal", "plugins") {
		t.Fatalf("GlobalStoreDir = %q", got)
	}
	if got := GlobalLockPath("/home/u"); got != filepath.Join("/home/u", ".config", "marshal", "plugins-lock.json") {
		t.Fatalf("GlobalLockPath = %q", got)
	}
	if got := ProjectStoreDir("/work"); got != filepath.Join("/work", ".marshal", "plugins") {
		t.Fatalf("ProjectStoreDir = %q", got)
	}
	if got := ProjectLockPath("/work"); got != filepath.Join("/work", ".marshal", "plugins-lock.json") {
		t.Fatalf("ProjectLockPath = %q", got)
	}
}
