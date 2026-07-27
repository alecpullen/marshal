package plugins

import (
	"path/filepath"
	"testing"
)

const testCommandMD = "+++\nname = \"review\"\ndescription = \"Review the diff\"\n+++\n\nReview the current diff for bugs.\n"

// writePluginCommand writes commands/<file> under dir.
func writePluginCommand(t *testing.T, dir, file, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "commands", file), content)
}

func TestParseCommandSpecsFindsCommands(t *testing.T) {
	dir := t.TempDir()
	writePluginCommand(t, dir, "review.md", testCommandMD)

	specs, err := parseCommandSpecs(dir)
	if err != nil {
		t.Fatalf("parseCommandSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs length = %d, want 1", len(specs))
	}
	if specs[0].Name != "review" || specs[0].Description != "Review the diff" {
		t.Fatalf("spec = %+v", specs[0])
	}
	if specs[0].Body != "Review the current diff for bugs.\n" {
		t.Fatalf("Body = %q", specs[0].Body)
	}
}

func TestParseCommandSpecsNoDir(t *testing.T) {
	specs, err := parseCommandSpecs(t.TempDir())
	if err != nil {
		t.Fatalf("missing commands dir should not error: %v", err)
	}
	if specs != nil {
		t.Fatalf("specs = %v, want nil", specs)
	}
}

func TestParseCommandSpecsNameLowercased(t *testing.T) {
	dir := t.TempDir()
	writePluginCommand(t, dir, "up.md", "+++\nname = \"Review\"\ndescription = \"d\"\n+++\n\nBody.\n")

	specs, err := parseCommandSpecs(dir)
	if err != nil {
		t.Fatalf("parseCommandSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "review" {
		t.Fatalf("specs = %+v, want name lowercased to review", specs)
	}
}

func TestParseCommandSpecsSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	writePluginCommand(t, dir, "good.md", testCommandMD)
	writePluginCommand(t, dir, "no-frontmatter.md", "# just markdown\n")
	writePluginCommand(t, dir, "bad-name.md", "+++\nname = \"has space\"\ndescription = \"d\"\n+++\n\nBody.\n")
	writePluginCommand(t, dir, "empty-body.md", "+++\nname = \"empty\"\ndescription = \"d\"\n+++\n")
	writePluginCommand(t, dir, "notes.txt", testCommandMD) // wrong extension

	specs, err := parseCommandSpecs(dir)
	if err != nil {
		t.Fatalf("parseCommandSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "review" {
		t.Fatalf("specs = %+v, want only [review]", specs)
	}
}
