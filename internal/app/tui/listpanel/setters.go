package listpanel

import (
	"fmt"
	"strconv"
	"time"
)

// IntSetter parses an int, clamps to min (when min != 0), and applies it.
// When min is 0, no minimum clamping is applied (negatives are allowed).
// To enforce non-negative, pass min=1 (or use a different setter).
func IntSetter(min int, apply func(int)) func(string) error {
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

func DurationSetter(apply func(time.Duration)) func(string) error {
	return func(s string) error {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("must be a duration like 30s or 8h")
		}
		apply(d)
		return nil
	}
}

func ScalarField(id, title string, get func() string, set func(string) error) *Field {
	return &Field{ID: id, Title: title, Kind: KindScalar, GetStr: get, SetStr: set}
}

// IntField binds a scalar row to an int config value.
func IntField(id, title string, get func() int, min int, apply func(int)) *Field {
	return &Field{
		ID: id, Title: title, Kind: KindScalar,
		GetStr: func() string { return strconv.Itoa(get()) },
		SetStr: IntSetter(min, apply),
	}
}

func EnumField(id, title string, opts []string, get func() string, set func(string)) *Field {
	return &Field{
		ID: id, Title: title, Kind: KindEnum,
		Options: func() []string { return opts },
		GetStr:  get,
		SetStr:  func(v string) error { set(v); return nil },
	}
}

// SecretRow is a masked scalar: displays via MaskKey, Enter replaces (empty
// keeps), d clears the stored value.
func SecretRow(id, title string, get func() string, set func(string)) *Field {
	return &Field{
		ID: id, Title: title, Kind: KindScalar, Masked: true,
		Desc:     "enter replaces · empty keeps · d clears · prefer the env-var field",
		Keywords: []string{"secret", "api key", "token"},
		GetStr:   get,
		SetStr:   func(v string) error { set(v); return nil },
		Del:      func() { set("") },
	}
}
