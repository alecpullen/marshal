package tui

import "marshal/internal/acpwire"

// visionDraft is the prompt a submit sent: the composer's line, the text the
// prompt carried and its image refs. A vision refusal restores it, so the
// draft is not lost, and send-anyway resends exactly it.
type visionDraft struct {
	// line is the composer's line as the user wrote it, chips and all.
	line string
	// text is the prompt's text, with the chips taken out.
	text   string
	images []acpwire.PromptImage
	// armed is set by a refusal that found an active route: the send-anyway
	// key can then force that route. provider and model are the route's.
	armed    bool
	provider string
	model    string
}

// restoreVisionDraft puts a refused prompt back in the composer and arms the
// send-anyway key when the session has an active route. The notice is the
// refusal's own message, which names the cause and the fix.
func (m *Model) restoreVisionDraft(err error) {
	d := m.visionDraft
	if d == nil {
		m.note("The model cannot see images: " + err.Error())
		return
	}
	m.input.SetValue(d.line)
	m.images = append([]acpwire.PromptImage(nil), d.images...)
	notice := "The model cannot see images: " + err.Error()
	if route := m.sess.Status().ActiveRoute; route.Provider != "" && route.Model != "" {
		d.armed = true
		d.provider = route.Provider
		d.model = route.Model
		notice += " · press alt+r to send it anyway"
	}
	m.note(notice)
}

// visionDraftIntact reports whether the composer still holds d untouched: the
// same line and the same image refs. An edit is a different draft, and the
// armed override never follows it.
func (m *Model) visionDraftIntact(d *visionDraft) bool {
	if m.input.Value() != d.line || len(m.images) != len(d.images) {
		return false
	}
	for i, img := range d.images {
		if m.images[i].Hash != img.Hash {
			return false
		}
	}
	return true
}
