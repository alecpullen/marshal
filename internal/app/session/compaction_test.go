package session

import (
	"strings"
	"testing"
)

func TestRecordCompactionAddsAVisibleMarker(t *testing.T) {
	s := newTestState()

	s.RecordCompaction(CompactionInfo{
		MessagesBefore: 62,
		MessagesAfter:  5,
		TokensBefore:   118000,
		TokensAfter:    9200,
		Generation:     3,
	})

	msgs := s.Messages()
	if len(msgs) == 0 {
		t.Fatal("compaction recorded no transcript message")
	}
	last := msgs[len(msgs)-1]
	if last.ContentType != ContentTypeCompaction {
		t.Errorf("content type = %q, want %q", last.ContentType, ContentTypeCompaction)
	}
	// CompactTokens uses integer division: 9200 renders as "9k".
	for _, want := range []string{"62", "118k", "9k", "3"} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("marker %q is missing %q", last.Content, want)
		}
	}
}

func TestCompactionInfoSummaryIsOneLine(t *testing.T) {
	got := CompactionInfo{
		MessagesBefore: 62, MessagesAfter: 5,
		TokensBefore: 118000, TokensAfter: 9200, Generation: 3,
	}.Summary()
	if strings.Contains(got, "\n") {
		t.Errorf("summary must stay one line, got %q", got)
	}
}
