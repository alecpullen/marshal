package tui

import (
	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/pubsub"
	"marshal/internal/repo/changedfiles"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/viewmodel"
)

// pumpChanged blocks until the session signals that its status may have
// changed, then returns a changedMsg carrying the channel it read. A closed
// channel gives a nil message, which ends the pump.
func pumpChanged(ch <-chan struct{}) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		return changedMsg{ch: ch}
	}
}

// pumpWorkEvents reads from the model's persistent Work snapshot subscription
// and returns a tea.Cmd that blocks until the next WorkSnapshot event lands,
// then returns a workMsg carrying the subscription ID the pump captured.
// Re-arming reads from the same channel: a terminated pump returns nil and
// never resubscribes itself, so only an explicit rebind starts a new stream.
func pumpWorkEvents(ch <-chan pubsub.Event[viewmodel.WorkSnapshot], subscriptionID uint64) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return workMsg{snapshot: ev.Payload, subscriptionID: subscriptionID}
	}
}

// sheetBaseRefCmd shells out to git off the UI thread — rev-parse, then the
// changed-files diff against that ref — and returns both as a
// sheetBaseRefMsg. dir is the workspace active root.
func sheetBaseRefCmd(dir string) tea.Cmd {
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		ref := gitinfo.HeadSHA(dir)
		return sheetBaseRefMsg{dir: dir, ref: ref, changed: sheetChangedFiles(changedfiles.Read(dir, ref))}
	}
}

// sheetChangedFiles converts the repo package's changed-file rows into the
// session sheet's render type. The types stay separate so the sheet does not
// depend on the git-reading package's richer File shape.
func sheetChangedFiles(files []changedfiles.File) []sessionsheet.ChangedFile {
	if files == nil {
		return nil
	}
	out := make([]sessionsheet.ChangedFile, 0, len(files))
	for _, f := range files {
		out = append(out, sessionsheet.ChangedFile{
			Path:    f.Path,
			Status:  f.Status,
			Added:   f.Added,
			Removed: f.Removed,
		})
	}
	return out
}
