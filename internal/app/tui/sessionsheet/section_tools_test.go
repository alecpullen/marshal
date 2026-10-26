package sessionsheet

import (
	"strings"
	"testing"

	"marshal/internal/acpwire"
)

// toolStats is the aggregate SheetToolsOf returns for the tool-call log the
// section used to read: file.read twice (20 ms slowest), shell.run twice with
// one failure (4.2 s slowest), file.write_patch once (5 ms).
func toolStats() []acpwire.SheetTool {
	return []acpwire.SheetTool{
		{Name: "file.read", Calls: 2, SlowestMs: 20},
		{Name: "shell.run", Calls: 2, Errors: 1, SlowestMs: 4200},
		{Name: "file.write_patch", Calls: 1, SlowestMs: 5},
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
	d := Data{Tools: toolStats()}
	got := StripANSI(strings.Join((ToolsSection{}).Render(d, 34, 10), "\n"))
	for _, want := range []string{"read", "run", "4.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render missing %q:\n%s", want, got)
		}
	}
}

func TestToolsSectionOneLine(t *testing.T) {
	got := StripANSI((ToolsSection{}).OneLine(Data{Tools: toolStats()}, 40))
	if !strings.Contains(got, "5") {
		t.Errorf("OneLine = %q, want the call count", got)
	}
	if !strings.Contains(got, "1 failed") {
		t.Errorf("OneLine = %q, want the failure count", got)
	}
}

func TestToolsSectionColorsErrorCount(t *testing.T) {
	d := Data{Tools: []acpwire.SheetTool{
		{Name: "file.read", Calls: 1},
		{Name: "shell.run", Calls: 1, Errors: 1},
	}}
	rows := ToolsSection{}.Render(d, 30, 0)

	var errRow string
	for _, r := range rows {
		if strings.Contains(StripANSI(r), "✘") {
			errRow = r
		}
	}
	if errRow == "" {
		t.Fatal("no row reported an error")
	}
	if errRow == StripANSI(errRow) {
		t.Errorf("error count is unstyled: %q", StripANSI(errRow))
	}
}

func TestToolsSectionCleanRunHasNoStatusColor(t *testing.T) {
	d := Data{Tools: []acpwire.SheetTool{{Name: "file.read", Calls: 1}}}
	for _, r := range (ToolsSection{}).Render(d, 30, 0) {
		if strings.Contains(r, "\x1b[") {
			t.Errorf("clean run should be unstyled, got %q", r)
		}
	}
}

func TestToolsSectionRelevance(t *testing.T) {
	if (ToolsSection{}).Relevant(Data{}) {
		t.Error("Relevant(no tools) = true, want false")
	}
	if !(ToolsSection{}).Relevant(Data{Tools: toolStats()}) {
		t.Error("Relevant(tools) = false, want true")
	}
}
