package tui

import "marshal/internal/acpwire"

// nodeID is a transcript node's identity in the TUI: its wire kind and its
// wire ID. Expansion state, regions, browse items, the render cache and the
// callers cache are keyed by it. A box stop reuses it with KindPassthrough
// and a "box:" key.
type nodeID struct {
	Kind string // an acpwire.Kind* value
	Key  string // the wire node's ID
}

// wireID is w's identity.
func wireID(w *acpwire.WireNode) nodeID {
	return nodeID{Kind: w.Kind, Key: w.ID}
}

// liveThinkingID is the live reasoning region's identity.
var liveThinkingID = nodeID{Kind: acpwire.KindThinking, Key: acpwire.LiveThinkingID}
