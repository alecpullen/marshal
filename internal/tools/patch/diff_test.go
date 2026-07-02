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
