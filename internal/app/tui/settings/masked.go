package settings

import "charm.land/huh/v2"

// maskKey renders a secret for display: bullets plus the last four runes.
// The real value is never rendered; save paths always use the raw value.
// The last-four suffix is a deliberate design choice (per spec): it
// helps the user confirm they are looking at the expected key without
// exposing the full value.
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

func secretField(title string, get func() string, set func(string)) (input *huh.Input, clear *huh.Confirm) {
	buf := new(string)
	clearVal := new(bool)
	input = huh.NewInput().
		Key(title).
		Title(title).
		Value(buf).
		Description("current: " + maskKey(get()) + " — type to replace, leave empty to keep · check Clear below to remove · prefer the env-var field").
		Validate(func(v string) error {
			if v != "" {
				set(v)
			}
			return nil
		})
	clear = huh.NewConfirm().
		Key(title + " clear").
		Title("Clear").
		Description("remove the stored " + title + " value").
		Affirmative("Clear").
		Negative("Keep").
		Value(clearVal).
		Validate(func(v bool) error {
			if v {
				set("")
			}
			return nil
		})
	return input, clear
}
