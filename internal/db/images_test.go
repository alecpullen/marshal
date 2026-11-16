package db

import (
	"testing"
	"time"
)

func TestMigrationAddsImagesJSON(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// A database from before version 4: the base schema and one message row,
	// with no images_json column.
	if _, err := d.sqlDB.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := d.sqlDB.Exec(
		`INSERT INTO projects (root_path, name, created_at, updated_at) VALUES ('/tmp/proj-images', 'proj-images', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := d.sqlDB.Exec(
		`INSERT INTO agent_sessions (id, project_id, started_at) VALUES ('s', 1, '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := d.sqlDB.Exec(
		`INSERT INTO messages (session_id, role, content, created_at) VALUES ('s', 'user', 'hello', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var has int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'images_json'`).Scan(&has); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if has != 1 {
		t.Fatalf("messages.images_json present = %d, want 1", has)
	}

	msgs, err := d.GetMessages("s")
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	if msgs[0].ImagesJSON != "" {
		t.Fatalf("old row images_json = %q, want %q", msgs[0].ImagesJSON, "")
	}
}

func TestSaveMessageWithImagesRoundTrip(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pid, err := d.GetOrCreateProject("/tmp/proj-images-rt", "proj-images-rt")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const sid = "images-rt"
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if err := d.CreateSession(sid, pid, "images", t0); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	imagesJSON := `[{"hash":"abc123","mime":"image/png","width":640,"height":480,"bytes":2048,"label":"screenshot of /tmp/ui.png"},{"hash":"def456","mime":"image/jpeg","width":32,"height":32,"bytes":512}]`
	id, err := d.SaveMessageWithImages(sid, "user", "what is this?", "plain", t0, "", 0, false, 0, 0, imagesJSON)
	if err != nil {
		t.Fatalf("SaveMessageWithImages: %v", err)
	}

	got, err := d.MessagesOnBranch(sid, id)
	if err != nil {
		t.Fatalf("MessagesOnBranch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ImagesJSON != imagesJSON {
		t.Fatalf("images_json = %q, want %q", got[0].ImagesJSON, imagesJSON)
	}

	// The plain entry point still stores '' for a message with no images.
	plainID, err := d.SaveMessageStep(sid, "assistant", "a screenshot", "plain", t0, "", 0, true, id, 0)
	if err != nil {
		t.Fatalf("SaveMessageStep: %v", err)
	}
	plain, err := d.GetMessages(sid)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var found bool
	for _, m := range plain {
		if m.ID == plainID {
			found = true
			if m.ImagesJSON != "" {
				t.Fatalf("plain message images_json = %q, want %q", m.ImagesJSON, "")
			}
		}
	}
	if !found {
		t.Fatalf("plain message %d not returned by GetMessages", plainID)
	}
}
