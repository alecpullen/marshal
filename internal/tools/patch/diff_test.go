package patch

import (
	"strings"
	"testing"
)

func TestApplyAndDiff(t *testing.T) {
	fileContent := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`

	patch := FilePatch{
		Path: "main.go",
		Chunks: []PatchChunk{
			{
				Search:  `	fmt.Println("hello")`,
				Replace: `	fmt.Println("patched")`,
			},
		},
	}

	// 1. Dry run/Validation
	ok, err := ValidatePatch(fileContent, patch)
	if !ok || err != nil {
		t.Fatalf("ValidatePatch failed: %v", err)
	}

	// 2. Generate Diff
	diff, err := GenerateDiff("main.go", fileContent, patch)
	if err != nil {
		t.Fatalf("GenerateDiff error: %v", err)
	}

	if !strings.Contains(diff, "-	fmt.Println(\"hello\")") {
		t.Errorf("Diff missing deleted lines: %s", diff)
	}
	if !strings.Contains(diff, "+	fmt.Println(\"patched\")") {
		t.Errorf("Diff missing added lines: %s", diff)
	}
}

func TestMultiChunkApplyAndDiff(t *testing.T) {
	fileContent := `line 1
line 2
line 3
line 4
line 5
`

	patch := FilePatch{
		Path: "test.txt",
		Chunks: []PatchChunk{
			{
				Search:  "line 2",
				Replace: "line 2 modified\nline 2.5 inserted",
			},
			{
				Search:  "line 4",
				Replace: "line 4 modified",
			},
		},
	}

	ok, err := ValidatePatch(fileContent, patch)
	if !ok || err != nil {
		t.Fatalf("ValidatePatch failed: %v", err)
	}

	diff, err := GenerateDiff("test.txt", fileContent, patch)
	if err != nil {
		t.Fatalf("GenerateDiff failed: %v", err)
	}

	// Verify the line count shifts are correctly mapped in unified diff header
	if !strings.Contains(diff, "@@ -1,5 +1,6 @@") {
		t.Errorf("Diff missing or incorrect first header: %s", diff)
	}
	if !strings.Contains(diff, "@@ -1,6 +2,6 @@") {
		t.Errorf("Diff missing or incorrect second header: %s", diff)
	}
}
