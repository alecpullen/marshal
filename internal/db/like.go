package db

import "strings"

// escapeLike escapes % and _ wildcards and the \ escape character itself
// so the string can be used safely in a LIKE predicate with ESCAPE '\'.
// The order is critical: \ must be escaped first so that the \ we insert
// before % and _ is not itself re-escaped.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
