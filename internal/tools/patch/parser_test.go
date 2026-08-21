package patch

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestParsePatches(t *testing.T) {
	input := `
Some message from the model.

File: internal/app/config/config.go
<<<<<<< SEARCH
type Config struct {
	Project ProjectConfig
}
=======
type Config struct {
	Project ProjectConfig
	Tools   ToolsConfig
}
>>>>>>> REPLACE

Another file change.

File: main.go
<<<<<<< SEARCH
func main() {
	println("hello")
}
=======
func main() {
	println("world")
}
>>>>>>> REPLACE
`
	want := []FilePatch{
		{
			Path: "internal/app/config/config.go",
			Chunks: []PatchChunk{
				{
					Search:  "type Config struct {\n\tProject ProjectConfig\n}",
					Replace: "type Config struct {\n\tProject ProjectConfig\n\tTools   ToolsConfig\n}",
				},
			},
		},
		{
			Path: "main.go",
			Chunks: []PatchChunk{
				{
					Search:  "func main() {\n\tprintln(\"hello\")\n}",
					Replace: "func main() {\n\tprintln(\"world\")\n}",
				},
			},
		},
	}

	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %#v, want %#v", got, want)
	}
}

func TestParseRejectsUnclosedSearch(t *testing.T) {
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unclosed SEARCH block, got nil")
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Fatalf("error should mention unclosed block: %v", err)
	}
}

func TestParseRepairsReplaceClosedByEOF(t *testing.T) {
	// EOF is an unambiguous terminator when a replacement is present. This
	// used to be a hard error, so nothing that previously parsed changes.
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n=======\nworld\n"
	res, err := ParseRepairing(input)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 1 || len(res.Patches[0].Chunks) != 1 {
		t.Fatalf("patches = %+v, want one chunk", res.Patches)
	}
	if got := res.Patches[0].Chunks[0].Replace; got != "world" {
		t.Errorf("Replace = %q, want %q", got, "world")
	}
	if len(res.Repairs) != 1 {
		t.Errorf("Repairs = %v, want exactly one note", res.Repairs)
	}
}

// TestParseRepairsDividerUsedAsTerminator reproduces the exact failure from
// session 1786111806174657000: every block was closed with "=======" instead
// of ">>>>>>> REPLACE". The stray divider used to land in the replace buffer
// as content and then the next File: line errored.
func TestParseRepairsDividerUsedAsTerminator(t *testing.T) {
	input := strings.Join([]string{
		"File: a.go",
		"<<<<<<< SEARCH",
		"old a",
		"=======",
		"new a",
		"=======",
		"File: b.go",
		"<<<<<<< SEARCH",
		"old b",
		"=======",
		"new b",
		"=======",
	}, "\n")
	res, err := ParseRepairing(input)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Patches) != 2 {
		t.Fatalf("patches = %+v, want 2 files", res.Patches)
	}
	for i, want := range []struct{ path, replace string }{{"a.go", "new a"}, {"b.go", "new b"}} {
		if res.Patches[i].Path != want.path {
			t.Errorf("patch %d path = %q, want %q", i, res.Patches[i].Path, want.path)
		}
		if got := res.Patches[i].Chunks[0].Replace; got != want.replace {
			t.Errorf("patch %d Replace = %q, want %q (stray divider must not survive)", i, got, want.replace)
		}
	}
	if len(res.Repairs) != 2 {
		t.Errorf("Repairs = %v, want one note per repaired block", res.Repairs)
	}
}

func TestParseStillRejectsEmptyReplaceAtEOF(t *testing.T) {
	// Truncation and "delete these lines" are indistinguishable here, and
	// guessing deletes code. Must stay an error.
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n=======\n"
	if _, err := ParseRepairing(input); err == nil {
		t.Fatal("expected error for an empty REPLACE block at EOF, got nil")
	}
}

func TestParseCleanInputReportsNoRepairs(t *testing.T) {
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"
	res, err := ParseRepairing(input)
	if err != nil {
		t.Fatalf("ParseRepairing: %v", err)
	}
	if len(res.Repairs) != 0 {
		t.Errorf("Repairs = %v, want none for a well-formed proposal", res.Repairs)
	}
}

func TestParseRejectsEmptyPathChunk(t *testing.T) {
	input := "<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for chunk with empty path, got nil")
	}
}

func TestCommitChunkLogsDroppedChunk(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(slog.Default()) })

	// A chunk with no File: header — the chunk should be silently dropped
	// but a warning should be logged.
	input := "<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"
	_, err := Parse(input)
	// Parse returns an error for empty-path chunks (existing behavior).
	// The test only checks that a warning was logged.
	_ = err // error is expected

	logOutput := buf.String()
	if !strings.Contains(logOutput, "dropped") || !strings.Contains(logOutput, "chunk") {
		t.Errorf("expected slog warning about dropped chunk, got: %s", logOutput)
	}
}
