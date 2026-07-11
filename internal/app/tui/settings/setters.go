package settings

import (
	"fmt"
	"strconv"
	"time"
)

// intSetter parses an int, clamps to min (when min != 0), and applies it.
func intSetter(min int, apply func(int)) func(string) error {
	return func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if min != 0 && v < min {
			v = min
		}
		apply(v)
		return nil
	}
}

func floatSetter(apply func(float64)) func(string) error {
	return func(s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		apply(v)
		return nil
	}
}

func durationSetter(apply func(time.Duration)) func(string) error {
	return func(s string) error {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("must be a duration like 30s or 8h")
		}
		apply(d)
		return nil
	}
}

func scalarField(id, title string, get func() string, set func(string) error) *field {
	return &field{id: id, title: title, kind: kindScalar, getStr: get, setStr: set}
}

// intField2 binds a scalar row to an int config value. Renamed to intField
// when the legacy numField is deleted (Task 10).
func intField2(id, title string, get func() int, min int, apply func(int)) *field {
	return &field{
		id: id, title: title, kind: kindScalar,
		getStr: func() string { return strconv.Itoa(get()) },
		setStr: intSetter(min, apply),
	}
}

func enumField(id, title string, opts []string, get func() string, set func(string)) *field {
	return &field{
		id: id, title: title, kind: kindEnum,
		options: func() []string { return opts },
		getStr:  get,
		setStr:  func(v string) error { set(v); return nil },
	}
}

// secretRow is a masked scalar: displays via maskKey, Enter replaces (empty
// keeps), d clears the stored value.
func secretRow(id, title string, get func() string, set func(string)) *field {
	return &field{
		id: id, title: title, kind: kindScalar, masked: true,
		desc:     "enter replaces · empty keeps · d clears · prefer the env-var field",
		keywords: []string{"secret", "api key", "token"},
		getStr:   get,
		setStr:   func(v string) error { set(v); return nil },
		del:      func() { set("") },
	}
}
