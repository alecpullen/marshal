package acp

import (
	"testing"

	"marshal/internal/acp/service"
	"marshal/internal/app"
)

// TestAllListsSessionsInCreationOrder pins the ordering All() is built on
// (decision 5): Go randomises map iteration, so a regression to map order
// would pass only by luck. Three sessions are adopted one after another and
// All() must return exactly them, in creation order. It also pins detach's
// other half: a closed session's runtime leaves the creation order with it.
func TestAllListsSessionsInCreationOrder(t *testing.T) {
	files := newRecordFiles(t)
	p := newProjectHarness(t, files.executable, files.user, files.project)

	var adopted []*app.Runtime
	for _, id := range []string{"s1", "s2", "s3"} {
		rt, _ := p.adopt(t, id, service.Deps{})
		adopted = append(adopted, rt)
	}

	got := p.host.manager.All()
	if len(got) != len(adopted) {
		t.Fatalf("All() returned %d runtimes, want %d: %v", len(got), len(adopted), got)
	}
	for i, rt := range adopted {
		if got[i] != rt {
			t.Fatalf("All() put %s at slot %d, want %s: not creation order %v", got[i].SessionID, i, rt.SessionID, got)
		}
	}

	// detach forgets the creation-order entry with the session: the manager
	// retains one *app.Runtime per closed session otherwise.
	if got := len(p.host.manager.seq); got != len(adopted) {
		t.Fatalf("the creation order holds %d entries, want %d", got, len(adopted))
	}
	rt, ok := p.host.manager.detach("s2")
	if !ok || rt != adopted[1] {
		t.Fatalf("detach s2 = (%v, %v), want the adopted runtime", rt, ok)
	}
	if got := len(p.host.manager.seq); got != len(adopted)-1 {
		t.Fatalf("after detach the creation order holds %d entries, want %d", got, len(adopted)-1)
	}
	got = p.host.manager.All()
	if len(got) != 2 || got[0] != adopted[0] || got[1] != adopted[2] {
		t.Fatalf("All() after detach = %v, want the others in creation order", got)
	}
	if _, ok := p.host.manager.detach("s2"); ok {
		t.Fatal("a second detach of s2 succeeded")
	}
}
