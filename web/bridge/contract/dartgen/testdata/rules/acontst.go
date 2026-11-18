package rules

// BaseValue is an untyped constant, so it belongs to no union.
const BaseValue = "base"

// The constants of Shade are declared before the type they name, in a file
// that sorts before it.
const (
	ShadeAmber Shade = "amber"
	ShadeAzure Shade = "azure"
	// ShadeAqua omits its expression list, so it repeats the previous
	// value and its type. A union holds a constant's value, so a value
	// two constants share appears twice.
	ShadeAqua
	// ShadeTeal takes its value from another constant.
	ShadeTeal Shade = BaseValue
)

// Repeat's second constant omits its expression list, so it repeats the
// first's value and its type.
const (
	RepeatOne Repeat = "one"
	RepeatTwo
)

// Text's constant is a raw string that spans lines.
const TextValue Text = `first
second`
