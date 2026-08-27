package bridge

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestHardenedGitArgsDisableHooks(t *testing.T) {
	args := hardenedGitArgs("fetch", "origin", "main")
	want := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
		"fetch", "origin", "main",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("hardenedGitArgs = %v, want %v", args, want)
	}
}

func TestGitEnvNeverCarriesAmbientConfig(t *testing.T) {
	os.Setenv("GIT_CONFIG_GLOBAL", "/tmp/attacker")
	defer os.Unsetenv("GIT_CONFIG_GLOBAL")

	env := gitEnv("", Credential{Kind: "none"})
	for _, kv := range env {
		if kv == "GIT_CONFIG_GLOBAL=/tmp/attacker" {
			t.Fatalf("ambient GIT_CONFIG_GLOBAL leaked into git env: %v", env)
		}
	}
	// The hardened value must be present.
	if !hasEnv(env, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("GIT_CONFIG_GLOBAL=/dev/null missing from git env: %v", env)
	}
	if !hasEnv(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatalf("GIT_CONFIG_NOSYSTEM=1 missing from git env: %v", env)
	}
}

func TestGitEnvPassesSecretOutOfBand(t *testing.T) {
	cred := Credential{
		ID:      "gh",
		Kind:    "pat",
		EnvVar:  "GH_TOKEN",
		OwnerID: "local",
		User:    "me",
		literal: "s3cr3t",
	}
	env := gitEnv("/bin/askpass", cred)
	if !hasEnv(env, "GIT_ASKPASS=/bin/askpass") {
		t.Fatalf("GIT_ASKPASS missing: %v", env)
	}
	if !hasEnv(env, "MARSHAL_ASKPASS=1") {
		t.Fatalf("MARSHAL_ASKPASS=1 missing: %v", env)
	}
	if !hasEnv(env, "MARSHAL_ASKPASS_SECRET=s3cr3t") {
		t.Fatalf("MARSHAL_ASKPASS_SECRET missing: %v", env)
	}
	if !hasEnv(env, "MARSHAL_ASKPASS_USER=me") {
		t.Fatalf("MARSHAL_ASKPASS_USER missing: %v", env)
	}
	if hasEnv(env, "GH_TOKEN=s3cr3t") {
		t.Fatalf("secret leaked as plain env var: %v", env)
	}
}

func TestGitRunnerHardensEveryCall(t *testing.T) {
	var got [][]string
	g := &gitRunner{
		bin:        "git",
		askpassBin: "/bin/askpass",
		exec: func(dir string, env []string, args ...string) ([]byte, error) {
			got = append(got, args)
			return nil, nil
		},
	}
	cred := Credential{Kind: "none"}
	if _, err := g.run("/tmp/repo", cred, "fetch", "origin"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("exec called %d times, want 1", len(got))
	}
	call := got[0]
	want := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
		"fetch", "origin",
	}
	if !reflect.DeepEqual(call, want) {
		t.Fatalf("exec args = %v, want hardened %v", call, want)
	}
}

func TestNewGitRunnerAskpassBinIsExecutable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g, err := newGitRunner()
	if err != nil {
		t.Fatalf("newGitRunner: %v", err)
	}

	// GIT_ASKPASS must name something git can actually exec. A path that
	// does not exist fails silently at clone time, because
	// GIT_TERMINAL_PROMPT=0 leaves no fallback prompt.
	info, err := os.Stat(g.askpassBin)
	if err != nil {
		t.Fatalf("askpassBin %q does not exist: %v", g.askpassBin, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("askpassBin %q is not executable (mode %v)", g.askpassBin, info.Mode())
	}
}

func TestNewGitRunnerAskpassIsThisBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g, err := newGitRunner()
	if err != nil {
		t.Fatalf("newGitRunner: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The askpass handler is a mode of this same binary
	// (cmd/webbridge/main.go, MARSHAL_ASKPASS=1), not a separate tool.
	if g.askpassBin != self {
		t.Fatalf("askpassBin = %q, want this executable %q", g.askpassBin, self)
	}
}

func hasEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
