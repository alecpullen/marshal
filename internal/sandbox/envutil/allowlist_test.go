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
		if IsSecretBearer(EnvKey(kv)) {
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
	}
	got := AllowList(parent)
	for _, kv := range got {
		k := EnvKey(kv)
		if k == "LD_LIBRARY_PATH" || k == "LD_AUDIT" ||
			k == "DYLD_FRAMEWORK_PATH" || k == "DYLD_FALLBACK_LIBRARY_PATH" {
			t.Errorf("AllowList leaked dynamic-loader key: %s", kv)
		}
	}
}

func TestAllowList_OrderIsStable(t *testing.T) {
	parent := []string{"B=2", "A=1", "C=3", "HOME=/h"}
	got := AllowList(parent)
	for i := 1; i < len(got); i++ {
		if EnvKey(got[i-1]) > EnvKey(got[i]) {
			t.Fatalf("AllowList output not sorted: %v", got)
		}
	}
}
