package session

import (
	"testing"
	"time"
)

func TestSetNoticeStampsZeroSetAt(t *testing.T) {
	s := newTestState()
	before := time.Now()
	s.SetNotice(Notice{Category: NoticeProvider, Severity: SeverityError, Message: "down"})
	n, ok := s.Notice()
	if !ok {
		t.Fatal("Notice() ok = false after SetNotice")
	}
	if n.SetAt.Before(before) {
		t.Fatalf("SetAt = %v, want stamped at write time (>= %v)", n.SetAt, before)
	}
}

func TestSetNoticePreservesExplicitSetAt(t *testing.T) {
	s := newTestState()
	s.SetNotice(Notice{Category: NoticeInternal, Message: "boom", SetAt: time.Unix(100, 0)})
	n, _ := s.Notice()
	if !n.SetAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("SetAt = %v, want the caller-provided timestamp preserved", n.SetAt)
	}
}

func TestClearNoticeOnlyClearsMatchingCategory(t *testing.T) {
	s := newTestState()
	s.SetNotice(Notice{Category: NoticeProvider, Message: "down"})
	s.ClearNotice(NoticeConfig) // must NOT clear a provider notice
	if _, ok := s.Notice(); !ok {
		t.Fatal("ClearNotice(config) cleared a provider notice")
	}
	s.ClearNotice(NoticeProvider)
	if _, ok := s.Notice(); ok {
		t.Fatal("ClearNotice(provider) did not clear the provider notice")
	}
}

func TestDismissNoticeClearsUnconditionally(t *testing.T) {
	s := newTestState()
	s.SetNotice(Notice{Category: NoticeTool, Message: "x"})
	s.DismissNotice()
	if _, ok := s.Notice(); ok {
		t.Fatal("DismissNotice did not clear the notice")
	}
}

func TestNoticeCategoryString(t *testing.T) {
	want := map[NoticeCategory]string{
		NoticeProvider: "provider",
		NoticeConfig:   "config",
		NoticeTool:     "tool",
		NoticeInternal: "internal",
	}
	for cat, label := range want {
		if cat.String() != label {
			t.Errorf("NoticeCategory(%d).String() = %q, want %q", int(cat), cat.String(), label)
		}
	}
}
