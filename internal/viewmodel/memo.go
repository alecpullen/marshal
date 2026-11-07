package viewmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// CheckWireMemo, when set, makes every WireMemo call also project afresh
// and panic on any difference, naming the first node that differs. Test
// entry points set it; production never does.
var CheckWireMemo atomic.Bool

// WireMemo remembers the wire form of every node one transcript last
// projected to, so a projection after a change re-projects only the nodes
// that changed. A node is reused when it is not live, its Version is not
// zero, and its Version, Parent, children IDs and the working directory
// are what they were. An entry made while its node was live is never
// reused: the wire form carries Live and no version covers it, so a beat
// that settles with an unchanged version would otherwise keep its live
// flag. Reused values share their slices and pointers with earlier results:
// callers must not change a returned node.
//
// The zero value is ready. A WireMemo is not safe for concurrent use.
type WireMemo struct {
	// entries is the last call's projection, by node ID key. A call replaces
	// it with exactly the nodes it returned.
	entries map[string]*memoEntry
	// fresh counts how many nodes the last call projected afresh. It exists
	// for the tests.
	fresh int
}

// memoEntry is one node's remembered wire form under the key its form reads.
type memoEntry struct {
	key  memoKey
	node acpwire.WireNode
	// encoding is the node's JSON, made on the first Encoded call and kept
	// from then on so the agent's diff pays for a changed node only.
	encoding []byte
}

// memoKey is everything a node's wire form reads. Kind is part of the ID's
// identity and the payload is covered by Version, so the key names it all.
// Live is in the key because a node's wire form carries it and no version
// covers it: an entry made from a live node must not be served to the same
// node once it settles.
type memoKey struct {
	id       NodeID
	version  uint64
	live     bool
	parent   string
	children []string
	workdir  string
}

// reusable reports whether a node's wire form may be kept: a live node's
// payload can change without its version, and a node with no version has
// nothing to compare.
func reusable(n *Node) bool { return !n.Live && n.Version != 0 }

// matches reports whether the node's key is the one the entry was made under.
func (e *memoEntry) matches(n *Node, parent, workdir string) bool {
	if e.key.id != n.ID || e.key.version != n.Version || e.key.live != n.Live || e.key.parent != parent || e.key.workdir != workdir {
		return false
	}
	if len(e.key.children) != len(n.Children) {
		return false
	}
	for i, c := range n.Children {
		if e.key.children[i] != c.ID.Key {
			return false
		}
	}
	return true
}

// memoKeyOf is the key n projects under.
func memoKeyOf(n *Node, parent, workdir string) memoKey {
	k := memoKey{id: n.ID, version: n.Version, live: n.Live, parent: parent, workdir: workdir}
	if len(n.Children) > 0 {
		k.children = make([]string, len(n.Children))
		for i, c := range n.Children {
			k.children[i] = c.ID.Key
		}
	}
	return k
}

// Project returns what Project(turns, workdir) returns, reusing the wire
// form of every unchanged node from the memo's last call. The memo then
// holds exactly the nodes this call returned.
func (m *WireMemo) Project(turns []*Node, workdir string) acpwire.WireTree {
	prev := m.entries
	next := make(map[string]*memoEntry, len(prev))
	m.fresh = 0
	t := acpwire.WireTree{Roots: make([]string, 0, len(turns))}
	var walk func(n *Node, parent string)
	walk = func(n *Node, parent string) {
		var w acpwire.WireNode
		if e := prev[n.ID.Key]; e != nil && reusable(n) && e.matches(n, parent, workdir) {
			// The kept entry carries its encoding too, so a reused node is
			// never re-encoded.
			w = e.node
			next[n.ID.Key] = e
		} else {
			m.fresh++
			w = wireNode(n, workdir)
			w.Parent = parent
			next[n.ID.Key] = &memoEntry{key: memoKeyOf(n, parent, workdir), node: w}
		}
		t.Nodes = append(t.Nodes, w)
		for _, c := range n.Children {
			walk(c, n.ID.Key)
		}
	}
	for _, n := range turns {
		t.Roots = append(t.Roots, n.ID.Key)
		walk(n, "")
	}
	m.entries = next
	if CheckWireMemo.Load() {
		// The check is fed the fresh projection, never the memo's: the memo
		// has already done its work and must not learn the answer.
		checkAgainstFresh(t, Project(turns, workdir))
	}
	return t
}

// ProjectState returns what ProjectState(st, busy, now) returns, with the
// turns projected through m. The band node is projected afresh.
func (m *WireMemo) ProjectState(st *session.State, busy bool, now time.Time) Projection {
	snap := SnapshotOf(st, busy, now)
	turns := Build(snap)
	tree := m.Project(turns, st.Workspace().ActiveRoot)
	band := BandOf(snap, turns)
	tree.Nodes = append(tree.Nodes, ProjectBand(band))
	return Projection{Turns: turns, Band: band, Tree: tree}
}

// Encoded returns the JSON encoding of the node with id from the memo's
// last call, encoding it on the first ask and keeping it. It reports false
// for an ID the last call did not return, the band's included.
func (m *WireMemo) Encoded(id string) ([]byte, bool) {
	e, ok := m.entries[id]
	if !ok {
		return nil, false
	}
	if e.encoding == nil {
		data, err := json.Marshal(e.node)
		if err != nil {
			panic(fmt.Sprintf("viewmodel: WireMemo cannot encode node %q: %v", id, err))
		}
		e.encoding = data
	}
	if CheckWireMemo.Load() {
		// A caller that changed a shared node after its encoding was taken
		// would otherwise be served the old bytes forever.
		data, err := json.Marshal(e.node)
		if err != nil || !bytes.Equal(data, e.encoding) {
			panic(fmt.Sprintf("viewmodel: WireMemo encoding of node %q is stale", id))
		}
	}
	return e.encoding, true
}

// checkAgainstFresh panics when the memo's tree is not what a fresh projection
// returns, naming the first node ID whose wire form differs.
func checkAgainstFresh(got, want acpwire.WireTree) {
	if reflect.DeepEqual(got, want) {
		return
	}
	if len(got.Nodes) == len(want.Nodes) {
		// Same nodes, different order: name the index rather than claim the
		// roots differ, which they need not.
		for i, w := range got.Nodes {
			if w.ID != want.Nodes[i].ID {
				panic(fmt.Sprintf("viewmodel: WireMemo put node %q at index %d, a fresh projection has %q there", w.ID, i, want.Nodes[i].ID))
			}
		}
	}
	fresh := make(map[string]acpwire.WireNode, len(want.Nodes))
	for _, w := range want.Nodes {
		fresh[w.ID] = w
	}
	for _, w := range got.Nodes {
		f, ok := fresh[w.ID]
		if !ok {
			panic(fmt.Sprintf("viewmodel: WireMemo returned node %q, which a fresh projection does not", w.ID))
		}
		if !reflect.DeepEqual(w, f) {
			panic(fmt.Sprintf("viewmodel: WireMemo node %q differs from a fresh projection", w.ID))
		}
	}
	if len(got.Nodes) != len(want.Nodes) {
		seen := make(map[string]bool, len(got.Nodes))
		for _, w := range got.Nodes {
			seen[w.ID] = true
		}
		for _, w := range want.Nodes {
			if !seen[w.ID] {
				panic(fmt.Sprintf("viewmodel: WireMemo is missing node %q", w.ID))
			}
		}
	}
	panic(fmt.Sprintf("viewmodel: WireMemo tree roots %v differ from a fresh projection's %v", got.Roots, want.Roots))
}
