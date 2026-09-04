package watch

import (
	"strings"
	"testing"
	"time"
)

func TestFormatBasic(t *testing.T) {
	got := Format(Report{
		Name:      "build",
		Kind:      KindCommand,
		Condition: "exit_code 0",
		Interval:  5 * time.Second,
		Sample:    "line1\nline2",
	})
	want := "[watch build fired] kind=command interval=5s\ncondition: exit_code 0\nlast sample (tail): line1\nline2"
	if got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}

func TestFormatSuffixes(t *testing.T) {
	got := Format(Report{
		Name:        "build",
		Kind:        KindCommand,
		AutoRemoved: true,
		FiredCount:  3,
	})
	if !strings.Contains(got, " (auto-removed)") {
		t.Fatalf("Format missing auto-removed suffix: %q", got)
	}
	if !strings.Contains(got, " (fired 3 times)") {
		t.Fatalf("Format missing fired-count suffix: %q", got)
	}
}

func TestFormatSubagentOwnerLabel(t *testing.T) {
	got := Format(Report{
		Name:  "build",
		Kind:  KindCommand,
		Owner: "subagent-7",
	})
	if !strings.Contains(got, " (from subagent subagent-7)") {
		t.Fatalf("Format missing subagent owner label: %q", got)
	}
}

func TestFormatNoIntervalWhenZero(t *testing.T) {
	got := Format(Report{Name: "x", Kind: KindFile})
	if strings.Contains(got, "interval=") {
		t.Fatalf("Format should omit interval when zero: %q", got)
	}
}
