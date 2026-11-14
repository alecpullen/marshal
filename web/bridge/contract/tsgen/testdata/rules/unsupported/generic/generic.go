// Package generic holds the generic-type fixture.
package generic

// Box is a generic type.
type Box[T any] struct {
	Value T `json:"value"`
}
