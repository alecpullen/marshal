package plugins

import (
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
