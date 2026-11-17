// Package collision holds one struct whose two field names meet in Dart:
// lower camel case maps both Foo and FOO to foo, so the class would declare
// the member twice.
package collision

// Clash is rejected rather than emitted with a duplicate field.
type Clash struct {
	Foo string `json:"foo"`
	FOO string `json:"FOO"`
}
