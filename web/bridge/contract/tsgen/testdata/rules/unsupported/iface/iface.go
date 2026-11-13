// Package iface holds the interface fixture.
package iface

// Shape is a named interface type.
type Shape interface {
	Area() float64
}

// Bad has an interface-typed field.
type Bad struct {
	Shape Shape `json:"shape"`
}
