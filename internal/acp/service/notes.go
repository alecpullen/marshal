package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/llm/provider"
	"marshal/internal/oauth"
)

// errNoteRole is what AddNote returns for a role it does not carry: a note is
// either the agent's system message or text the user typed.
var errNoteRole = errors.New("inproc: a note's role must be system or user")

// errNoticeCategory is what ClearNotice returns for a category the wire does
// not carry: clearing by a category that matches no banner would report a
// success that dismissed nothing.
var errNoticeCategory = errors.New("inproc: a notice's category must be provider, config, tool or internal")

// AddNote appends note to the transcript without starting a turn, and does not
// fire Changed (decision 2).
func (s *Session) AddNote(ctx context.Context, note acpwire.Note) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	role := session.RoleSystem
	switch note.Role {
	case "", acpwire.RoleSystem:
		role = session.RoleSystem
	case acpwire.RoleUser:
		role = session.RoleUser
	default:
		return errNoteRole
	}
	contentType := session.ContentType(note.ContentType)
	if note.ContentType == "" {
		contentType = session.ContentTypePlain
	}
	s.state.AddMessage(role, note.Text, contentType)
	return nil
}

// SetNotice makes notice the session's banner, replacing any other, and does
// not fire Changed (decision 2).
func (s *Session) SetNotice(ctx context.Context, notice acpwire.Notice) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.SetNotice(noticeState(notice))
	return nil
}

// ClearNotice removes the banner only when its category is category, and does
// not fire Changed (decision 2). A category the wire does not carry is an
// error rather than a success: it matches no banner.
func (s *Session) ClearNotice(ctx context.Context, category string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	mapped, ok := noticeCategory(category)
	if !ok {
		return errNoticeCategory
	}
	s.state.ClearNotice(mapped)
	return nil
}

// DismissNotice removes the banner, whatever its category, and does not fire
// Changed (decision 2).
func (s *Session) DismissNotice(ctx context.Context) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.state.DismissNotice()
	return nil
}

// noticeWire converts a session notice for the wire.
func noticeWire(n session.Notice) acpwire.Notice {
	severity := acpwire.NoticeError
	if n.Severity == session.SeverityWarn {
		severity = acpwire.NoticeWarn
	}
	return acpwire.Notice{
		Category: n.Category.String(),
		Severity: severity,
		Message:  n.Message,
		Hint:     n.Hint,
		Source:   n.Source,
		SetAt:    acpwire.Millis(n.SetAt),
	}
}

// noticeState converts a wire notice for the session. An unknown category is
// internal, and any severity but warn is error.
func noticeState(n acpwire.Notice) session.Notice {
	severity := session.SeverityError
	if n.Severity == acpwire.NoticeWarn {
		severity = session.SeverityWarn
	}
	// Setting keeps an unknown category as internal; only clearing refuses one.
	category, _ := noticeCategory(n.Category)
	return session.Notice{
		Category: category,
		Severity: severity,
		Message:  n.Message,
		Hint:     n.Hint,
		Source:   n.Source,
		SetAt:    acpwire.Time(n.SetAt),
	}
}

// noticeCategory maps a wire category onto the session's. An empty category is
// internal, so a notice set without one can be cleared without one; anything
// else unknown is internal too but reports ok false, because clearing by a
// category that matches no banner is an error.
func noticeCategory(category string) (session.NoticeCategory, bool) {
	switch category {
	case "", acpwire.NoticeInternal:
		return session.NoticeInternal, true
	case acpwire.NoticeProvider:
		return session.NoticeProvider, true
	case acpwire.NoticeConfig:
		return session.NoticeConfig, true
	case acpwire.NoticeTool:
		return session.NoticeTool, true
	default:
		return session.NoticeInternal, false
	}
}

// runError returns err as a run's caller sees it (decision 4): a provider
// failure becomes an *acpwire.Error carrying the notice its caller shows, and
// every other error, a cancellation included, is returned unchanged.
func runError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var authErr *oauth.ErrAuthRequired
	if errors.As(err, &authErr) {
		name := authErr.ServerName
		if name == "" {
			name = "this provider"
		}
		return noteError(err, acpwire.CodeAuthRequired, session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityError,
			Message:  firstLine(err.Error()),
			Hint:     "Run /connect to sign in to " + name + ".",
		})
	}
	var httpErr *provider.ProviderError
	var reqErr *provider.RequestError
	if errors.As(err, &httpErr) || errors.As(err, &reqErr) {
		return noteError(err, acpwire.CodeProviderError, session.Notice{
			Category: session.NoticeProvider,
			Severity: session.SeverityError,
			Message:  firstLine(err.Error()),
			Hint:     "Run /connect to review the provider, or /models to pick another model.",
		})
	}
	return err
}

// noteError wraps err as a coded wire error whose data carries notice. The
// notice's source stays empty for the caller to stamp.
func noteError(err error, code acpwire.ErrorCode, notice session.Notice) error {
	data, merr := json.Marshal(acpwire.ErrorData{Notice: ptrOf(noticeWire(notice))})
	if merr != nil {
		return err
	}
	return &acpwire.Error{Code: code, Message: err.Error(), Data: data}
}

// ptrOf is the address of v.
func ptrOf[T any](v T) *T { return &v }

// firstLine is the first line of s, right-trimmed, as the TUI's is.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t\r")
}
