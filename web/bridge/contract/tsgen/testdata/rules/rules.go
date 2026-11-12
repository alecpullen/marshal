// Package rules is the generator's fixture: one case for each mapping rule.
package rules

import (
	"encoding/json"
	"time"
)

// Color is a named string type with typed constants.
type Color string

// The palette.
const (
	ColorRed   Color = "red"
	ColorGreen Color = "green"
)

// Label is a named string type without constants.
type Label string

// Inner is another named type, kept by its Go name.
type Inner struct {
	N int `json:"n"`
}

// Sample exercises every field mapping rule.
type Sample struct {
	Plain string `json:"plain"`
	// Optional is dropped from the wire when empty.
	Optional string `json:"optional,omitempty"`
	Required *Inner `json:"required"`
	OptPtr   *int   `json:"optPtr,omitempty"`
	Skip     string `json:"-"`
	hidden   string
	At       time.Time       `json:"at"`
	Raw      json.RawMessage `json:"raw"`
	Tags     []string        `json:"tags"`
	Lookup   map[string]int  `json:"lookup"`
	Shade    Color           `json:"shade"`
	Nested   Inner           `json:"nested"`
	Fallback Label           `json:"fallback"`
	Enabled  bool            `json:"enabled"`
	Count    uint64          `json:"count"`
	Ratio    float64         `json:"ratio"`
}
