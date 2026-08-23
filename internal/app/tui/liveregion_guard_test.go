package tui

import (
	"testing"

	"marshal/internal/app/tui/liveregion"
)

// liveregion duplicates the gutter width because tui imports liveregion and
// the constant cannot be imported back. Same precedent as dock duplicating
// layout.StatusLineRows. If these drift, every region's rows stop lining up
// with the rest of the transcript.
func TestLiveRegionGutterWidthMatchesTranscript(t *testing.T) {
	if liveregion.GutterWidth != gutterWidth {
		t.Fatalf("liveregion.GutterWidth = %d but tui gutterWidth = %d",
			liveregion.GutterWidth, gutterWidth)
	}
}
