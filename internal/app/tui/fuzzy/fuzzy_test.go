package fuzzy

import (
	"slices"
	"testing"
)

func TestRankEmptyQueryReturnsAllInOrder(t *testing.T) {
	got := Rank("  ", []string{"b", "a", "c"})
	if !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("empty query should keep order, got %v", got)
	}
}

func TestRankSubstringBeforeSubsequence(t *testing.T) {
	hay := []string{
		"xyz", // no match for "ab"
		"acb", // subsequence match (a…b)
		"cab", // substring match
	}
	got := Rank("ab", hay)
	if !slices.Equal(got, []int{2, 1}) {
		t.Fatalf("want [2 1] (substring first, no-match dropped), got %v", got)
	}
}

func TestRankCaseInsensitive(t *testing.T) {
	got := Rank("ALLOW", []string{"allow network"})
	if !slices.Equal(got, []int{0}) {
		t.Fatalf("matching must be case-insensitive, got %v", got)
	}
}
