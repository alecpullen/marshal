package db

import (
	"math"
	"testing"
)

func TestVectorCodecRoundTrip(t *testing.T) {
	cases := [][]float32{
		{},
		{0},
		{1.5, -2.25, 0, 3.14159},
		{float32(math.MaxFloat32), float32(-math.MaxFloat32)},
	}
	for _, want := range cases {
		got := decodeVector(encodeVector(want))
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
