package acp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
)

// TestTheBridgeNamesTheProjectChildVariable pins the one literal the boundary
// forces the bridge to write out: web/ is standard library only, so
// web/bridge/projectchild.go may not import this package and carries the
// variable's name itself. A rename here must fail here rather than leave the
// children the bridge starts for a project never told they keep a record.
func TestTheBridgeNamesTheProjectChildVariable(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "web", "bridge", "projectchild.go"))
	if err != nil {
		t.Fatalf("read the bridge's projectchild.go: %v", err)
	}
	literal := `projectChildEnv = "` + ProjectChildEnv + `"`
	if !strings.Contains(string(source), literal) {
		t.Fatalf("web/bridge/projectchild.go does not carry %s: the children the bridge starts for a project would never learn they keep a record", literal)
	}
}

// TestOnlyAProjectChildKeepsARecord proves the gate end to end (P6 B3): a
// child started the way the bridge starts a project child — the mark set —
// serves the two project requests, and one started without it, as a fleet
// agent's own child is, answers both as methods that do not exist, the way a
// host without a record does.
func TestOnlyAProjectChildKeepsARecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark bool
	}{
		{"the bridge's project child", true},
		{"a fleet agent's own child", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mark {
				t.Setenv(ProjectChildEnv, "1")
			} else {
				t.Setenv(ProjectChildEnv, "")
			}

			// stdio is the bridge's own connection: the real RunWithViewers
			// runs over a pipe, as the bridge does over the socket it names.
			client, agent := rpc.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- RunWithViewers(ctx, agent, agent, io.Discard,
					filepath.Join(shortTempDir(t), "viewers.sock"))
			}()
			t.Cleanup(func() {
				cancel()
				_ = client.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("the child did not stop")
				}
			})
			conn := rpc.NewConn(client, rpc.Handlers{})

			var state acpwire.ProjectState
			err := conn.Call(context.Background(), acpwire.MethodProjectState, nil, &state)
			if tc.mark {
				if err != nil {
					t.Fatalf("project/state on a marked child = %v, want it served", err)
				}
				if state.Version != AgentVersion {
					t.Fatalf("project/state version = %q, want the child's own %q", state.Version, AgentVersion)
				}
			} else if err == nil || !strings.Contains(err.Error(), "method not found") {
				t.Fatalf("project/state on an unmarked child = %v, want method not found", err)
			}

			var reached int
			err = conn.Call(context.Background(), acpwire.MethodProjectNote, map[string]any{"text": "hello"}, &reached)
			if tc.mark {
				if err != nil {
					t.Fatalf("project/note on a marked child = %v, want it served", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "method not found") {
				t.Fatalf("project/note on an unmarked child = %v, want method not found", err)
			}
		})
	}
}
