// Package daemon_test is P4's end-to-end proof: a real bridge on the local
// profile's socket, a real per-project `marshal acp --viewers` child, and a
// marshal client attaching to a session through the relay.
package daemon_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"marshal/internal/acp"
	"marshal/internal/testenv"
)

// childEnv marks a re-executed test binary as the project child the bridge
// starts: the `marshal acp --viewers unix://<socket>` process (P4 D2). The
// bridge runs MarshalBin with "acp" prepended to the child's own arguments
// (Child.spawn), so this binary has to be that program.
const childEnv = "MARSHAL_DAEMON_TEST_CHILD"

// childPIDEnv names a file the project child writes its process ID to when it
// starts, when the variable is set. GET /api/daemon reports no process IDs, so
// this is how a test that has to end a project child's process — as a crash
// ends one — finds it. Only `acp --viewers` writes it, because the bridge also
// starts a control agent from this same binary (a plain `acp`), and the test
// wants the child that serves its project. Unset in ordinary runs, so nothing
// else in this package is affected.
const childPIDEnv = "MARSHAL_DAEMON_TEST_CHILD_PID"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		if err := runChild(os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "marshal acp: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(testenv.Run(m))
}

// buildEnv names the build a `--version` answer reports. The bridge checks a
// new build by running the binary's `--version` (web/bridge/restart.go), and
// the child inherits this process's environment, so a test that rebuilt the
// file says which build it is. Unset in ordinary runs.
const buildEnv = "MARSHAL_DAEMON_TEST_BUILD"

// testBuild is the build a `--version` answer names when buildEnv is unset.
const testBuild = "test-build"

// runChild is the child the bridge starts, served over this process's own
// stdio. A project's child is `acp --viewers unix://<socket>`: it returns when
// the bridge ends stdio, and the viewers end with it. A plain `acp` is the
// child an agent of its own gets — web/bridge starts that one with no
// arguments — and it serves its one session over stdio.
//
// A sole `--version` answers the build check instead: the bridge runs it to
// learn whether the binary on disk starts at all, and what it is (P6 B4).
func runChild(args []string) error {
	if len(args) == 1 && args[0] == "--version" {
		build := os.Getenv(buildEnv)
		if build == "" {
			build = testBuild
		}
		fmt.Printf("marshal %s\n", build)
		return nil
	}
	if len(args) == 1 && args[0] == "acp" {
		return acp.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr)
	}
	if len(args) < 3 || args[0] != "acp" || args[1] != "--viewers" {
		return fmt.Errorf("the child expects `acp` or `acp --viewers unix://<socket>`, got %q", args)
	}
	// A project child records its process ID for the tests that have to end
	// it: the child's own arguments are the bridge's, so the file comes from
	// the environment the bridge inherited.
	if path := os.Getenv(childPIDEnv); path != "" {
		pid := strconv.Itoa(os.Getpid())
		if err := os.WriteFile(path, []byte(pid), 0o600); err != nil {
			return fmt.Errorf("write the child's process id to %s: %w", path, err)
		}
	}
	network, path, err := acp.ParseListenAddr(args[2])
	if err != nil {
		return err
	}
	if network != "unix" {
		return fmt.Errorf("--viewers takes a unix socket, got %s://", network)
	}
	return acp.RunWithViewers(context.Background(), os.Stdin, os.Stdout, os.Stderr, path)
}
