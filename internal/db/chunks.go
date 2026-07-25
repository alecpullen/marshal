package db

import (
	"encoding/binary"
	"math"
)

// Chunk is one embeddable unit of a file.
type Chunk struct {
	FilePath   string
	FileHash   string
	Kind       string // "code" | "doc"
	SymbolName string
	StartLine  int
	EndLine    int
	Content    string
	TokenCount int
}

// ChunkWithVector pairs a Chunk with its embedding for insertion.
type ChunkWithVector struct {
	Chunk
	Model  string
	Dim    int
	Vector []float32
}

// FileChunkState is the stored (hash, model) for a file's chunks, used to
// decide whether a file needs re-embedding.
type FileChunkState struct {
	FileHash string
	Model    string
}

// VectorRow is one chunk's vector plus enough to render a retrieval hit.
type VectorRow struct {
	ChunkID   int64
	FilePath  string
	StartLine int
	EndLine   int
	Content   string
	Vector    []float32
}

// encodeVector serializes a float32 slice as little-endian bytes.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVector reverses encodeVector.
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
