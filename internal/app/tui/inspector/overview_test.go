package inspector

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/app/tui/sidepanel"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// overviewNow is the injected clock every Overview fixture uses, so elapsed
// time and telemetry are deterministic.
var overviewNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// overviewData is a real sidepanel.Data with every section that can be
// relevant populated. The section types are the production ones — nothing
// here is mocked, so the Overview is exercised against the same renderers
// the rail uses.
func overviewData(now time.Time) Data {
	st := session.New(config.Config{}, "/tmp", now.Add(-90*time.Second), session.Persistence{})
	return Data{
		Side: sidepanel.Data{
			State: st,
			Git:   gitinfo.Info{InRepo: true, Branch: "main"},
			Repo: sidepanel.RepoStats{
				Files:     1234,
				Symbols:   5678,
				Languages: []sidepanel.LanguageShare{{Name: "Go", Share: 80}},
			},
			Totals: db.UsageTotals{Turns: 7, PromptTokens: 42_100, CompletionTokens: 8_300},
			Changed: []sidepanel.ChangedFile{
				{Path: "internal/app/tui/inspector/overview.go", Status: 'A', Added: 120},
				{Path: "internal/app/tui/inspector/inspector.go", Status: 'A', Added: 90},
				{Path: "internal/app/tui/model.go", Status: 'M', Added: 12, Removed: 3},
				{Path: "internal/app/tui/view.go", Status: 'M', Added: 4, Removed: 1},
				{Path: "internal/app/tui/frame.go", Status: 'M', Added: 3},
				{Path: "internal/app/tui/old.go", Status: 'D', Removed: 40},
			},
			Audit: []registry.AuditEvent{
				{
					ToolName:  "file.read",
					Args:      json.RawMessage(`{"path":"internal/app/tui/transcript.go"}`),
					Timestamp: now,
					Duration:  120 * time.Millisecond,
				},
				{
					ToolName:     "file.write_patch",
					FilesChanged: []string{"internal/app/tui/status.go"},
					Timestamp:    now.Add(time.Second),
					Duration:     40 * time.Millisecond,
				},
			},
			Rules:  []string{"allow go test *"},
			Skills: []string{"marshal-writing-plans"},
			Pack: contextpack.Pack{
				Sections: []contextpack.Section{
					{Kind: contextpack.SectionRepoCard, Title: "repo card", EstimatedTokens: 1200},
				},
				TokenUsage: contextpack.TokenUsage{MaxTokens: 128_000, EstimatedTokens: 4_200},
			},
			Now: now,
		},
	}
}

// overviewLines splits a rendered view into its rows.
func overviewLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Overview is the retained telemetry view: it must render the same section
// content the rail does, from the same Data.
func TestOverviewRendersChangedFilesAndRepoStats(t *testing.T) {
	d := overviewData(overviewNow)
	m := New()
	m.Resize(120, 60)
	m.SetData(d)

	out := sidepanel.StripANSI(m.View(d))
	if !strings.Contains(out, "internal/app/tui/inspector/overview.go") {
		t.Errorf("Overview did not render the changed-file rows:\n%s", out)
	}
	if !strings.Contains(out, "1k files · 5k symbols") {
		t.Errorf("Overview did not render the repo stats:\n%s", out)
	}
	if !strings.Contains(out, "CHANGED") || !strings.Contains(out, "REPO") {
		t.Errorf("Overview did not render the section headers:\n%s", out)
	}
}

// The compact summary is built from the sections' own one-line digests,
// ordered by section priority so the most important facts survive the
// truncation a narrow inspector applies.
func TestOverviewSummaryUsesSectionPriority(t *testing.T) {
	d := overviewData(overviewNow)
	m := New()
	m.Resize(400, 40)

	summary := sidepanel.StripANSI(m.Summary(d, 400))
	if summary == "" {
		t.Fatal("Summary() returned nothing for a populated snapshot")
	}
	if strings.Contains(summary, "\n") {
		t.Fatalf("Summary() is not a single line: %q", summary)
	}

	// context is priority 1, changed 2, repo 5. Their digests must appear in
	// that order, and each must be the section's real OneLine rather than a
	// truncation of its body.
	ctx := strings.Index(summary, "ctx 4k/128k")
	changed := strings.Index(summary, "± 6 files")
	repo := strings.Index(summary, "⎇ main")
	if ctx < 0 || changed < 0 || repo < 0 {
		t.Fatalf("Summary() is missing a section digest: %q", summary)
	}
	if !(ctx < changed && changed < repo) {
		t.Errorf("Summary() is not in priority order (context=%d changed=%d repo=%d): %q",
			ctx, changed, repo, summary)
	}
}

// Long content scrolls rather than collapsing sections away. The last
// section must be reachable, and the top of the document must not already
// contain it — otherwise the test would pass without scrolling at all.
func TestOverviewScrollsLongContent(t *testing.T) {
	d := overviewData(overviewNow)
	m := New()
	m.Resize(80, 10)
	m.SetData(d)

	top := sidepanel.StripANSI(m.View(d))
	if !strings.Contains(top, "CONTEXT") {
		t.Fatalf("the first section is not visible at the top:\n%s", top)
	}
	if strings.Contains(top, "42k in") {
		t.Fatalf("the last section is already visible at the top; the test proves nothing:\n%s", top)
	}

	max := m.maxScroll(d)
	if max <= 0 {
		t.Fatalf("maxScroll() = %d for content taller than the viewport", max)
	}

	m.SetState(TabOverview, TabState{Scroll: max})
	bottom := sidepanel.StripANSI(m.View(d))
	if bottom == top {
		t.Error("advancing Scroll did not change the rendered output")
	}
	if !strings.Contains(bottom, "42k in") {
		t.Errorf("the last section is not reachable by scrolling to maxScroll=%d:\n%s", max, bottom)
	}
}

// Rendering is a pure function of the snapshot: it must not write back into
// the caller's Hidden map, which would show up as a spurious config write.
func TestOverviewHiddenSectionsArePreserved(t *testing.T) {
	d := overviewData(overviewNow)
	d.Hidden = map[string]bool{"changed": true}
	before := map[string]bool{}
	for k, v := range d.Hidden {
		before[k] = v
	}

	m := New()
	m.Resize(120, 60)
	m.SetData(d)

	out := sidepanel.StripANSI(m.View(d))
	if strings.Contains(out, "CHANGED") {
		t.Errorf("a hidden section's header was rendered:\n%s", out)
	}
	if strings.Contains(out, "internal/app/tui/inspector/overview.go") {
		t.Errorf("a hidden section's rows were rendered:\n%s", out)
	}
	if strings.Contains(sidepanel.StripANSI(m.Summary(d, 400)), "± 6 files") {
		t.Error("the compact summary included a hidden section")
	}

	if len(d.Hidden) != len(before) {
		t.Fatalf("rendering changed the caller's Hidden map: %v, want %v", d.Hidden, before)
	}
	for k, v := range before {
		if got, ok := d.Hidden[k]; !ok || got != v {
			t.Fatalf("rendering changed the caller's Hidden map: %v, want %v", d.Hidden, before)
		}
	}
}

// A renderer that panics on a degenerate size takes the whole TUI down with
// it, and one that emits an over-wide line corrupts the frame it is joined
// into.
func TestOverviewNeverPanicsAtSillySizes(t *testing.T) {
	d := overviewData(overviewNow)
	sizes := []struct{ w, h int }{
		{0, 0}, {0, 24}, {80, 0}, {-5, 10}, {10, -3}, {1, 1}, {3, 2}, {2, 40}, {200, 60},
	}
	for _, s := range sizes {
		m := New()
		m.Resize(s.w, s.h)
		m.SetData(d)

		out := m.View(d) // must not panic
		if s.w <= 0 || s.h <= 0 {
			if out != "" {
				t.Errorf("%dx%d: View() = %q, want empty", s.w, s.h, out)
			}
			continue
		}
		lines := overviewLines(out)
		if len(lines) > s.h {
			t.Errorf("%dx%d: View() emitted %d rows, want <= %d", s.w, s.h, len(lines), s.h)
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > s.w {
				t.Errorf("%dx%d: row %d is %d cells wide, want <= %d: %q", s.w, s.h, i, w, s.w, line)
			}
		}
	}
}

// Scrolling is bounded by the content, and the bound is a function of the
// recorded size — so it must move when the size does, without the model
// having to be told.
func TestOverviewMaxScrollTracksSize(t *testing.T) {
	d := overviewData(overviewNow)
	m := New()

	m.Resize(80, 10)
	small := m.maxScroll(d)
	m.Resize(80, 60)
	large := m.maxScroll(d)

	if small <= 0 {
		t.Fatalf("maxScroll() at 80x10 = %d, want positive for content taller than the viewport", small)
	}
	if large != 0 {
		t.Errorf("maxScroll() at 80x60 = %d, want 0 when everything fits", large)
	}
	if small <= large {
		t.Errorf("maxScroll() did not shrink as the viewport grew: %d then %d", small, large)
	}
}
