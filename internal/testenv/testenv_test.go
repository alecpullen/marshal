package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeXDGBlanksOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/somewhere")
	t.Setenv("MARSHAL_DATA_DIR", "/tmp/elsewhere")

	SanitizeXDG()

	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR"} {
		if got := os.Getenv(key); got != "" {
			t.Fatalf("%s = %q after SanitizeXDG, want empty", key, got)
		}
	}
}

// restoreEnv registers a cleanup that puts key back exactly as it was,
// including unsetting a variable that was absent.
func restoreEnv(t *testing.T, key string) {
	t.Helper()
	value, set := os.LookupEnv(key)
	t.Cleanup(func() {
		if set {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}

// TestIsolateHomeRedirectsAndRestores pins the helper's contract: HOME and
// USERPROFILE point at a fresh temporary directory, every other isolated key
// is blanked, an explicitly selected live-test switch survives, and cleanup
// restores each prior value — distinguishing a variable that was absent from
// one that was deliberately empty — and removes the temporary home.
//
// Deliberately not parallel: the test mutates process environment.
func TestIsolateHomeRedirectsAndRestores(t *testing.T) {
	const (
		emptyKey  = "MARSHAL_CONFIG_DIR"            // set-but-empty prior value
		absentKey = "OPENROUTER_API_KEY"            // absent prior value
		liveKey   = "MARSHAL_CREDENTIALS_LIVE_TEST" // not isolated: explicit opt-in
		keyKey    = "OPENAI_API_KEY"                // set prior value
	)
	for _, key := range []string{"HOME", "USERPROFILE", emptyKey, absentKey, liveKey, keyKey} {
		restoreEnv(t, key)
	}

	priorHome := filepath.Join(t.TempDir(), "prior-home")
	if err := os.MkdirAll(priorHome, 0o755); err != nil {
		t.Fatalf("mkdir prior home: %v", err)
	}
	if err := os.Setenv("HOME", priorHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("USERPROFILE", priorHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv(emptyKey, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv(keyKey, "sk-prior-synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv(liveKey, "1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv(absentKey); err != nil {
		t.Fatal(err)
	}

	cleanup, err := IsolateHome()
	if err != nil {
		t.Fatalf("IsolateHome: %v", err)
	}

	isolatedHome := os.Getenv("HOME")
	if isolatedHome == "" || isolatedHome == priorHome {
		t.Fatalf("HOME = %q after IsolateHome, want a fresh temporary directory", isolatedHome)
	}
	if got := os.Getenv("USERPROFILE"); got != isolatedHome {
		t.Fatalf("USERPROFILE = %q, want it to follow HOME (%q)", got, isolatedHome)
	}
	info, err := os.Stat(isolatedHome)
	if err != nil {
		t.Fatalf("stat isolated home: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("isolated home %s is not a directory", isolatedHome)
	}
	for _, key := range isolatedKeys {
		if key == "HOME" || key == "USERPROFILE" {
			continue
		}
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s = %q while isolated, want empty", key, got)
		}
	}
	if got := os.Getenv(liveKey); got != "1" {
		t.Errorf("%s = %q while isolated, want the explicit live switch preserved", liveKey, got)
	}

	cleanup()

	if got := os.Getenv("HOME"); got != priorHome {
		t.Errorf("HOME = %q after cleanup, want %q", got, priorHome)
	}
	if got := os.Getenv("USERPROFILE"); got != priorHome {
		t.Errorf("USERPROFILE = %q after cleanup, want %q", got, priorHome)
	}
	if got := os.Getenv(keyKey); got != "sk-prior-synthetic" {
		t.Errorf("%s = %q after cleanup, want the prior value restored", keyKey, got)
	}
	// A prior empty-but-set variable stays set: empty is meaningful to the
	// resolvers (it means "no override"), absent is not the same thing.
	if got, set := os.LookupEnv(emptyKey); !set || got != "" {
		t.Errorf("%s = (%q, set=%v) after cleanup, want a set empty value", emptyKey, got, set)
	}
	// A variable that was absent must be absent again, not left empty.
	if got, set := os.LookupEnv(absentKey); set {
		t.Errorf("%s = %q after cleanup, want it unset again", absentKey, got)
	}
	if _, err := os.Stat(isolatedHome); !os.IsNotExist(err) {
		t.Errorf("temporary home %s still present after cleanup (err=%v)", isolatedHome, err)
	}
}

// TestIsolateHomeIndependentInvocations pins that two isolated scopes do not
// share a home directory, so a test that nests IsolateHome cannot inherit the
// outer scope's paths.
func TestIsolateHomeIndependentInvocations(t *testing.T) {
	restoreEnv(t, "HOME")

	outer, err := IsolateHome()
	if err != nil {
		t.Fatalf("outer IsolateHome: %v", err)
	}
	outerHome := os.Getenv("HOME")

	inner, err := IsolateHome()
	if err != nil {
		outer()
		t.Fatalf("inner IsolateHome: %v", err)
	}
	innerHome := os.Getenv("HOME")
	if innerHome == outerHome {
		inner()
		outer()
		t.Fatalf("inner scope reused outer home %q", outerHome)
	}
	inner()
	if got := os.Getenv("HOME"); got != outerHome {
		t.Errorf("HOME = %q after inner cleanup, want the outer home %q", got, outerHome)
	}
	outer()
}
