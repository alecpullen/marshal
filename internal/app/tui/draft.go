package tui

// Draft is the composer's text and its paste attachments.
//
// It is the whole of what the user had typed: the text the textarea shows
// (paste chips included, never the paste contents) and the attachments those
// chips expand from on submit. A value of Draft is self-contained and
// copyable, so a screen can be read with Draft and a new screen opened with
// WithDraft without either one reaching into the other — the fields are
// unexported and only package tui ever fills them.
type Draft struct {
	// text is the composer's textarea value, paste chips as they are written.
	text string
	// pastes are the attachments the chips expand from, in the order the
	// composer appended them.
	pastes []pasteAttachment
}

// Empty reports whether the draft holds nothing.
func (d Draft) Empty() bool {
	return d.text == "" && len(d.pastes) == 0
}

// clone returns a copy of d that shares no mutable state with it: the
// attachments are copied, so appending to either draft's pastes never shows
// in the other.
func (d Draft) clone() Draft {
	c := Draft{text: d.text}
	if len(d.pastes) > 0 {
		c.pastes = append([]pasteAttachment(nil), d.pastes...)
	}
	return c
}

// Draft is the screen's draft as it stands.
func (m Model) Draft() Draft {
	return Draft{text: m.input.Value(), pastes: m.pastes}.clone()
}

// WithDraft opens the screen with d in its composer.
//
// New runs an Option before the composer exists, so this records the draft
// and New seeds the textarea from it once the textarea is built, with the
// cursor at the end of the text — where it would be after typing it. The
// recorded draft is dropped on seeding, so the composer and m.pastes are the
// one copy from then on.
func WithDraft(d Draft) Option {
	return func(m *Model) { m.pendingDraft = &d }
}
