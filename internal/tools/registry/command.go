package registry

import "context"

// CommandFacts is one native command's canonical execution facts: what will
// run, where, in which workspace, and the environment it will run in.
//
// It is the single description shared by preparation, policy evaluation and
// launch. A field that was not observed is left empty rather than guessed: an
// unknown container-image or login-script value is unknown to policy, never a
// host value standing in for it.
type CommandFacts struct {
	// Command is the validated, normalized command bytes.
	Command string
	// Dir is the working directory the process will be launched in.
	Dir string
	// WorkspaceRoot is the workspace the command belongs to.
	WorkspaceRoot string
	// HomeDir is the home directory the analyzer may resolve "~" against.
	HomeDir string
	// ReadRoots are the additional roots the command may read.
	ReadRoots []string
	// ScratchRoots are the roots the command may read AND write as ordinary
	// working space, outside the workspace: the operating system's temporary
	// directory and the session's own scratch area.
	//
	// A target inside one of them is not an external operation, so it does not
	// require System. The temporary directory in particular is where a session
	// stages build output, test fixtures and throwaway scripts as a matter of
	// course, and the shell surface could already read and write it without a
	// System grant — treating it as privileged space was an inconsistency
	// between two surfaces rather than a boundary worth keeping.
	ScratchRoots []string
	// Env is the environment facts the analyzer actually knows: only names
	// whose value the launch is known to carry. A variable absent here is
	// unknown and must not be guessed from the host. Policy fingerprints
	// include this map; they never include Environment.
	Env map[string]string
	// Environment is the captured backend shell input: the exact "KEY=VALUE"
	// list the process will be launched with. Nil preserves a legacy launch
	// that inherits the runner's own environment; non-nil — including an
	// intentionally empty list — means this captured list is the environment.
	// It is json:"-" and is never logged, audited or fingerprinted as a whole.
	Environment []string `json:"-"`
}

// CommandPreparer prepares the actual native command a tool call would run.
//
// A tool that executes no shell command leaves Tool.PrepareCommand nil. A
// prepared command is what policy evaluates and what launch must use: an
// edited command starts a fresh preparation, and a launch may never substitute
// a second description of the same call.
type CommandPreparer func(context.Context, ToolCall) (CommandFacts, error)

// cloneCommandFacts returns facts whose slices and map are the caller's own,
// so a view or consumer that mutates them cannot reach into the prepared value
// another caller sees. The nil/non-nil distinction of Environment is
// preserved: an intentionally empty captured environment stays empty rather
// than collapsing to "inherit".
func cloneCommandFacts(f CommandFacts) CommandFacts {
	out := f
	out.ReadRoots = cloneStrings(f.ReadRoots)
	out.ScratchRoots = cloneStrings(f.ScratchRoots)
	out.Env = cloneStringMap(f.Env)
	out.Environment = cloneStrings(f.Environment)
	return out
}

// cloneStrings copies a string slice, keeping nil as nil and an empty non-nil
// slice as an empty non-nil slice.
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneStringMap copies a string map, keeping nil as nil.
func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
