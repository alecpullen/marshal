package viewmodel

import (
	"strings"
	"testing"
)

// The band travels to clients that render it on their own terminals, so the
// projection has to be safe for a BandInfo a caller assembled itself.
func TestProjectBandScrubsCallerSuppliedText(t *testing.T) {
	n := ProjectBand(BandInfo{
		Todos: true, Done: 1, Total: 3,
		Active:   "work\x1b]52;c;x\x07",
		Next:     []string{"next\x1b[2J"},
		Entities: []BandEntity{{Ref: NodeID{KindTool, "tool:1"}, Label: "job-1", Detail: "run\x1b]0;t\x07"}},
	})
	if strings.Contains(n.Band.Active, "\x1b") {
		t.Fatalf("active = %q", n.Band.Active)
	}
	if strings.Contains(n.Band.Next[0], "\x1b") {
		t.Fatalf("next = %q", n.Band.Next[0])
	}
	if strings.Contains(n.Band.Entities[0].Detail, "\x1b") {
		t.Fatalf("entity = %+v", n.Band.Entities[0])
	}
}
