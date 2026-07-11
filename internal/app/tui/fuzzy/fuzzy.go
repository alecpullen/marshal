// Package fuzzy provides the shared filter-as-you-type matcher used by the
// settings search overlay and command picker modals.
package fuzzy

import "strings"

// Rank returns the indices of haystacks matching query, best first:
// case-insensitive substring matches rank before subsequence matches.
// An empty (or whitespace) query matches everything in original order.
func Rank(query string, haystacks []string) []int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		out := make([]int, len(haystacks))
		for i := range out {
			out[i] = i
		}
		return out
	}
	var sub, seq []int
	for i, h := range haystacks {
		hay := strings.ToLower(h)
		if strings.Contains(hay, q) {
			sub = append(sub, i)
		} else if isSubsequence(q, hay) {
			seq = append(seq, i)
		}
	}
	return append(sub, seq...)
}

func isSubsequence(needle, hay string) bool {
	i := 0
	for _, c := range hay {
		if i < len(needle) && rune(needle[i]) == c {
			i++
		}
	}
	return i == len(needle)
}
