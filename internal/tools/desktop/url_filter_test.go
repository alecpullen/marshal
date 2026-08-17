package desktop

import (
	"strings"
	"testing"
)

func TestURLAllowedEmptyLists(t *testing.T) {
	if err := urlAllowed("https://anything.com", nil, nil); err != nil {
		t.Fatalf("empty lists should allow: %v", err)
	}
}

func TestURLAllowedDenylistBlocks(t *testing.T) {
	err := urlAllowed("https://evil.com/admin", nil, []string{"evil.com"})
	if err == nil {
		t.Fatal("denylist should block evil.com")
	}
	if !strings.Contains(err.Error(), "evil.com") {
		t.Fatalf("error should mention url, got: %v", err)
	}
}

func TestURLAllowedDenylistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/safe", nil, []string{"example.com/admin"}); err != nil {
		t.Fatalf("denylist prefix should not block /safe: %v", err)
	}
	if err := urlAllowed("https://example.com/admin/users", nil, []string{"example.com/admin"}); err == nil {
		t.Fatal("denylist prefix should block /admin/users")
	}
}

func TestURLAllowedAllowlistPermits(t *testing.T) {
	if err := urlAllowed("https://example.com/page", []string{"example.com"}, nil); err != nil {
		t.Fatalf("allowlist should permit example.com: %v", err)
	}
}

func TestURLAllowedAllowlistBlocksUnlisted(t *testing.T) {
	if err := urlAllowed("https://other.com", []string{"example.com"}, nil); err == nil {
		t.Fatal("allowlist should block unlisted host")
	}
}

func TestURLAllowedAllowlistPathPrefix(t *testing.T) {
	if err := urlAllowed("https://example.com/docs/page", []string{"example.com/docs"}, nil); err != nil {
		t.Fatalf("allowlist prefix should permit /docs/page: %v", err)
	}
	if err := urlAllowed("https://example.com/blog", []string{"example.com/docs"}, nil); err == nil {
		t.Fatal("allowlist prefix should block /blog")
	}
}

func TestURLAllowedDenylistWinsOverAllowlist(t *testing.T) {
	if err := urlAllowed("https://example.com/admin", []string{"example.com"}, []string{"example.com/admin"}); err == nil {
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
		if err := urlAllowed(u, nil, nil); err == nil {
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
		if err := urlAllowed(u, nil, nil); err != nil {
			t.Errorf("urlAllowed(%q) should be allowed, got: %v", u, err)
		}
	}
}

func TestURLAllowedInvalidURL(t *testing.T) {
	if err := urlAllowed("not a url", nil, nil); err == nil {
		t.Fatal("invalid URL should be blocked")
	}
}
