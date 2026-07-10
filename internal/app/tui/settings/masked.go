package settings

import "charm.land/huh/v2"

// maskKey renders a secret for display: bullets plus the last four runes.
// The real value is never rendered; save paths always use the raw value.
func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	runes := []rune(key)
	if len(runes) < 4 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}

func secretField(title string, get func() string, set func(string)) *huh.Input {
	buf := new(string)
	return huh.NewInput().
		Key(title).
		Title(title).
		Value(buf).
		Description("current: " + maskKey(get()) + " — type to replace, leave empty to keep · prefer the env-var field").
		Validate(func(v string) error {
			if v != "" {
				set(v)
			}
			return nil
		})
}
