// Package channel holds the channel fixture.
package channel

// Bad has a channel field.
type Bad struct {
	Events chan string `json:"events"`
}
