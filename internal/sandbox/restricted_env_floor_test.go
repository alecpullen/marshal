package sandbox

import (
	"os"
	"os/user"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/sandbox/envutil"
)

// envValue returns the value for key in an "KEY=VALUE" env slice, or "".
func envValue(env []string, key string) string {
	for _, kv := range env {
		if envutil.EnvKey(kv) == key {
			_, v, _ := strings.Cut(kv, "=")
			return v
		}
	}
	return ""
}

// TestBuildEnvExplicitEmptyKeepsHomeFloor pins the hardening for the reported
// bug: env_allowlist = [] stripped HOME from the child. Without HOME the shell
// tool's login shell expands its "~/.bashrc" PATH prepend against an empty
// $HOME (yielding "/.local/bin:..."), and the Go toolchain cannot derive
// GOPATH/GOMODCACHE — so `go build` failed with
// "neither GOMODCACHE nor GOPATH is set".
func TestBuildEnvExplicitEmptyKeepsHomeFloor(t *testing.T) {
	const fakeHome = "/tmp/marshal-env-floor-home"
	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", "/usr/bin:/bin")

	r := newRestricted(Config{EnvAllowlist: []string{}}, nil)
	env := r.buildEnv()

	if got := envValue(env, "HOME"); got != fakeHome {
		t.Fatalf("explicit-empty allowlist HOME = %q, want %q (env=%v)", got, fakeHome, env)
	}
	if got := envValue(env, "PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("explicit-empty allowlist PATH = %q, want the parent PATH (env=%v)", got, env)
	}
}

// TestBuildEnvExplicitEmptyDeniesEverythingElse is the regression guard for
// the hardening's blast radius: putting HOME on the floor must not turn the
// explicit-empty sentinel back into "inherit the parent environment". Nil and
// explicit-empty still differ in every variable except HOME.
func TestBuildEnvExplicitEmptyDeniesEverythingElse(t *testing.T) {
	t.Setenv("HOME", "/tmp/marshal-env-floor-home")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("MARSHAL_ENV_FLOOR_PROBE", "leaked")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked")

	r := newRestricted(Config{EnvAllowlist: []string{}}, nil)
	env := r.buildEnv()

	if got := envValue(env, "MARSHAL_ENV_FLOOR_PROBE"); got != "" {
		t.Errorf("explicit-empty leaked a non-allowlisted var: %q", got)
	}
	if got := envValue(env, "AWS_SECRET_ACCESS_KEY"); got != "" {
		t.Errorf("explicit-empty leaked a secret var: %q", got)
	}
	if len(env) > 2 {
		t.Errorf("explicit-empty env has %d entries, want at most PATH+HOME: %v", len(env), env)
	}
}

// TestBuildEnvExplicitEmptyRespectsHomeDenylist keeps the floor subordinate to
// the denylist: a user who explicitly denies HOME still gets no HOME, because
// the denylist is documented as always applied to the final result.
func TestBuildEnvExplicitEmptyRespectsHomeDenylist(t *testing.T) {
	t.Setenv("HOME", "/tmp/marshal-env-floor-home")
	t.Setenv("PATH", "/usr/bin:/bin")

	r := newRestricted(Config{EnvAllowlist: []string{}, EnvDenylist: []string{"HOME"}}, nil)
	if got := envValue(r.buildEnv(), "HOME"); got != "" {
		t.Fatalf("denylisted HOME was added by the floor: %q", got)
	}
}

// TestBuildEnvNilAllowlistUsesCuratedDefaults is the other half of the
// distinction: an omitted key (nil) still means "use the curated allowlist",
// which is exactly envutil.AllowList of the parent minus the denylist.
func TestBuildEnvNilAllowlistUsesCuratedDefaults(t *testing.T) {
	t.Setenv("HOME", "/tmp/marshal-env-floor-home")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leaked")

	r := newRestricted(Config{EnvAllowlist: nil}, nil)
	env := r.buildEnv()

	if got := envValue(env, "HOME"); got != "/tmp/marshal-env-floor-home" {
		t.Errorf("nil allowlist HOME = %q, want the parent HOME", got)
	}
	if got := envValue(env, "AWS_SECRET_ACCESS_KEY"); got != "" {
		t.Errorf("nil allowlist leaked a secret var: %q", got)
	}
	want := envutil.AllowList(os.Environ())
	if !reflect.DeepEqual(env, want) {
		t.Errorf("nil allowlist env = %v, want envutil.AllowList(parent) = %v", env, want)
	}
}

// TestHomeDirFallsBackWhenHomeUnset covers the fallback path directly: the
// case the floor exists for is $HOME being absent, so resolution must not
// depend on $HOME alone.
func TestHomeDirFallsBackWhenHomeUnset(t *testing.T) {
	t.Setenv("HOME", "")

	got := homeDir()
	if got == "" {
		t.Skip("no home directory resolvable in this environment")
	}
	u, err := user.Current()
	if err == nil && u.HomeDir != "" && got != u.HomeDir {
		t.Errorf("homeDir() = %q, want the passwd home %q", got, u.HomeDir)
	}
}
