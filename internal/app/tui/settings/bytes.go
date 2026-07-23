package settings

import (
	"fmt"
	"strconv"
	"strings"
)

// formatBytes renders an int64 byte count as a human-readable string.
func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(b)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f TB", v)
}

// parseBytes parses a human-readable byte string back into an int64.
// Accepts plain numbers (bytes) or numbers with a unit suffix (B, KB, MB, GB, TB).
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty string")
	}
	// Try plain number (bytes)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	// Try with unit suffix
	unit := ""
	trimmed := s
	for i, c := range s {
		if c < '0' || c > '9' {
			if c != '.' && c != '-' {
				unit = strings.TrimSpace(s[i:])
				trimmed = strings.TrimSpace(s[:i])
				break
			}
		}
	}
	if unit == "" {
		return 0, fmt.Errorf("cannot parse %q", s)
	}
	v, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q", s)
	}
	switch strings.ToUpper(unit) {
	case "B":
		return int64(v), nil
	case "KB", "K":
		return int64(v * 1024), nil
	case "MB", "M":
		return int64(v * 1024 * 1024), nil
	case "GB", "G":
		return int64(v * 1024 * 1024 * 1024), nil
	case "TB", "T":
		return int64(v * 1024 * 1024 * 1024 * 1024), nil
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
}

// bytesField creates a scalar field that displays and edits a byte count
// using human-readable formatting.
func bytesField(id, title string, get func() int64, apply func(int64)) *field {
	return scalarField(id, title,
		func() string { return formatBytes(get()) },
		func(v string) error {
			n, err := parseBytes(v)
			if err != nil {
				return err
			}
			apply(n)
			return nil
		})
}
