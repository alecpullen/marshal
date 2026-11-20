package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"marshal/internal/tools/registry"
)

// CommandEnvironmentSource is implemented by a command runner or sandbox
// backend that can report the exact environment it would give a child process
// running in dir.
//
// The first result is the captured launch input. It must never be nil: an
// intentionally empty environment is []string{}, while nil would mean "the
// runner builds its own environment", which is exactly the ambiguity a
// preparation must not leave. The second result is the analyzer-visible subset
// of that same environment; a variable the backend filtered out is absent, and
// a value only the container image or a login script produces stays unknown.
type CommandEnvironmentSource interface {
	CaptureCommandEnvironment(dir string) ([]string, map[string]string, error)
}

// knownVariableNames are the environment variables the shell analysis can
// classify a command by. A value outside this set is not a policy fact: the
// analyzer treats it as unknown rather than fingerprinting unrelated ambient
// state (or, worse, a secret).
var knownVariableNames = []string{"HOME", "PATH", "PWD", "SHELL", "TMPDIR", "USER"}

// knownEnvironmentFrom returns the analyzer-visible subset of a captured
// environment. A variable the environment does not carry is absent, never
// guessed from the host that captured it.
func knownEnvironmentFrom(environment []string) map[string]string {
	all := make(map[string]string, len(environment))
	for _, kv := range environment {
		if key, value, ok := strings.Cut(kv, "="); ok && key != "" {
			all[key] = value
		}
	}
	known := make(map[string]string, len(knownVariableNames))
	for _, name := range knownVariableNames {
		if value, ok := all[name]; ok {
			known[name] = value
		}
	}
	return known
}

// cloneEnv copies an environment slice, preserving the nil/non-nil distinction:
// an intentionally empty captured environment stays empty.
func cloneEnv(environment []string) []string {
	if environment == nil {
		return nil
	}
	out := make([]string, len(environment))
	copy(out, environment)
	return out
}

// setEnvValue sets key=value in env, replacing an existing entry, and returns
// the resulting slice.
func setEnvValue(env []string, key, value string) []string {
	for i, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

// prepareCommand resolves the one canonical description of the command a call
// would execute: the native toolset's actual command root, grants and
// test-command source, together with the environment its configured backend
// would launch with.
//
// It is called before evaluation so policy and launch judge the same facts. A
// runner that cannot say what environment it would use fails preparation
// rather than guessing: an unknown environment must not be approved as if it
// were the host's.
func (t *toolSet) prepareCommand(_ context.Context, call registry.ToolCall) (registry.CommandFacts, error) {
	command, err := t.preparedCommand(call)
	if err != nil {
		return registry.CommandFacts{}, err
	}
	source, ok := t.runner.(CommandEnvironmentSource)
	if !ok {
		return registry.CommandFacts{}, fmt.Errorf(
			"%s: the configured command runner cannot capture its launch environment", call.Name)
	}
	dir := t.commandRoot()
	if strings.TrimSpace(dir) == "" {
		dir = t.root
	}
	workspace := t.activeRoot()
	if strings.TrimSpace(workspace) == "" {
		workspace = t.root
	}
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = h
	}
	environment, known, err := source.CaptureCommandEnvironment(dir)
	if err != nil {
		return registry.CommandFacts{}, fmt.Errorf("capture %s environment: %w", call.Name, err)
	}
	if environment == nil {
		// A preparation never leaves inheritance ambiguous. The contract makes
		// an empty list the explicit "run with nothing" spelling.
		environment = []string{}
	} else {
		environment = cloneEnv(environment)
	}
	if known == nil {
		known = map[string]string{}
	} else {
		copied := make(map[string]string, len(known))
		for key, value := range known {
			copied[key] = value
		}
		known = copied
	}
	return registry.CommandFacts{
		Command:       command,
		Dir:           dir,
		WorkspaceRoot: workspace,
		HomeDir:       home,
		ReadRoots:     t.commandReadRoots(),
		Env:           known,
		Environment:   environment,
	}, nil
}

// preparedCommand validates and normalizes the command bytes one call would
// run, once. shell.run always names its command; test.run falls back to the
// owner's current configured test command when the caller passed none.
func (t *toolSet) preparedCommand(call registry.ToolCall) (string, error) {
	var command string
	switch call.Name {
	case "shell.run":
		var args shellRunArgs
		if err := decodeCommandArgs(call, &args); err != nil {
			return "", err
		}
		command = args.Command
	case "test.run":
		var args testRunArgs
		if err := decodeCommandArgs(call, &args); err != nil {
			return "", err
		}
		command = args.Command
		if strings.TrimSpace(command) == "" {
			command = t.configuredTestCommand()
		}
	default:
		return "", fmt.Errorf("%s does not run a prepared shell command", call.Name)
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	return command, nil
}

// decodeCommandArgs decodes a command tool's arguments. An absent body is the
// empty argument set, matching what Dispatch accepts for these tools.
func decodeCommandArgs(call registry.ToolCall, dst any) error {
	if len(call.Args) == 0 {
		return nil
	}
	if err := json.Unmarshal(call.Args, dst); err != nil {
		return fmt.Errorf("decode %s arguments: %w", call.Name, err)
	}
	return nil
}

// commandReadRoots lists the roots a shell command may read: the legacy
// additional roots plus every readable grant this toolset holds (named aliases,
// the project checkout of an active worktree and operation-specific permits).
// A write-only grant is not a read root.
func (t *toolSet) commandReadRoots() []string {
	roots := append([]string(nil), t.additionalRoots...)
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		seen[root] = true
	}
	for _, grant := range t.grantedRoots() {
		if !grant.Read || grant.Path == "" || seen[grant.Path] {
			continue
		}
		seen[grant.Path] = true
		roots = append(roots, grant.Path)
	}
	return roots
}
