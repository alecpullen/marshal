package acp

import (
	"context"
	"os"
	"path/filepath"

	"marshal/internal/db"
)

// perCwdLister implements SessionLister by opening the per-cwd Marshal
// database (<cwd>/.marshal/marshal.db), migrating it idempotently,
// querying sessions, and closing the handle before returning. Each call
// is independent; there is no connection pooling across list requests.
type perCwdLister struct{}

func newPerCwdLister() *perCwdLister { return &perCwdLister{} }

func (l *perCwdLister) DeleteSession(ctx context.Context, cwd, sessionID string) (bool, error) {
	dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return false, err
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return false, err
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		return false, err
	}
	return d.DeleteSession(ctx, sessionID)
}

func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	dbPath := filepath.Join(cwd, ".marshal", "marshal.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, "", err
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		return nil, "", err
	}
	return d.ListSessions(ctx, cwd, cursor, limit)
}
