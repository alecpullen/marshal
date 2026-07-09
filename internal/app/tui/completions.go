package tui

import (
	"strings"
)

// F18: editor completions — fuzzy score + completion popup component.
//
// The popup is fed by the commands registry (`/`) and the repo file index
// (`@`). It filters items via a simple in-package subsequence scorer
// (fuzzyScore) and is rendered above the input area by view.go.

// fuzzyScore returns a subsequence-match score for query against target
// (higher is better). A non-match returns (0, false). The scorer rewards
// consecutive matches and prefix matches so "/pl" ranks "/plan" above
// "/help". This is a simple in-package scorer (F18 design sketch: "a
// simple subsequence scorer") — no external fuzzy dep.
func fuzzyScore(query, target string) (int, bool) {
	q := strings.ToLower(query)
	t := strings.ToLower(target)
	if len(q) == 0 {
		return 0, true
	}
	if len(q) > len(t) {
		return 0, false
	}
	score := 0
	ti := 0
	consecutive := 0
	for qi := 0; qi < len(q); qi++ {
		matched := false
		for ; ti < len(t); ti++ {
			if q[qi] == t[ti] {
				matched = true
				score += 1 + consecutive
				consecutive++
				if qi == 0 {
					score += 5 // prefix bonus
				}
				if ti == 0 {
					score += 2 // leading target bonus
				}
				ti++
				break
			}
			consecutive = 0
		}
		if !matched {
			return 0, false
		}
	}
	// Mild preference for shorter targets when scores tie (more specific).
	score += max(20-len(t), 0)
	return score, true
}

type completionKind int

const (
	completionCommand completionKind = iota
	completionFile
)

type completionItem struct {
	Text        string
	Description string
	Kind        completionKind
}

type completionPopup struct {
	items        []completionItem
	filtered     []completionItem
	index        int
	visible      bool
	acceptedText string
}

func newCompletionPopup(items []completionItem) *completionPopup {
	return &completionPopup{items: items}
}

func (p *completionPopup) update(query string) {
	if query == "" {
		p.visible = false
		p.filtered = nil
		p.index = 0
		p.acceptedText = ""
		return
	}
	type scored struct {
		item  completionItem
		score int
	}
	hits := make([]scored, 0, len(p.items))
	for _, it := range p.items {
		if it.Kind == completionFile && containsRunnerWhitespace(it.Text) {
			continue
		}
		s, ok := fuzzyScore(query, it.Text)
		if ok {
			hits = append(hits, scored{it, s})
		}
	}
	// Sort by score desc, then text asc.
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].score > hits[i].score ||
				(hits[j].score == hits[i].score && hits[j].item.Text < hits[i].item.Text) {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	p.filtered = make([]completionItem, len(hits))
	for i, h := range hits {
		p.filtered[i] = h.item
	}
	p.index = 0
	p.acceptedText = ""
	p.visible = len(p.filtered) > 0
}

func (p *completionPopup) matches() []completionItem { return p.filtered }
func (p *completionPopup) isVisible() bool           { return p.visible }

func (p *completionPopup) dismiss() {
	p.visible = false
	p.filtered = nil
	p.index = 0
	p.acceptedText = ""
}

func (p *completionPopup) moveDown() {
	if len(p.filtered) == 0 {
		return
	}
	p.index = (p.index + 1) % len(p.filtered)
}

func (p *completionPopup) moveUp() {
	if len(p.filtered) == 0 {
		return
	}
	p.index = (p.index - 1 + len(p.filtered)) % len(p.filtered)
}

func (p *completionPopup) accept() {
	if len(p.filtered) == 0 {
		return
	}
	chosen := p.filtered[p.index]
	switch chosen.Kind {
	case completionCommand:
		// Commands get a trailing space so args can be typed immediately.
		p.acceptedText = chosen.Text + " "
	case completionFile:
		// File paths are inserted as literal @path tokens so the agent
		// runner can extract and pin them into the context pack.
		accepted := "@" + chosen.Text
		if !strings.ContainsAny(chosen.Text, " \t") {
			p.acceptedText = accepted + " "
		} else {
			p.acceptedText = accepted
		}
	}
	p.visible = false
	p.filtered = nil
	p.index = 0
}
