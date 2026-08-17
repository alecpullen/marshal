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
