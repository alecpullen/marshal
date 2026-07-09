package pubsub

import "time"

// Event is a single published message. Type is an opaque string the
// publisher and subscriber agree on (defined next to the owning service,
// never centrally — F19 R4). Payload is the typed value.
type Event[T any] struct {
	Type        string
	Payload     T
	PublishedAt time.Time
}
