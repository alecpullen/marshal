// Package envutil hosts env-scrubbing helpers shared between the sandbox
// and the hooks runner.
package envutil

import (
	"testing"
)

func TestAllowList_StripsSecrets(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"ANTHROPIC_API_KEY=sk-secret",
		"OPENAI_API_KEY=sk-secret",
		"AWS_ACCESS_KEY_ID=AKIA...",
		"GH_TOKEN=ghp_...",
		"LD_PRELOAD=/tmp/evil.so",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"IFS=,",
		"LANG=en_US.UTF-8",
		"USER=alice",
	}
	got := AllowList(parent)
	for _, kv := range got {
		if IsSecretKey(EnvKey(kv)) {
			t.Errorf("AllowList leaked secret key: %s", kv)
		}
		if k := EnvKey(kv); k == "LD_PRELOAD" || k == "DYLD_INSERT_LIBRARIES" || k == "IFS" {
			t.Errorf("AllowList leaked dangerous key: %s", kv)
		}
	}
}

func TestAllowList_PreservesCoreVars(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"USER=alice",
		"TZ=UTC",
		"TMPDIR=/tmp",
		"TERM=xterm-256color",
	}
	got := AllowList(parent)
	want := map[string]bool{
		"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true,
		"USER": true, "TZ": true, "TMPDIR": true, "TERM": true,
	}
	for _, kv := range got {
		if !want[EnvKey(kv)] {
			t.Errorf("unexpected key in allowlist: %s", kv)
		}
		delete(want, EnvKey(kv))
	}
	if len(want) != 0 {
		t.Errorf("missing core keys: %v", want)
	}
}

func TestAllowList_StripsLDAndDYLDWildcards(t *testing.T) {
	parent := []string{
		"LD_LIBRARY_PATH=/tmp",
		"LD_AUDIT=/tmp/audit.so",
		"DYLD_FRAMEWORK_PATH=/tmp",
		"DYLD_FALLBACK_LIBRARY_PATH=/tmp",
		"PATH=/usr/bin", // allowed key — proves the function is not a no-op
	}
	got := AllowList(parent)
	// Verify the dangerous keys are stripped.
	for _, kv := range got {
		k := EnvKey(kv)
		if k == "LD_LIBRARY_PATH" || k == "LD_AUDIT" ||
			k == "DYLD_FRAMEWORK_PATH" || k == "DYLD_FALLBACK_LIBRARY_PATH" {
			t.Errorf("AllowList leaked dynamic-loader key: %s", kv)
		}
	}
	// Verify the allowed key survived.
	found := false
	for _, kv := range got {
		if EnvKey(kv) == "PATH" {
			found = true
			break
		}
	}
	if !found {
		t.Error("AllowList dropped allowed key PATH but should have kept it")
	}
}

func TestAllowList_OrderIsStable(t *testing.T) {
	// All keys are in allowlistKeys so the sort loop actually iterates.
	parent := []string{"TMPDIR=/t", "PATH=/usr/bin", "USER=alice"}
	got := AllowList(parent)
	if len(got) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if EnvKey(got[i-1]) > EnvKey(got[i]) {
			t.Fatalf("AllowList output not sorted: %v", got)
		}
	}
}

func TestIsSecretKeyUnion(t *testing.T) {
	secret := []string{
		// substring patterns
		"OPENAI_API_KEY", "MY_TOKEN_INFO", "POSTGRES_PASSWORD", "GH_PAT",
		// exact names without telling substrings
		"SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO",
		"DATABASE_URL", "REDIS_URL", "MONGODB_URI",
		// provider prefixes from IsSecretBearer
		"GOOGLE_API_KEY", "HUGGINGFACE_TOKEN", "OPENROUTER_API_KEY",
		// bare suffix rules from IsSecretBearer
		"MY_KEY", "SERVICE_PRIVATE_KEY", "BACKUP_CREDENTIALS",
	}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false, want true", k)
		}
	}
	benign := []string{"PATH", "HOME", "MONKEY", "KEYBOARD_LAYOUT", "TONALITY"}
	for _, k := range benign {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true, want false", k)
		}
	}
}

func TestIsDangerousKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"LD_PRELOAD", true},
		{"LD_LIBRARY_PATH", true},
		{"DYLD_INSERT_LIBRARIES", true},
		{"DYLD_FRAMEWORK_PATH", true},
		{"IFS", true},
		{"SHELLOPTS", true},
		{"BASH_ENV", true},
		{"ENV", true},
		{"BASH_FUNC_foo", true},
		{"BASH_FUNC_bar", true},
		{"ZDOTDIR", true},
		{"PATH", true},
		{"USER", false},
		{"HOME", false},
		{"TMPDIR", false},
		{"ANTHROPIC_API_KEY", false}, // secret-bearing, but not dangerous
		{"LANG", false},
		{"TERM", false},
	}
	for _, tc := range tests {
		got := IsDangerousKey(tc.key)
		if got != tc.want {
			t.Errorf("IsDangerousKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
