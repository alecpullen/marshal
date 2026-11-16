// Package factorysubagents is the end-to-end proof of the Studio worker
// subagent flow: a real Marshal ACP child, the real agent runtime inside it,
// a real factory controller and a deterministic offline provider.
//
// The test deliberately lives outside web/bridge. `TestWebIsStdlibOnly`
// (web/bridge/boundary_test.go) walks every .go file under web/, tests
// included, and forbids marshal/internal/..., so a real agent runtime cannot be
// built there. This package is inside the main module and may import both the
// bridge and the agent side.
package factorysubagents

import (
	"context"
	"fmt"
	"os"
	"testing"

	"marshal/internal/acp"
	"marshal/internal/testenv"
)

// childEnv marks a re-executed test binary as the project child the bridge
// starts for an agent: `marshal acp`, serving one session over stdio
// (web/bridge Child.spawn prepends "acp" to the child's own arguments). The
// fleet is built with this test binary as MarshalBin, so the whole agent
// runtime under test is the production one.
const childEnv = "MARSHAL_FACTORY_SUBAGENTS_TEST_CHILD"

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

// runChild is the agent the bridge starts, served over this process's own
// stdio. An agent of its own is `acp --viewers unix://<socket>`: the viewer
// socket is where the bridge's own client would attach, and the session the
// factory prompts runs beside it. A plain `acp` is the same server with no
// viewer socket.
func runChild(args []string) error {
	if len(args) == 1 && args[0] == "acp" {
		return acp.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr)
	}
	if len(args) < 3 || args[0] != "acp" || args[1] != "--viewers" {
		return fmt.Errorf("the child expects `acp` or `acp --viewers unix://<socket>`, got %q", args)
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
