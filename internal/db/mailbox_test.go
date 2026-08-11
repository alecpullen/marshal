package db

import (
	"testing"
	"time"
)

func TestMailboxRoundTrip(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	at := time.Unix(100, 0).UTC()
	if err := db.SendMail(projectID, "sender", "receiver", "hello", at); err != nil {
		t.Fatalf("SendMail failed: %v", err)
	}

	got, err := db.UnreadMail("receiver")
	if err != nil {
		t.Fatalf("UnreadMail failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 unread mail, got %d", len(got))
	}
	if got[0].FromSession != "sender" || got[0].ToSession != "receiver" || got[0].Body != "hello" {
		t.Fatalf("mail row mismatch: %+v", got[0])
	}

	if err := db.MarkMailRead([]int64{got[0].ID}, at.Add(time.Second)); err != nil {
		t.Fatalf("MarkMailRead failed: %v", err)
	}
	got, err = db.UnreadMail("receiver")
	if err != nil {
		t.Fatalf("UnreadMail after mark failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 unread mail after mark, got %d", len(got))
	}
}

func TestMailboxBroadcast(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	at := time.Unix(100, 0).UTC()
	if err := db.SendMail(projectID, "sender", "", "broadcast", at); err != nil {
		t.Fatalf("SendMail failed: %v", err)
	}

	// Broadcast is visible to another session, not the sender.
	receiver, err := db.UnreadMail("other")
	if err != nil {
		t.Fatalf("UnreadMail failed: %v", err)
	}
	if len(receiver) != 1 || receiver[0].Body != "broadcast" {
		t.Fatalf("expected broadcast for other, got %+v", receiver)
	}
	sender, err := db.UnreadMail("sender")
	if err != nil {
		t.Fatalf("UnreadMail sender failed: %v", err)
	}
	if len(sender) != 0 {
		t.Fatalf("broadcast must not be unread for sender, got %d", len(sender))
	}

	// Addressed mail is invisible to other sessions.
	if err := db.SendMail(projectID, "sender", "receiver", "direct", at.Add(time.Second)); err != nil {
		t.Fatalf("SendMail direct failed: %v", err)
	}
	other, err := db.UnreadMail("other")
	if err != nil {
		t.Fatalf("UnreadMail other failed: %v", err)
	}
	if len(other) != 1 || other[0].Body != "broadcast" {
		t.Fatalf("other should not see direct mail, got %+v", other)
	}
}

func TestMailboxOrdering(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	for i := 1; i <= 3; i++ {
		if err := db.SendMail(projectID, "a", "b", string(rune('a'+i)), time.Unix(int64(i), 0).UTC()); err != nil {
			t.Fatalf("SendMail %d failed: %v", i, err)
		}
	}
	got, err := db.UnreadMail("b")
	if err != nil {
		t.Fatalf("UnreadMail failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 mails, got %d", len(got))
	}
	for i, want := range []string{"b", "c", "d"} {
		if got[i].Body != want {
			t.Fatalf("mail %d body = %q, want %q", i, got[i].Body, want)
		}
	}
}
