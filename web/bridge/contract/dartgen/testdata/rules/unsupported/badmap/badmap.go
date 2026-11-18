// Package badmap holds the non-string map key fixture.
package badmap

// Bad has a map keyed by an int.
type Bad struct {
	Counts map[int]string `json:"counts"`
}
