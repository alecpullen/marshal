package viewmodel

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func TestSubagentNoticeIsNotARow(t *testing.T) {
	notice := session.TranscriptItem{Timestamp: at(5), Kind: session.KindMessage,
		Message: &session.Message{ID: 7, Role: session.RoleSystem, ContentType: session.ContentTypeSubagentNotice,
			Content: "✓ subagent 1 completed: Task 4", CreatedAt: at(5)}}
	nodes := Build(Snapshot{Items: []session.TranscriptItem{userMsg(1, 0), notice}})
	if strings.Contains(shape(nodes), "msg:7") {
		t.Fatalf("the card and its marker replace the notice:\n%s", shape(nodes))
	}
}
