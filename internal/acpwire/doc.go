// Package acpwire holds the types both sides of the client seam exchange.
//
// The seam is described in spec C1: the agent side produces these values and
// the TUI consumes them, so nothing here may depend on the rest of marshal.
// Like web/, this package imports only the standard library
// (TestWireImportsOnlyTheStandardLibrary enforces that).
//
// Every field is exported and JSON-tagged with a camelCase name. Its type is a
// bool, a number or a string, a slice, string-keyed map or pointer of those or
// of this package's types, or a json.RawMessage (spec C3).
// TestWireFieldsFollowTheRules enforces that.
package acpwire
