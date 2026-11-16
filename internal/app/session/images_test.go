package session

import (
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/schema"
)

func newImagesTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := d.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}
	const sid = "images-sess"
	if err := d.CreateSession(sid, pid, "images", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return d, sid
}

func TestImagesSurviveLoadFromDB(t *testing.T) {
	d, sid := newImagesTestDB(t)
	s := persistedState(t, d, sid)

	refs := []schema.ImageRef{
		{Hash: "aaaa1111", MIME: "image/png", Width: 640, Height: 480, Bytes: 2048, Label: "screenshot of /tmp/ui.png"},
		{Hash: "bbbb2222", MIME: "image/jpeg", Width: 32, Height: 32, Bytes: 512},
	}
	s.AddMessageWithImages(RoleUser, "what is this?", ContentTypePlain, refs)

	reloaded := persistedState(t, d, sid)
	msgs := reloaded.Messages()
	if len(msgs) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(msgs))
	}
	if len(msgs[0].Images) != len(refs) {
		t.Fatalf("images = %+v, want %d refs", msgs[0].Images, len(refs))
	}
	for i, want := range refs {
		if msgs[0].Images[i] != want {
			t.Errorf("image %d = %+v, want %+v", i, msgs[0].Images[i], want)
		}
	}
}

func TestCorruptImagesJSONLoadsEmpty(t *testing.T) {
	d, sid := newImagesTestDB(t)
	s := persistedState(t, d, sid)

	refs := []schema.ImageRef{
		{Hash: "cccc3333", MIME: "image/png", Width: 1, Height: 1, Bytes: 10},
	}
	s.AddMessageWithImages(RoleUser, "look", ContentTypePlain, refs)

	// Corrupt the stored column behind the session's back: a load must
	// degrade to no images, never fail.
	if _, err := d.SQLDB().Exec(`UPDATE messages SET images_json = 'not json' WHERE session_id = ?`, sid); err != nil {
		t.Fatalf("corrupt images_json: %v", err)
	}

	reloaded := persistedState(t, d, sid)
	msgs := reloaded.Messages()
	if len(msgs) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1 after a corrupt images_json", len(msgs))
	}
	if len(msgs[0].Images) != 0 {
		t.Fatalf("images = %+v, want none", msgs[0].Images)
	}
}
