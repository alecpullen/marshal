package bridge

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// hardenedGitArgs prefixes a caller's git invocation with flags that
// neutralise two remote-code-execution channels a hostile repo can
// exploit. core.hooksPath=/dev/null makes git ignore the repository's
// own hooks (which would otherwise run arbitrary shell on checkout or
// fetch); protocol.ext.allow=never rejects the external (ext) transport
// protocol that invokes arbitrary commands on subprocess. The caller's
// own args always come last, so nothing the operator typed is reordered.
func hardenedGitArgs(args ...string) []string {
	return append([]string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
	}, args...)
}

// gitEnv builds the environment for a git child process. The ambient
// GIT_CONFIG_* variables are deliberately replaced: git would otherwise
// honour a global config that could install aliases or credential
// helpers, so any attacker-supplied config is cut off at the boundary.
// PATH is inherited because git needs it to locate helper binaries. The
// returned slice never contains the credential literal itself as a plain
// variable — a PAT is passed via the askpass protocol, where git treats
// it as opaque stdin data rather than as environment that might be
// inherited or logged.
func gitEnv(askpassBin string, cred Credential) []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	switch cred.Kind {
	case "pat":
		env = append(env,
			"GIT_ASKPASS="+askpassBin,
			"MARSHAL_ASKPASS=1",
			"MARSHAL_ASKPASS_SECRET="+cred.literal,
			"MARSHAL_ASKPASS_USER="+cred.username(),
		)
	case "ssh":
		// The SSH key is passed via GIT_SSH_COMMAND so git invokes ssh
		// with the key pinned and never attempts interactive password
		// prompts or the user's default identity agent. The key path is
		// validated to contain no spaces or shell metacharacters: a path
		// like "/tmp/key -o ProxyCommand=evil" would inject ssh options.
		if strings.ContainsAny(cred.KeyPath, " \t\n\"'\\;&|`$()") {
			// Refuse to build the command rather than risk injection.
			// The empty string here causes git to fail with a clear
			// "no GIT_SSH_COMMAND" rather than executing a hostile one.
			break
		}
		env = append(env,
			"GIT_SSH_COMMAND=ssh -i "+cred.KeyPath+
				" -o IdentitiesOnly=yes -o BatchMode=yes",
		)
	}
	return env
}

// gitExecFunc is the injection seam that lets tests observe and stub the
// child process without ever invoking a real git binary.
type gitExecFunc func(dir string, env []string, args ...string) ([]byte, error)

// gitRunner executes hardened git subprocesses. It owns both the git
// binary path and the askpass shim path, and guarantees every call goes
// through hardenedGitArgs and gitEnv.
type gitRunner struct {
	bin        string
	askpassBin string
	exec       gitExecFunc

	// mirrorLocks serialises concurrent EnsureMirror calls for the same
	// URL so two agents spawning against one repo do not race on the
	// shared bare mirror.
	mirrorLocks sync.Map // map[string]*sync.Mutex
}

// newGitRunner locates the git binary on PATH and the askpass shim
// shipped alongside the current executable. Both paths are fixed once at
// construction so a later cwd or PATH change cannot redirect them.
func newGitRunner() (*gitRunner, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("find git: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable for GIT_ASKPASS: %w", err)
	}
	// GIT_ASKPASS is this same binary re-executed in askpass mode
	// (MARSHAL_ASKPASS=1), not a separate helper: there is no
	// webbridge-askpass to ship, and a path that does not exist fails
	// silently at clone time because GIT_TERMINAL_PROMPT=0 leaves no
	// fallback prompt.
	return &gitRunner{bin: bin, askpassBin: self}, nil
}

// mirrorMutex returns the lock for a specific mirror directory,
// creating it on first use.
func (g *gitRunner) mirrorMutex(dir string) *sync.Mutex {
	v, _ := g.mirrorLocks.LoadOrStore(dir, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// run executes git with dir as the working directory and cred supplying
// any authentication. The error message deliberately omits the env: it
// can carry an askpass secret, and a stderr echo on a shared CI log
// would leak it.
func (g *gitRunner) run(dir string, cred Credential, args ...string) ([]byte, error) {
	full := hardenedGitArgs(args...)
	env := gitEnv(g.askpassBin, cred)
	if g.exec != nil {
		return g.exec(dir, env, full...)
	}
	cmd := exec.Command(g.bin, full...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Trim the trailing newline git emits; the caller may wrap this
		// in a larger error. Never include env.
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
