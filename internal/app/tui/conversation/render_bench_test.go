package conversation

import (
	"strings"
	"testing"
)

// benchSources are the shapes the benchmarks measure: the two that dominate a
// real transcript (prose and fenced code, both long enough to wrap) plus a table,
// whose projection does more work per byte than a paragraph does.
var benchSources = map[string]string{
	"Prose": strings.Repeat("A paragraph of ordinary prose with a [link](https://example.com) "+
		"and some `inline code` and an *emphasis* in it. ", 20),
	"Code": "```go\n" + strings.Repeat(
		"func handler(w http.ResponseWriter, r *http.Request) error {\n"+
			"\treturn process(r.Context(), r.URL.Query().Get(\"id\"))\n"+
			"}\n\n", 20) + "```",
	"Table": "| repository | stars | language |\n| --- | --- | --- |\n" +
		strings.Repeat("| owner/name | 1234 | Go |\n", 40),
}

// BenchmarkProject measures the Markdown projection alone: parsing, the AST walk
// and span emission, with no layout and no styling.
//
// It exists to answer "does the mapped path cost more than the glamour path it
// replaces" for the part that is new. A number recorded here is the baseline to
// compare against after a change, not a threshold — this hardware is not CI's, so
// a pass/fail bound would be measuring the machine.
func BenchmarkProject(b *testing.B) {
	for name, src := range benchSources {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sp := ProjectMarkdown(src, MarkdownOptions{})
				if sp.Text == "" {
					b.Fatal("empty projection")
				}
			}
		})
	}
}

// BenchmarkRender measures projection plus layout at a realistic transcript
// width: everything a caller does to put one block on screen for the first time.
func BenchmarkRender(b *testing.B) {
	for name, src := range benchSources {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sp := ProjectMarkdown(src, MarkdownOptions{})
				rows := Layout(sp, LayoutOptions{Width: 100, Breakpoints: "/-:"})
				if len(rows) == 0 {
					b.Fatal("no rows")
				}
			}
		})
	}
}

// BenchmarkReflow measures laying out an ALREADY-PROJECTED block at a new width,
// which is what a terminal resize does to every visible block.
//
// This is the number that matters for resizing, and it is the reason the
// projection and the layout are separate steps: a reflow must not reparse the
// Markdown, because the parser's output does not depend on the width. A single
// "render at this width" API would reparse every block on every resize, and a
// large session resizes many times.
func BenchmarkReflow(b *testing.B) {
	for name, src := range benchSources {
		b.Run(name, func(b *testing.B) {
			sp := ProjectMarkdown(src, MarkdownOptions{})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows := Layout(sp, LayoutOptions{Width: 60 + i%80, Breakpoints: "/-:"})
				if len(rows) == 0 {
					b.Fatal("no rows")
				}
			}
		})
	}
}

// BenchmarkCellMapping measures the hit-test path, which runs on every pointer
// motion during a drag (Task 12). A drag is hundreds of events per second, so
// this is the one place where a per-call allocation would be felt.
func BenchmarkCellMapping(b *testing.B) {
	src := benchSources["Prose"]
	sp := ProjectMarkdown(src, MarkdownOptions{})
	block := RenderedBlock{
		BlockID: "msg:1",
		Logical: sp.Text,
		Width:   100,
		Rows:    Layout(sp, LayoutOptions{Width: 100, Breakpoints: "/-:"}),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		row := block.Rows[i%len(block.Rows)]
		block.OffsetAt(i%len(block.Rows), i%max(row.Cells, 1))
	}
}
