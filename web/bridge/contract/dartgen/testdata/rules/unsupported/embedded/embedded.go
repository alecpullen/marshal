// Package embedded holds the embedded-field fixture.
package embedded

// Base is embedded by Bad.
type Base struct {
	N int `json:"n"`
}

// Bad embeds Base.
type Bad struct {
	Base
}
