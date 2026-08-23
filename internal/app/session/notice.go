package session

import "time"

// NoticeCategory identifies which subsystem a user-facing notice belongs
// to. Clearing is category-scoped so a success in one subsystem never
// dismisses an unrelated notice.
type NoticeCategory int

const (
	NoticeProvider NoticeCategory = iota
	NoticeConfig
	NoticeTool
	NoticeInternal
)

func (c NoticeCategory) String() string {
	switch c {
	case NoticeProvider:
		return "provider"
	case NoticeConfig:
		return "config"
	case NoticeTool:
		return "tool"
	default:
		return "internal"
	}
}

// NoticeSeverity drives the banner's glyph and colour.
type NoticeSeverity int

const (
	SeverityError NoticeSeverity = iota
	SeverityWarn
)

// Notice is a single user-facing banner entry. SetAt is stamped by
// SetNotice when zero, so every notice carries a real timestamp no matter
// which layer produced it — the fix for runtime-injected errors pinning
// the banner forever.
type Notice struct {
	Category NoticeCategory
	Severity NoticeSeverity
	Message  string
	Hint     string
	Source   string
	SetAt    time.Time
}

// SetNotice stores n as the session's current notice, replacing any
// existing one. A zero SetAt is stamped with the current time.
func (s *State) SetNotice(n Notice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.SetAt.IsZero() {
		n.SetAt = time.Now()
	}
	s.notice = n
	s.noticeSet = true
}

// Notice returns the current notice, if any.
func (s *State) Notice() (Notice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notice, s.noticeSet
}

// ClearNotice removes the current notice only when it belongs to cat — a
// success in one subsystem must not dismiss an unrelated notice.
func (s *State) ClearNotice(cat NoticeCategory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noticeSet && s.notice.Category == cat {
		s.noticeSet = false
		s.notice = Notice{}
	}
}

// DismissNotice removes the current notice unconditionally (TTL expiry or
// manual dismiss).
func (s *State) DismissNotice() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noticeSet = false
	s.notice = Notice{}
}
