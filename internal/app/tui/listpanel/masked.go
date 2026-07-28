package listpanel

// MaskKey renders a secret for display: bullets plus the last four runes.
// The real value is never rendered; save paths always use the raw value.
// The last-four suffix is a deliberate design choice (per spec): it
// helps the user confirm they are looking at the expected key without
// exposing the full value.
func MaskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	runes := []rune(key)
	if len(runes) < 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}
