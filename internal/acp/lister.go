package acp

import (
	"context"
	"path/filepath"

	"marshal/internal/db"
)

// perCwdLister implements SessionLister by opening the per-cwd Marshal
// database (<cwd>/.marshal/marshal.db), migrating it idempotently,
// querying sessions, and closing the handle before returning. Each call
// is independent; there is no connection pooling across list requests.
type perCwdLister struct{}

func newPerCwdLister() *perCwdLister { return &perCwdLister{} }

func (l *perCwdLister) ListSessions(ctx context.Context, cwd, cursor string, limit int) ([]db.SessionEntry, string, error) {
	d, err := db.Open(filepath.Join(cwd, ".marshal", "marshal.db"))
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	if err := d.Migrate(); err != nil {
		return nil, "", err
	}
	return d.ListSessions(ctx, cwd, cursor, limit)
}
