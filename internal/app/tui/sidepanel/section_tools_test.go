package sidepanel

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func toolEvents() []registry.AuditEvent {
	return []registry.AuditEvent{
		{ToolName: "file.read", Duration: 10 * time.Millisecond},
		{ToolName: "file.read", Duration: 20 * time.Millisecond},
		{ToolName: "shell.run", Duration: 4200 * time.Millisecond},
		{ToolName: "shell.run", Duration: 100 * time.Millisecond, Error: "exit 1"},
		{ToolName: "file.write_patch", Duration: 5 * time.Millisecond},
	}
}

func TestToolStatsAggregates(t *testing.T) {
	got := ToolStats(toolEvents())
	if len(got) != 3 {
		t.Fatalf("got %d tools, want 3: %+v", len(got), got)
	}
	// Descending by call count, so file.read and shell.run (2 each) lead.
	byName := map[string]ToolStat{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if byName["shell.run"].Calls != 2 {
		t.Errorf("shell.run calls = %d, want 2", byName["shell.run"].Calls)
	}
	if byName["shell.run"].Errors != 1 {
		t.Errorf("shell.run errors = %d, want 1", byName["shell.run"].Errors)
	}
	if byName["shell.run"].Slowest != 4200*time.Millisecond {
		t.Errorf("shell.run slowest = %v, want 4.2s", byName["shell.run"].Slowest)
	}
	if byName["file.read"].Errors != 0 {
		t.Errorf("file.read errors = %d, want 0", byName["file.read"].Errors)
	}
}

func TestToolStatsSortedByCallsDescending(t *testing.T) {
	got := ToolStats(toolEvents())
	for i := 1; i < len(got); i++ {
		if got[i-1].Calls < got[i].Calls {
			t.Errorf("not sorted descending by calls: %+v", got)
			break
		}
	}
}

func TestToolStatsEmpty(t *testing.T) {
	if got := ToolStats(nil); len(got) != 0 {
		t.Errorf("ToolStats(nil) = %+v, want empty", got)
	}
}

func TestToolsSectionIdentity(t *testing.T) {
	s := ToolsSection{}
	if s.ID() != "tools" {
		t.Errorf("ID = %q, want tools", s.ID())
	}
	if s.Priority() != 3 {
		t.Errorf("Priority = %d, want 3", s.Priority())
	}
	if !s.Clippable() {
		t.Error("Clippable = false, want true")
	}
}

func TestToolsSectionRender(t *testing.T) {
	d := Data{Audit: toolEvents()}
	got := StripANSI(strings.Join((ToolsSection{}).Render(d, 34, 10), "\n"))
	for _, want := range []string{"read", "run", "4.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestToolsSectionOneLine(t *testing.T) {
	got := StripANSI((ToolsSection{}).OneLine(Data{Audit: toolEvents()}, 40))
	if !strings.Contains(got, "5") {
		t.Errorf("OneLine = %q, want the call count", got)
	}
	if !strings.Contains(got, "1 failed") {
		t.Errorf("OneLine = %q, want the failure count", got)
	}
}

func TestToolsSectionRelevance(t *testing.T) {
	if (ToolsSection{}).Relevant(Data{}) {
		t.Error("Relevant(no events) = true, want false")
	}
	if !(ToolsSection{}).Relevant(Data{Audit: toolEvents()}) {
		t.Error("Relevant(events) = false, want true")
	}
}
