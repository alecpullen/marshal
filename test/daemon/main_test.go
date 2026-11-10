// Package daemon_test is P4's end-to-end proof: a real bridge on the local
// profile's socket, a real per-project `marshal acp --viewers` child, and a
// marshal client attaching to a session through the relay.
package daemon_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"marshal/internal/acp"
	"marshal/internal/testenv"
)

// childEnv marks a re-executed test binary as the project child the bridge
// starts: the `marshal acp --viewers unix://<socket>` process (P4 D2). The
// bridge runs MarshalBin with "acp" prepended to the child's own arguments
// (Child.spawn), so this binary has to be that program.
const childEnv = "MARSHAL_DAEMON_TEST_CHILD"

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

// runChild is the project child: `acp --viewers unix://<socket>`, served over
// this process's own stdio. It returns when the bridge ends stdio, and the
// viewers end with it.
func runChild(args []string) error {
	if len(args) < 3 || args[0] != "acp" || args[1] != "--viewers" {
		return fmt.Errorf("the project child expects `acp --viewers unix://<socket>`, got %q", args)
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
