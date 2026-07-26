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

func TestNearestRegionFindsClosestWindow(t *testing.T) {
	content := `package main

import "fmt"

func hello() {
	fmt.Println("hello")
}

func goodbye() {
	fmt.Println("goodbye")
}
`
	search := `func hello() {
	fmt.Println("hello world")
}`

	region := NearestRegion(content, search, 4)
	if region == "" {
		t.Fatal("NearestRegion returned empty string")
	}
	if !strings.Contains(region, "hello") {
		t.Errorf("region should contain lines near the hello function, got: %q", region)
	}
	if !strings.Contains(region, "Println") {
		t.Errorf("region should contain Println, got: %q", region)
	}
}

func TestNearestRegionEmptyInputs(t *testing.T) {
	if got := NearestRegion("", "anything", 3); got != "" {
		t.Errorf("expected empty for empty content, got %q", got)
	}
	if got := NearestRegion("some content", "", 3); got != "" {
		t.Errorf("expected empty for empty search, got %q", got)
	}
}

func TestValidatePatchNotFoundIncludesNearestRegion(t *testing.T) {
	content := `package main

func hello() {
	println("hello")
}
`
	fp := FilePatch{
		Path: "main.go",
		Chunks: []PatchChunk{
			{
				Search:  `	println("goodbye")`,
				Replace: `	println("farewell")`,
			},
		},
	}

	ok, err := ValidatePatch(content, fp)
	if ok {
		t.Fatal("expected ValidatePatch to fail")
	}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "nearest region") {
		t.Fatalf("error should mention nearest region, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "hello") {
		t.Fatalf("error should contain content near the target, got: %s", errMsg)
	}
}

// Live evidence (Kimi's kimi-for-coding-highspeed against a table-driven Go
// test file): a search block spanning MORE lines than NearestRegion's
// hardcoded 5-line window failed to patch 4 times in a row, each retry
// shown the identical truncated hint -- because the window can only ever
// show 5 lines, it can never contain the full multi-line block the model
// is trying to match exactly, so every retry has the same incomplete
// information as the last. The window must scale with the search block's
// own size so the model can see the ENTIRE region it needs to reconstruct.
func TestValidatePatchNearestRegionCoversWholeMultiLineSearchBlock(t *testing.T) {
	content := `package tablecheck

func run(tests []Case) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.input)
			assertLen(t, got, tt.maxLen)
			if maxLen := tt.maxLen; maxLen > 0 {
				checkBounds(t, got, maxLen)
			}
		})
	}
}
`
	// This search block differs from the real content only in the
	// "checkBounds" line (a plausible near-miss), but spans 8 lines --
	// more than the old hardcoded windowLines=5.
	fp := FilePatch{
		Path: "tablecheck_test.go",
		Chunks: []PatchChunk{
			{
				Search: `		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.input)
			assertLen(t, got, tt.maxLen)
			if maxLen := tt.maxLen; maxLen > 0 {
				verifyBounds(t, got, maxLen)
			}
		})`,
				Replace: "// replaced",
			},
		},
	}

	ok, err := ValidatePatch(content, fp)
	if ok {
		t.Fatal("expected ValidatePatch to fail (search block intentionally mismatched)")
	}
	errMsg := err.Error()
	for _, want := range []string{"t.Run(tt.name", "assertLen", "checkBounds", "})"} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("nearest region hint missing %q -- window too small to show the full block\n%s", want, errMsg)
		}
	}
}
