package patch

import (
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

func TestParseRejectsUnclosedReplace(t *testing.T) {
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n=======\nworld\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unclosed REPLACE block, got nil")
	}
}

func TestParseRejectsEmptyPathChunk(t *testing.T) {
	input := "<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for chunk with empty path, got nil")
	}
}
