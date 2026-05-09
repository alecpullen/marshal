package protocol

import (
	"testing"
	"time"
)

func TestEntryKind_IsValid(t *testing.T) {
	tests := []struct {
		kind  EntryKind
		valid bool
	}{
		{EntryKindFile, true},
		{EntryKindDirectory, true},
		{EntryKindSymbol, true},
		{EntryKindOutput, true},
		{EntryKindPlan, true},
		{EntryKindSearchResult, true},
		{EntryKindUserInput, true},
		{EntryKindSummary, true},
		{EntryKindTestResult, true},
		{EntryKind("unknown"), false},
		{EntryKind(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			got := tt.kind.IsValid()
			if got != tt.valid {
				t.Errorf("IsValid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestParseContextRef_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  ContextRef
	}{
		{
			input: "files/test.go@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			want:  "files/test.go@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			input: "symbols/MyStruct@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			want:  "symbols/MyStruct@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		},
		{
			input: "outputs/result@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want:  "outputs/result@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			input: "summaries/project@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			want:  "summaries/project@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseContextRef(tt.input)
			if err != nil {
				t.Fatalf("ParseContextRef() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseContextRef() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseContextRef_Invalid(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"test.go"},                                    // Missing sha256 prefix
		{"files/test.go@sha256:abc"},                   // Hash too short
		{"files/test.go@sha256:xyz"},                   // Invalid hex
		{"files/test.go@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855@extra"}, // Extra
		{"test.go@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},             // Missing kind
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseContextRef(tt.input)
			if err == nil {
				t.Error("ParseContextRef() expected error, got nil")
			}
		})
	}
}

func TestNewContextRef(t *testing.T) {
	content := []byte("package main\n\nfunc main() {}")
	ref := NewContextRef(EntryKindFile, "cmd/main.go", content)

	if !ref.IsValid() {
		t.Error("NewContextRef() produced invalid reference")
	}

	if ref.Kind() != EntryKindFile {
		t.Errorf("Kind() = %v, want %v", ref.Kind(), EntryKindFile)
	}

	if ref.Path() != "cmd/main.go" {
		t.Errorf("Path() = %v, want %v", ref.Path(), "cmd/main.go")
	}

	if ref.Hash() == "" {
		t.Error("Hash() should not be empty")
	}

	// Verify the hash is correct
	if !ref.MatchesContent(content) {
		t.Error("MatchesContent() should be true for original content")
	}

	// Verify it doesn't match different content
	if ref.MatchesContent([]byte("different")) {
		t.Error("MatchesContent() should be false for different content")
	}
}

func TestContextRef_Extractors(t *testing.T) {
	ref := ContextRef("files/internal/test.go@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	if got := ref.Kind(); got != EntryKindFile {
		t.Errorf("Kind() = %v, want %v", got, EntryKindFile)
	}

	if got := ref.Path(); got != "internal/test.go" {
		t.Errorf("Path() = %v, want %v", got, "internal/test.go")
	}

	if got := ref.Hash(); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("Hash() = %v, want hash string", got)
	}
}

func TestContextRef_Extractors_Symbol(t *testing.T) {
	ref := ContextRef("symbols/MyPackage.MyType@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	if got := ref.Kind(); got != EntryKindSymbol {
		t.Errorf("Kind() = %v, want %v", got, EntryKindSymbol)
	}

	if got := ref.Path(); got != "MyPackage.MyType" {
		t.Errorf("Path() = %v, want %v", got, "MyPackage.MyType")
	}
}

func TestNewEntryKey(t *testing.T) {
	key := NewEntryKey(EntryKindSymbol, "MyPackage.MyType")

	if got := key.Kind(); got != EntryKindSymbol {
		t.Errorf("Kind() = %v, want %v", got, EntryKindSymbol)
	}

	if got := key.Path(); got != "MyPackage.MyType" {
		t.Errorf("Path() = %v, want %v", got, "MyPackage.MyType")
	}

	if got := key.String(); got != "symbols/MyPackage.MyType" {
		t.Errorf("String() = %v, want %v", got, "symbols/MyPackage.MyType")
	}
}

func TestNewEntryKey_Files(t *testing.T) {
	key := NewEntryKey(EntryKindFile, "internal/test.go")

	if got := key.Kind(); got != EntryKindFile {
		t.Errorf("Kind() = %v, want %v", got, EntryKindFile)
	}

	if got := key.Path(); got != "internal/test.go" {
		t.Errorf("Path() = %v, want %v", got, "internal/test.go")
	}

	if got := key.String(); got != "files/internal/test.go" {
		t.Errorf("String() = %v, want %v", got, "files/internal/test.go")
	}
}

func TestContextEntry_IsSuperseded(t *testing.T) {
	entry := ContextEntry{
		Ref: "files/test.go@sha256:abc123",
		Metadata: EntryMetadata{
			SupersededBy: "",
		},
	}

	if entry.IsSuperseded() {
		t.Error("IsSuperseded() = true, want false for entry without superseder")
	}

	entry.Metadata.SupersededBy = "files/test.go@sha256:def456"
	if !entry.IsSuperseded() {
		t.Error("IsSuperseded() = false, want true for entry with superseder")
	}
}

func TestContextEntry_Supersede(t *testing.T) {
	oldContent := []byte("old content")
	newContent := []byte("new content")

	entry := ContextEntry{
		Ref:         NewContextRef(EntryKindFile, "test.go", oldContent),
		Key:         NewEntryKey(EntryKindFile, "test.go"),
		Kind:        EntryKindFile,
		Size:        len(oldContent),
		ContentHash: ComputeHash(oldContent),
		ProducedBy:  "agent_1",
		ProducedAt:  time.Now().Add(-time.Hour),
		Metadata: EntryMetadata{
			Tags:     []string{"src"},
			Source:   "read_file",
			Language: "go",
		},
	}

	newEntry := entry.Supersede(newContent)

	if newEntry.Ref == entry.Ref {
		t.Error("Supersede() should create a new reference")
	}

	if newEntry.Key != entry.Key {
		t.Error("Supersede() should preserve the key")
	}

	if newEntry.Kind != entry.Kind {
		t.Error("Supersede() should preserve the kind")
	}

	if newEntry.Size != len(newContent) {
		t.Errorf("Supersede() Size = %v, want %v", newEntry.Size, len(newContent))
	}

	if newEntry.ProducedBy != entry.ProducedBy {
		t.Error("Supersede() should preserve ProducedBy")
	}

	if !newEntry.ProducedAt.After(entry.ProducedAt) {
		t.Error("Supersede() should have later ProducedAt")
	}

	// Verify the new reference matches the new content
	if !newEntry.Ref.MatchesContent(newContent) {
		t.Error("Supersede() should create reference matching new content")
	}
}

func TestComputeHash(t *testing.T) {
	content := []byte("test content")
	hash := ComputeHash(content)

	if len(hash) != 64 {
		t.Errorf("ComputeHash() length = %v, want 64", len(hash))
	}

	// Same content should produce same hash
	hash2 := ComputeHash(content)
	if hash != hash2 {
		t.Error("ComputeHash() should be deterministic")
	}

	// Different content should produce different hash
	differentContent := []byte("different content")
	hash3 := ComputeHash(differentContent)
	if hash == hash3 {
		t.Error("ComputeHash() should produce different hashes for different content")
	}
}

func TestContentMatchesHash(t *testing.T) {
	content := []byte("test content")
	hash := ComputeHash(content)

	if !ContentMatchesHash(content, hash) {
		t.Error("ContentMatchesHash() should return true for matching content")
	}

	if ContentMatchesHash([]byte("different"), hash) {
		t.Error("ContentMatchesHash() should return false for different content")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		content []byte
		want    int
	}{
		{[]byte(""), 0},
		{[]byte("test"), 1},        // 4 chars / 4 = 1
		{[]byte("hello world"), 2}, // 11 chars / 4 = 2 (integer division)
		{make([]byte, 100), 25},    // 100 chars / 4 = 25
	}

	for _, tt := range tests {
		t.Run(string(tt.content), func(t *testing.T) {
			got := EstimateTokens(tt.content)
			if got != tt.want {
				t.Errorf("EstimateTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}
