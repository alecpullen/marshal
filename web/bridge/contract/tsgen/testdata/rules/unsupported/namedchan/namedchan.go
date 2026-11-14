// Package namedchan holds the named-channel-type fixture.
package namedchan

// Conn is a named channel type.
type Conn chan int

// Bad has a field of that named type.
type Bad struct {
	Events Conn `json:"events"`
}
