// Package anonstruct holds the anonymous-struct fixture.
package anonstruct

// Bad has an anonymous struct field.
type Bad struct {
	Inner struct {
		N int `json:"n"`
	} `json:"inner"`
}
