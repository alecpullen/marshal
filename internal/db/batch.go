package db

import (
	"strings"
)

// buildValues returns a "(?,?,?),(?,?,?),…" string of len(cols)*n
// placeholders, suitable for a multi-row INSERT INTO ... VALUES ….
func buildValues(n, cols int) string {
	if n <= 0 || cols <= 0 {
		return ""
	}
	var b strings.Builder
	row := "(" + strings.Repeat("?,", cols)
	row = row[:len(row)-1] + ")"
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(row)
	}
	return b.String()
}
