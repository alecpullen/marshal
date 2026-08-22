package db

import (
	"math"
	"strings"
	"testing"
)

func mustCreateProject(t *testing.T, database *DB, path string) int64 {
	t.Helper()
	id, err := database.GetOrCreateProject(path, path)
	if err != nil {
		t.Fatalf("GetOrCreateProject(%q): %v", path, err)
	}
	return id
}

func TestDecodeVectorRejectsNonMultipleOf4(t *testing.T) {
	// 5 bytes is not a valid float32 blob (must be multiple of 4).
	_, err := decodeVector([]byte{1, 2, 3, 4, 5}, 1)
	if err == nil {
		t.Fatal("expected error for non-multiple-of-4 blob length, got nil")
	}
	if !strings.Contains(err.Error(), "not a multiple") {
		t.Fatalf("expected 'not a multiple' error, got: %v", err)
	}
}

func TestChunkCRUD(t *testing.T) {
	database := newTestDB(t)
	projectID := mustCreateProject(t, database, "/tmp/proj")

	cwv := []ChunkWithVector{{
		Chunk: Chunk{FilePath: "a.go", FileHash: "h1", Kind: "code", SymbolName: "Foo", StartLine: 1, EndLine: 3, Content: "x", TokenCount: 1},
		Model: "nomic", Dim: 2, Vector: []float32{0.1, 0.2},
	}}
	if err := database.ReplaceFileChunks(projectID, "a.go", "h1", cwv); err != nil {
		t.Fatalf("ReplaceFileChunks: %v", err)
	}

	states, err := database.ChunkedFiles(projectID)
	if err != nil || states["a.go"].FileHash != "h1" || states["a.go"].Model != "nomic" {
		t.Fatalf("ChunkedFiles = %#v err=%v", states, err)
	}

	count, _, err := database.ChunkGeneration(projectID)
	if err != nil || count != 1 {
		t.Fatalf("ChunkGeneration count=%d err=%v", count, err)
	}

	rows, err := database.LoadVectors(projectID, "nomic")
	if err != nil || len(rows) != 1 || rows[0].FilePath != "a.go" || len(rows[0].Vector) != 2 {
		t.Fatalf("LoadVectors = %#v err=%v", rows, err)
	}

	if err := database.DeleteFileChunks(projectID, "a.go"); err != nil {
		t.Fatalf("DeleteFileChunks: %v", err)
	}
	states, _ = database.ChunkedFiles(projectID)
	if len(states) != 0 {
		t.Fatalf("after delete states=%#v", states)
	}
}

func TestVectorCodecRoundTrip(t *testing.T) {
	cases := [][]float32{
		{},
		{0},
		{1.5, -2.25, 0, 3.14159},
		{float32(math.MaxFloat32), float32(-math.MaxFloat32)},
	}
	for _, want := range cases {
		got, err := decodeVector(encodeVector(want), len(want))
		if err != nil {
			t.Fatalf("decodeVector: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("len = %d, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("v[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	}
}

func TestDecodeVectorRejectsWrongLength(t *testing.T) {
	// Encode a 4-element vector, then try to decode with dims=2.
	encoded := encodeVector([]float32{1.0, 2.0, 3.0, 4.0})
	_, err := decodeVector(encoded, 4)
	if err != nil {
		t.Fatalf("decodeVector with correct dims: %v", err)
	}

	_, err = decodeVector(encoded, 2)
	if err == nil {
		t.Fatal("expected error for wrong dims, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got: %v", err)
	}
}

func TestLoadVectorsSince(t *testing.T) {
	database := newTestDB(t)
	projectID := mustCreateProject(t, database, "/tmp/proj")

	// Insert first file's chunks.
	if err := database.ReplaceFileChunks(projectID, "a.go", "h1", []ChunkWithVector{{
		Chunk: Chunk{FilePath: "a.go", FileHash: "h1", Kind: "code", StartLine: 1, EndLine: 1, Content: "a", TokenCount: 1},
		Model: "nomic", Dim: 2, Vector: []float32{0.1, 0.2},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks a.go: %v", err)
	}

	// All vectors since 0 should return 1 row.
	rows, err := database.LoadVectorsSince(projectID, "nomic", 0)
	if err != nil {
		t.Fatalf("LoadVectorsSince 0: %v", err)
	}
	if len(rows) != 1 || rows[0].FilePath != "a.go" {
		t.Fatalf("LoadVectorsSince 0 = %#v err=%v", rows, err)
	}

	// Record the max chunk ID.
	_, maxID, err := database.ChunkGeneration(projectID)
	if err != nil {
		t.Fatalf("ChunkGeneration: %v", err)
	}

	// Insert second file's chunks.
	if err := database.ReplaceFileChunks(projectID, "b.go", "h2", []ChunkWithVector{{
		Chunk: Chunk{FilePath: "b.go", FileHash: "h2", Kind: "code", StartLine: 1, EndLine: 1, Content: "b", TokenCount: 1},
		Model: "nomic", Dim: 2, Vector: []float32{0.3, 0.4},
	}}); err != nil {
		t.Fatalf("ReplaceFileChunks b.go: %v", err)
	}

	// LoadVectorsSince maxID should return only the new b.go row.
	rows, err = database.LoadVectorsSince(projectID, "nomic", maxID)
	if err != nil {
		t.Fatalf("LoadVectorsSince maxID: %v", err)
	}
	if len(rows) != 1 || rows[0].FilePath != "b.go" {
		t.Fatalf("LoadVectorsSince maxID = %#v err=%v, want 1 row for b.go", rows, err)
	}

	// LoadVectorsSince with a very high ID should return nothing.
	rows, err = database.LoadVectorsSince(projectID, "nomic", 999999)
	if err != nil {
		t.Fatalf("LoadVectorsSince 999999: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("LoadVectorsSince 999999 = %d rows, want 0", len(rows))
	}
}
