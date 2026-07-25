package sdd

import (
	"strings"
	"testing"
)

func TestExtractContractMissingFile(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	_, err := ExtractContract(ws, nil, "T1")
	if err == nil {
		t.Fatal("expected error for missing contract file, got nil")
	}
}

func TestContractExtractAndValidateRoundTrip(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	// Write a contract file with all 7 required sections.
	body := `## Knowledge Bundle

some knowledge

## Task

do the thing

## Allowed Files

- internal/foo.go
- internal/bar.go

## Allowed Symbols

Foo, Bar

## Acceptance Criteria

go test ./...

## Constraints

no global state

## Code Examples

Foo()

## Detailed Instructions

step 1
`
	if err := writeFile(ws.Dir()+"/contracts/T1.md", body); err != nil {
		t.Fatal(err)
	}
	c, err := ExtractContract(ws, nil, "T1")
	if err != nil {
		t.Fatalf("ExtractContract: %v", err)
	}
	if c.KnowledgeBundle != "some knowledge" {
		t.Errorf("KnowledgeBundle = %q", c.KnowledgeBundle)
	}
	if c.Task != "do the thing" {
		t.Errorf("Task = %q", c.Task)
	}
	if !strings.Contains(c.AllowedFiles, "internal/foo.go") {
		t.Errorf("AllowedFiles = %q", c.AllowedFiles)
	}
	if err := c.Validate("internal/foo.go,internal/bar.go"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestContractValidateCatchesMissingSection(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	body := `## Knowledge Bundle

x

## Task

y

## Allowed Files

- a.go

## Allowed Symbols

z

## Acceptance Criteria

go test

## Constraints

none
`
	if err := writeFile(ws.Dir()+"/contracts/T1.md", body); err != nil {
		t.Fatal(err)
	}
	c, err := ExtractContract(ws, nil, "T1")
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate("a.go")
	if err == nil {
		t.Fatal("expected error for missing Code Examples section")
	}
	if !strings.Contains(err.Error(), "Code Examples") {
		t.Errorf("error should mention Code Examples, got: %v", err)
	}
}

func TestContractValidateCatchesAllowedFilesViolation(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	body := `## Knowledge Bundle

x

## Task

y

## Allowed Files

- a.go
- b.go

## Allowed Symbols

z

## Acceptance Criteria

go test

## Constraints

none

## Code Examples

f()
`
	if err := writeFile(ws.Dir()+"/contracts/T1.md", body); err != nil {
		t.Fatal(err)
	}
	c, _ := ExtractContract(ws, nil, "T1")
	// glob only covers a.go; b.go is in the contract but not the allowed glob.
	if err := c.Validate("a.go"); err == nil {
		t.Fatal("expected error for allowed-files violation")
	}
}

func TestContractWriteAndReextract(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	c := &Contract{
		TaskID:               "T1",
		KnowledgeBundle:      "kb",
		Task:                 "t",
		AllowedFiles:         "x.go",
		AllowedSymbols:       "X",
		Acceptance:           "go test",
		Constraints:          "c",
		CodeExamples:         "X()",
		Dependencies:         "",
		DetailedInstructions: "",
	}
	if err := c.Write(ws); err != nil {
		t.Fatal(err)
	}
	got, err := ExtractContract(ws, nil, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "T1" || got.KnowledgeBundle != "kb" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
