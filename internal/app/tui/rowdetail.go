package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// cachedDetail is one node's StackNode detail, kept for the node version it
// was fetched at. A failed fetch is kept too, so it is not asked again until
// the node changes.
type cachedDetail struct {
	version string
	detail  acpwire.NodeDetail
	failed  bool
}

// detailWant is a render's request for a node's detail: the version it drew,
// and the subagent whose transcript holds the node (0: the session's own).
type detailWant struct {
	version  string
	subagent int64
}

// detailMsg carries a finished StackNode fetch back to the model.
type detailMsg struct {
	key     string // the wire node ID
	version string // the version the render wanted
	res     acpwire.StackNodeResult
	err     error
}

// wantDetail is the detail cached for the node key names at version.
// Otherwise it records the want for detailQueryCmds and returns nil, and the
// row draws its wire text until the answer lands. It never calls the
// session, and it records nothing for an empty version.
func (m *Model) wantDetail(key, version string) *acpwire.NodeDetail {
	if version == "" {
		return nil
	}
	if c, ok := m.details[key]; ok && c.version == version {
		if c.failed {
			return nil
		}
		detail := c.detail
		return &detail
	}
	if m.detailWant == nil {
		m.detailWant = map[string]detailWant{}
	}
	m.detailWant[key] = detailWant{version: version, subagent: m.drilledSheetID()}
	return nil
}

// detailQueryCmds sends one StackNode fetch for every want not already in
// flight at its version, and clears the wants.
func (m *Model) detailQueryCmds() tea.Cmd {
	if len(m.detailWant) == 0 {
		return nil
	}
	// With no session there is nobody to ask. The wants are dropped rather than
	// held for a later frame: a want is recorded by a render, so the next one
	// records it again, and holding it here would fire it against whatever
	// session the node IDs happen to match later.
	if m.sess == nil {
		m.detailWant = nil
		return nil
	}
	if m.detailAsked == nil {
		m.detailAsked = map[string]string{}
	}
	var cmds []tea.Cmd
	for key, w := range m.detailWant {
		if m.detailAsked[key] == w.version {
			continue
		}
		m.detailAsked[key] = w.version
		cmds = append(cmds, fetchDetailCmd(m.ctx, m.sess, key, w))
	}
	m.detailWant = nil
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// fetchDetailCmd asks the session for one node's detail.
func fetchDetailCmd(ctx context.Context, sess acpclient.Session, key string, w detailWant) tea.Cmd {
	return func() tea.Msg {
		res, err := sess.StackNode(ctx, key, w.subagent)
		return detailMsg{key: key, version: w.version, res: res, err: err}
	}
}

// handleDetail stores a finished fetch and repaints.
func (m Model) handleDetail(msg detailMsg) (Model, tea.Cmd) {
	if v, ok := m.detailAsked[msg.key]; ok && v == msg.version {
		delete(m.detailAsked, msg.key)
	}
	// The node can leave the tree while its detail is in flight: a rollback, a
	// cleared run, a swap to another transcript. Nothing would draw the answer,
	// and pruneDetails only reaches it on the next content change, so it is
	// dropped here rather than cached in the window between.
	if _, ok := m.wire[msg.key]; !ok {
		delete(m.details, msg.key)
		return m, nil
	}
	if m.details == nil {
		m.details = map[string]cachedDetail{}
	}
	if msg.err != nil {
		m.details[msg.key] = cachedDetail{version: msg.version, failed: true}
	} else {
		m.details[msg.key] = cachedDetail{version: msg.res.Node.Version, detail: msg.res.Detail}
	}
	m.refreshViewport()
	return m, nil
}

// pruneDetails drops the cached detail and the fetches in flight of nodes the
// wire index no longer holds.
func (m *Model) pruneDetails() {
	for key := range m.details {
		if _, ok := m.wire[key]; !ok {
			delete(m.details, key)
		}
	}
	for key := range m.detailAsked {
		if _, ok := m.wire[key]; !ok {
			delete(m.detailAsked, key)
		}
	}
}
