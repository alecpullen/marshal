package sdd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// requiredContractSections are the H2 headings a contract must include
// (spec §4). Order is the canonical write order.
var requiredContractSections = []string{
	"Knowledge Bundle",
	"Task",
	"Allowed Files",
	"Allowed Symbols",
	"Acceptance Criteria",
	"Constraints",
	"Code Examples",
}

// optionalContractSections are H2 headings the contract MAY include.
var optionalContractSections = []string{
	"Dependencies",
	"Detailed Instructions",
}

// allContractSections returns required + optional in canonical order.
func allContractSections() []string {
	out := make([]string, 0, len(requiredContractSections)+len(optionalContractSections))
	out = append(out, requiredContractSections...)
	out = append(out, optionalContractSections...)
	return out
}

// Contract is one task's contract extracted from .marshal/sdd/contracts/<T>.md.
// Fields mirror the H2 section titles in spec §4. A field is empty if its
// section is absent from the source file (Validate catches that for the
// required sections).
type Contract struct {
	TaskID string

	KnowledgeBundle string
	Task            string
	AllowedFiles    string
	AllowedSymbols  string
	Acceptance      string
	Constraints     string
	CodeExamples    string

	Dependencies         string
	DetailedInstructions string
}

// ExtractContract reads <ws.Dir()>/contracts/<taskID>.md and parses it into
// a Contract. Section bodies are trimmed of leading/trailing blank lines.
// Returns an error if the file does not exist.
func ExtractContract(ws *Workspace, _ *DAG, taskID string) (*Contract, error) {
	path := filepath.Join(ws.Dir(), "contracts", taskID+".md")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sdd contract: open %s: %w", path, err)
	}
	defer f.Close()

	c := &Contract{TaskID: taskID}
	current := ""
	// fieldSetter is a small dispatch: given a section title, returns a
	// pointer to the matching struct field.
	fieldFor := func(title string) *string {
		switch title {
		case "Knowledge Bundle":
			return &c.KnowledgeBundle
		case "Task":
			return &c.Task
		case "Allowed Files":
			return &c.AllowedFiles
		case "Allowed Symbols":
			return &c.AllowedSymbols
		case "Acceptance Criteria":
			return &c.Acceptance
		case "Constraints":
			return &c.Constraints
		case "Code Examples":
			return &c.CodeExamples
		case "Dependencies":
			return &c.Dependencies
		case "Detailed Instructions":
			return &c.DetailedInstructions
		default:
			return nil
		}
	}
	setSection := func() {
		if current == "" {
			return
		}
		if f := fieldFor(current); f != nil {
			*f = strings.TrimRight(*f, "\n")
		}
		current = ""
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			// New section header.
			setSection() // finalize the previous section's body collection
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if current == "" {
			continue
		}
		f := fieldFor(current)
		if f == nil {
			continue
		}
		// Skip leading blank lines so section bodies are clean.
		if *f == "" && strings.TrimSpace(line) == "" {
			continue
		}
		// First content line; subsequent lines append with newline separator.
		if *f == "" {
			*f = line
		} else {
			*f += "\n" + line
		}
	}
	setSection()
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sdd contract: scan: %w", err)
	}
	return c, nil
}

// Write serializes the contract as Markdown to
// <ws.Dir()>/contracts/<TaskID>.md. Sections are emitted in canonical order.
// Optional sections are skipped when empty.
func (c *Contract) Write(ws *Workspace) error {
	if _, err := ws.Ensure(); err != nil {
		return err
	}
	dir := filepath.Join(ws.Dir(), "contracts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("sdd contract: mkdir: %w", err)
	}
	path := filepath.Join(dir, c.TaskID+".md")

	var b strings.Builder
	for _, title := range allContractSections() {
		f := func(title string) string {
			switch title {
			case "Knowledge Bundle":
				return c.KnowledgeBundle
			case "Task":
				return c.Task
			case "Allowed Files":
				return c.AllowedFiles
			case "Allowed Symbols":
				return c.AllowedSymbols
			case "Acceptance Criteria":
				return c.Acceptance
			case "Constraints":
				return c.Constraints
			case "Code Examples":
				return c.CodeExamples
			case "Dependencies":
				return c.Dependencies
			case "Detailed Instructions":
				return c.DetailedInstructions
			}
			return ""
		}(title)
		if f == "" && !isRequired(title) {
			continue
		}
		b.WriteString("## ")
		b.WriteString(title)
		b.WriteString("\n\n")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("sdd contract: write: %w", err)
	}
	return nil
}

func isRequired(title string) bool {
	for _, r := range requiredContractSections {
		if r == title {
			return true
		}
	}
	return false
}

// ValidationError indicates a contract failed Validate. It carries a
// machine-parseable Field name so the controller (P4) can format a
// structured report.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("sdd contract: invalid %s: %s", e.Field, e.Reason)
}

// Validate checks that all required sections are present and that every
// file path mentioned in `## Allowed Files` is covered by `allowedFilesGlob`
// (a comma-separated list of file paths the contract may touch — typically
// the task's DAGTask.Files joined). Returns *ValidationError on failure,
// or a wrapped IO error if the contract cannot be re-read.
func (c *Contract) Validate(allowedFilesGlob string) error {
	required := map[string]string{
		"Knowledge Bundle":    c.KnowledgeBundle,
		"Task":                c.Task,
		"Allowed Files":       c.AllowedFiles,
		"Allowed Symbols":     c.AllowedSymbols,
		"Acceptance Criteria": c.Acceptance,
		"Constraints":         c.Constraints,
		"Code Examples":       c.CodeExamples,
	}
	for title, body := range required {
		if strings.TrimSpace(body) == "" {
			return &ValidationError{Field: title, Reason: "required section missing or empty"}
		}
	}
	// Allowed-files check: every file path mentioned in c.AllowedFiles must
	// appear in allowedFilesGlob.
	allowed := strings.Split(allowedFilesGlob, ",")
	allowedSet := make(map[string]bool, len(allowed))
	for _, p := range allowed {
		allowedSet[strings.TrimSpace(p)] = true
	}
	for _, line := range strings.Split(c.AllowedFiles, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Lines may be bullet-prefixed ("- file.go"); strip common prefixes.
		trimmed = strings.TrimLeft(trimmed, "-* \t")
		if trimmed == "" {
			continue
		}
		if !allowedSet[trimmed] {
			return &ValidationError{Field: "Allowed Files", Reason: fmt.Sprintf("file %q not in allowed set", trimmed)}
		}
	}
	return nil
}

// writeFile is a small test helper (kept here so the test file doesn't have
// to import os).
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
