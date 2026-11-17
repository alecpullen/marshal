package desktop

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestURLAllowedEmptyLists(t *testing.T) {
	if err := urlAllowed("https://anything.com", nil, nil, ""); err != nil {
		t.Fatalf("empty lists should allow: %v", err)
	}
}

func TestURLAllowedDenylistBlocks(t *testing.T) {
	err := urlAllowed("https://evil.com/admin", nil, []string{"evil.com"}, "")
	if err == nil {
		t.Fatal("denylist should block evil.com")
	}
	if !strings.Contains(err.Error(), "evil.com") {
		t.Fatalf("error should mention url, got: %v", err)
	}
}

func TestURLAllowedDenylistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/safe", nil, []string{"example.com/admin"}, ""); err != nil {
		t.Fatalf("denylist prefix should not block /safe: %v", err)
	}
	if err := urlAllowed("https://example.com/admin/users", nil, []string{"example.com/admin"}, ""); err == nil {
		t.Fatal("denylist prefix should block /admin/users")
	}
}

func TestURLAllowedAllowlistPermits(t *testing.T) {
	if err := urlAllowed("https://example.com/page", []string{"example.com"}, nil, ""); err != nil {
		t.Fatalf("allowlist should permit example.com: %v", err)
	}
}

func TestURLAllowedAllowlistBlocksUnlisted(t *testing.T) {
	if err := urlAllowed("https://other.com", []string{"example.com"}, nil, ""); err == nil {
		t.Fatal("allowlist should block unlisted host")
	}
}

func TestURLAllowedAllowlistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/docs/page", []string{"example.com/docs"}, nil, ""); err != nil {
		t.Fatalf("allowlist prefix should permit /docs/page: %v", err)
	}
	if err := urlAllowed("https://example.com/blog", []string{"example.com/docs"}, nil, ""); err == nil {
		t.Fatal("allowlist prefix should block /blog")
	}
}

func TestURLAllowedDenylistWinsOverAllowlist(t *testing.T) {
	if err := urlAllowed("https://example.com/admin", []string{"example.com"}, []string{"example.com/admin"}, ""); err == nil {
		t.Fatal("denylist should win over allowlist")
	}
}

func TestURLAllowedRejectsDangerousSchemes(t *testing.T) {
	// javascript://example.com/alert(1) parses with a non-empty host, so
	// it would pass the host check if no scheme allowlist is set — this is
	// the real bypass the scheme check closes. The opaque forms (no host)
	// are already blocked by the host check but are included for coverage.
	dangerous := []string{
		"javascript://example.com/alert(1)",
		"data://example.com/text/html",
		"vbscript://example.com/msgbox",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox",
	}
	for _, u := range dangerous {
		if err := urlAllowed(u, nil, nil, ""); err == nil {
			t.Errorf("urlAllowed(%q) should be blocked, got nil", u)
		}
	}
}

func TestURLAllowedPermitsSafeSchemes(t *testing.T) {
	safe := []string{
		"https://example.com",
		"http://example.com",
	}
	for _, u := range safe {
		if err := urlAllowed(u, nil, nil, ""); err != nil {
			t.Errorf("urlAllowed(%q) should be allowed, got: %v", u, err)
		}
	}
}

func TestURLAllowedInvalidURL(t *testing.T) {
	if err := urlAllowed("not a url", nil, nil, ""); err == nil {
		t.Fatal("invalid URL should be blocked")
	}
}

func TestURLAllowedFileURL(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create workspace root: %v", err)
	}
	inside := filepath.Join(root, "page.html")
	if err := os.WriteFile(inside, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	outside := filepath.Join(base, "outside.html")
	if err := os.WriteFile(outside, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "link.html")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	scratch := filepath.Join(root, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatalf("create scratch dir: %v", err)
	}
	scratchFile := filepath.Join(scratch, "x.html")
	if err := os.WriteFile(scratchFile, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	otherFile := filepath.Join(root, "other.html")
	if err := os.WriteFile(otherFile, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write other file: %v", err)
	}

	fileURL := func(path string) string {
		return (&url.URL{Scheme: "file", Path: path}).String()
	}

	t.Run("inside the workspace is allowed", func(t *testing.T) {
		if err := urlAllowed(fileURL(inside), nil, nil, root); err != nil {
			t.Fatalf("workspace file should be allowed: %v", err)
		}
	})

	t.Run("parent escape is blocked", func(t *testing.T) {
		if err := urlAllowed(fileURL(filepath.Join(root, "..", "outside.html")), nil, nil, root); err == nil {
			t.Fatal(".. escape should be blocked")
		}
	})

	t.Run("symlink escape is blocked", func(t *testing.T) {
		if err := urlAllowed(fileURL(link), nil, nil, root); err == nil {
			t.Fatal("symlink pointing outside the workspace should be blocked")
		}
	})

	t.Run("remote host is blocked", func(t *testing.T) {
		if err := urlAllowed("file://host"+inside, nil, nil, root); err == nil {
			t.Fatal("file URL with a host should be blocked")
		}
	})

	t.Run("allowlist does not gate workspace files", func(t *testing.T) {
		if err := urlAllowed(fileURL(inside), []string{"example.com"}, nil, root); err != nil {
			t.Fatalf("allowlist must not deny a workspace file: %v", err)
		}
	})

	t.Run("denylist blocks the resolved path", func(t *testing.T) {
		// The denylist is matched against the resolved absolute path, so
		// the entry is that path rather than a relative pattern.
		resolved, err := filepath.EvalSymlinks(inside)
		if err != nil {
			t.Fatalf("resolve workspace file: %v", err)
		}
		if err := urlAllowed(fileURL(inside), nil, []string{resolved}, root); err == nil {
			t.Fatal("denylist should block the resolved path")
		}
	})

	t.Run("denylist matches a workspace-relative entry", func(t *testing.T) {
		// A user writes the workspace-relative form; it must block the file it
		// names, as the analogous http prefix would.
		if err := urlAllowed(fileURL(scratchFile), nil, []string{"scratch/**"}, root); err == nil {
			t.Fatal("a workspace-relative denylist entry should block scratch/x.html")
		}
	})

	t.Run("workspace-relative denylist spares a file outside it", func(t *testing.T) {
		if err := urlAllowed(fileURL(otherFile), nil, []string{"scratch/**"}, root); err != nil {
			t.Fatalf("scratch/** must not block a file outside scratch/: %v", err)
		}
	})

	t.Run("percent-encoded traversal is blocked", func(t *testing.T) {
		// The escape is spelled %2e%2e so it survives the URL as written; the
		// path it resolves to still lands outside the workspace.
		encoded := "file://" + filepath.ToSlash(root) + "/%2e%2e/outside.html"
		if err := urlAllowed(encoded, nil, nil, root); err == nil {
			t.Fatal("a percent-encoded .. escape should be blocked")
		}
	})

	t.Run("a symlinked workspace root still allows a file inside it", func(t *testing.T) {
		realRoot := filepath.Join(base, "realws")
		if err := os.MkdirAll(realRoot, 0o755); err != nil {
			t.Fatalf("create real workspace root: %v", err)
		}
		realFile := filepath.Join(realRoot, "page.html")
		if err := os.WriteFile(realFile, []byte("<html></html>"), 0o644); err != nil {
			t.Fatalf("write real workspace file: %v", err)
		}
		linkRoot := filepath.Join(base, "ws-link")
		if err := os.Symlink(realRoot, linkRoot); err != nil {
			t.Fatalf("create workspace root symlink: %v", err)
		}
		// Both sides resolve, so the file is inside the root however it is
		// spelled.
		if err := urlAllowed(fileURL(filepath.Join(linkRoot, "page.html")), nil, nil, linkRoot); err != nil {
			t.Fatalf("a file inside a symlinked workspace root should be allowed: %v", err)
		}
	})

	t.Run("a nonexistent path fails closed", func(t *testing.T) {
		if err := urlAllowed(fileURL(filepath.Join(root, "missing.html")), nil, nil, root); err == nil {
			t.Fatal("a path that does not exist should be blocked")
		}
	})
}
