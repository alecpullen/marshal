package patch

import (
	"reflect"
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
