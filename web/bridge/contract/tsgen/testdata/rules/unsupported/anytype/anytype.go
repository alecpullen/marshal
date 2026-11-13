// Package anytype holds the any-typed field fixture.
package anytype

// Bad has an any-typed field.
type Bad struct {
	Value any `json:"value"`
}
