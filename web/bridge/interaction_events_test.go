package bridge

import (
	"encoding/json"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// A mode_changed update carries the whole committed tuple, so the fleet delta
// must forward all four fields — including an explicit false, which is a
// revocation and not an omission.
func TestModeDeltaPreservesAutoSystemAndRevision(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want AccessState
	}{
		{
			name: "grant",
			body: `{"sessionId":"s1","update":{"kind":"mode_changed","mode":"act","auto":true,"system_access":true,"access_revision":4}}`,
			want: AccessState{Mode: "act", Auto: true, System: true, Revision: 4},
		},
		{
			name: "revocation",
			body: `{"sessionId":"s1","update":{"kind":"mode_changed","mode":"ask","auto":false,"system_access":false,"access_revision":5}}`,
			want: AccessState{Mode: "ask", Auto: false, System: false, Revision: 5},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, ok := classifyNotification("session/update", json.RawMessage(c.body))
			if !ok || d.Kind != "mode" {
				t.Fatalf("delta = %+v ok=%v", d, ok)
			}
			if d.Access == nil {
				t.Fatal("mode delta carries no access state")
			}
			if *d.Access != c.want {
				t.Fatalf("access = %+v, want %+v", *d.Access, c.want)
			}

			// Every field must reach the wire, false included: a client that
			// cannot see an explicit false cannot tell a revocation from a
			// value it was never told.
			out, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(out, &wire); err != nil {
				t.Fatal(err)
			}
			access, ok := wire["access"].(map[string]any)
			if !ok {
				t.Fatalf("delta = %s, want an access object", out)
			}
			for field, want := range map[string]any{
				"mode":            c.want.Mode,
				"auto":            c.want.Auto,
				"system_access":   c.want.System,
				"access_revision": float64(c.want.Revision),
			} {
				got, present := access[field]
				if !present {
					t.Fatalf("access.%s missing from %s", field, out)
				}
				if got != want {
					t.Fatalf("access.%s = %v, want %v", field, got, want)
				}
			}

			// The live projection keeps the whole tuple, not just the mode.
			live := newLiveState()
			live.apply(d)
			if got := live.get("s1"); got.access == nil || *got.access != c.want {
				t.Fatalf("live access = %+v, want %+v", got.access, c.want)
			}
		})
	}
}

// A reordered notification must not roll a revocation back, and a replacement
// session starts its own revision sequence rather than inheriting the old
// one's high-water mark.
func TestFleetAccessSnapshotRejectsStaleRevision(t *testing.T) {
	f := testFleet(t)
	a := Agent{
		ID: "a1", Project: "/home/u/a", Name: "one",
		OwnerID: DefaultOwnerID, SourceKind: "local", CreatedAt: time.Now(),
	}
	if err := f.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	apply := func(session string, st AccessState) {
		f.live.apply(fleetDelta{FleetDelta: contract.FleetDelta{SessionID: a.ID, Kind: "mode", Access: &st}, accessSession: session})
	}
	snapshotAccess := func() *AccessState {
		t.Helper()
		snap := f.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("snapshot = %+v", snap)
		}
		return snap[0].Access
	}

	apply("s-1", AccessState{Mode: "act", Auto: true, System: true, Revision: 5})
	if got := snapshotAccess(); got == nil || *got != (AccessState{Mode: "act", Auto: true, System: true, Revision: 5}) {
		t.Fatalf("access = %+v, want the revision-5 state", got)
	}

	// An older revision for the same session is a reordered notification.
	apply("s-1", AccessState{Mode: "ask", Auto: false, System: false, Revision: 3})
	if got := snapshotAccess(); got == nil || *got != (AccessState{Mode: "act", Auto: true, System: true, Revision: 5}) {
		t.Fatalf("access = %+v, want the revision-5 state to survive a stale report", got)
	}

	// A replacement session starts over: its revision 1 is current, not stale.
	apply("s-2", AccessState{Mode: "ask", Auto: false, System: false, Revision: 1})
	if got := snapshotAccess(); got == nil || *got != (AccessState{Mode: "ask", Auto: false, System: false, Revision: 1}) {
		t.Fatalf("access = %+v, want the replacement session's state", got)
	}
}

// Reattaching must ask the agent what it actually holds. The persisted
// starting mode is history, not a grant, so it is never promoted into one.
func TestFleetReattachReadsActualAccessInsteadOfStoredMode(t *testing.T) {
	f, agentOf := recipeFleet(t, func(fa *fakeAgent) {
		fa.results["session/access_state"] = map[string]any{
			"mode": "act", "auto": true, "system_access": false, "access_revision": 7,
		}
	})
	ctx, cancel := testContext(t)
	defer cancel()
	id, err := f.Spawn(ctx, "/home/u/a", SpawnOptions{Name: "one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	// The record stores the canonical mode the legacy "edit" spelling maps to.
	if a, ok := f.ws.Agent(id); !ok || a.Mode != "ask" {
		t.Fatalf("persisted agent = %+v, want the stored ask mode", a)
	}
	// Before reattach the bridge has no report, so it claims nothing.
	if got := f.Snapshot()[0].Access; got != nil {
		t.Fatalf("access = %+v before reattach, want unavailable", got)
	}

	f.dropRuntimesForTest()
	if errs := f.ReattachAll(ctx); len(errs) != 0 {
		t.Fatalf("ReattachAll: %v", errs)
	}

	got := f.Snapshot()[0].Access
	if got == nil {
		t.Fatal("reattach did not read the agent's access state")
	}
	want := AccessState{Mode: "act", Auto: true, System: false, Revision: 7}
	if *got != want {
		t.Fatalf("access = %+v, want %+v (the stored mode must not be reported)", *got, want)
	}
	if calls := agentOf(id).calls("session/access_state"); len(calls) != 1 {
		t.Fatalf("session/access_state calls = %v, want exactly one", calls)
	}
}
