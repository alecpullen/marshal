package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/subagentwork"
)

// subagentWorkspaceStore is the durable, owner-scoped adapter behind
// subagentwork.Store. One row is one owner's task: (owner, task_key) is the
// primary key and (owner, invocation_id) is unique, so one logical dispatch can
// never acquire a second workspace.
//
// The whole versioned record lives in record_json; revision is a column so a
// compare-and-swap is one conditional UPDATE rather than read-modify-write.
type subagentWorkspaceStore struct {
	db *DB
}

// NewSubagentWorkspaceStore returns the production Store over database. It is
// the adapter a persistent session must use: records held only in memory cannot
// settle a retained task after a restart.
func NewSubagentWorkspaceStore(database *DB) subagentwork.Store {
	return &subagentWorkspaceStore{db: database}
}

const subagentWorkspaceColumns = `owner_session_id, task_key, invocation_id, revision, record_json`

func (s *subagentWorkspaceStore) Create(ctx context.Context, rec subagentwork.Record) (subagentwork.Record, bool, error) {
	if err := validateWorkspaceRecord(rec); err != nil {
		return subagentwork.Record{}, false, err
	}
	rec.Version = subagentwork.RecordVersion
	rec.Revision = 1
	payload, err := marshalWorkspaceRecord(rec)
	if err != nil {
		return subagentwork.Record{}, false, err
	}
	res, err := s.db.sqlDB.ExecContext(ctx,
		`INSERT INTO session_subagent_workspaces (`+subagentWorkspaceColumns+`)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		rec.OwnerSessionID, rec.TaskKey, rec.InvocationID, rec.Revision, payload,
	)
	if err != nil {
		return subagentwork.Record{}, false, fmt.Errorf("insert subagent workspace: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return rec, true, nil
	}
	// The task key or the invocation already belongs to a record: return that
	// record unchanged so a retried dispatch observes the first attempt.
	existing, err := s.getBy(ctx, "task_key", rec.TaskKey, rec.OwnerSessionID)
	if err != nil {
		existing, err = s.getBy(ctx, "invocation_id", rec.InvocationID, rec.OwnerSessionID)
		if err != nil {
			return subagentwork.Record{}, false, fmt.Errorf("insert subagent workspace: %w", err)
		}
	}
	return existing, false, nil
}

func (s *subagentWorkspaceStore) Get(ctx context.Context, ownerSessionID, taskKey string) (subagentwork.Record, error) {
	return s.getBy(ctx, "task_key", taskKey, ownerSessionID)
}

func (s *subagentWorkspaceStore) ByInvocation(ctx context.Context, ownerSessionID, invocationID string) (subagentwork.Record, error) {
	return s.getBy(ctx, "invocation_id", invocationID, ownerSessionID)
}

// getBy reads one record scoped to its owner. column is a package constant, and
// owner_session_id is always part of the predicate: a lookup never crosses
// owners, and an unowned record is indistinguishable from a missing one.
func (s *subagentWorkspaceStore) getBy(ctx context.Context, column, value, ownerSessionID string) (subagentwork.Record, error) {
	var (
		payload  string
		revision int64
	)
	err := s.db.sqlDB.QueryRowContext(ctx,
		`SELECT record_json, revision FROM session_subagent_workspaces
		 WHERE owner_session_id = ? AND `+column+` = ?`,
		ownerSessionID, value,
	).Scan(&payload, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return subagentwork.Record{}, subagentwork.ErrRecordNotFound
	}
	if err != nil {
		return subagentwork.Record{}, fmt.Errorf("load subagent workspace: %w", err)
	}
	return unmarshalWorkspaceRecord(payload, uint64(revision))
}

func (s *subagentWorkspaceStore) CompareAndSwap(ctx context.Context, rec subagentwork.Record, expected uint64) (subagentwork.Record, error) {
	if err := validateWorkspaceRecord(rec); err != nil {
		return subagentwork.Record{}, err
	}
	rec.Version = subagentwork.RecordVersion
	rec.Revision = expected + 1
	payload, err := marshalWorkspaceRecord(rec)
	if err != nil {
		return subagentwork.Record{}, err
	}
	res, err := s.db.sqlDB.ExecContext(ctx,
		`UPDATE session_subagent_workspaces
		 SET invocation_id = ?, revision = ?, record_json = ?
		 WHERE owner_session_id = ? AND task_key = ? AND revision = ?`,
		rec.InvocationID, rec.Revision, payload, rec.OwnerSessionID, rec.TaskKey, expected,
	)
	if err != nil {
		return subagentwork.Record{}, fmt.Errorf("update subagent workspace: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// No row matched the expected revision. Distinguish "someone else
		// wrote first" from "there is no such record": both must not silently
		// succeed.
		var stored uint64
		err := s.db.sqlDB.QueryRowContext(ctx,
			`SELECT revision FROM session_subagent_workspaces
			 WHERE owner_session_id = ? AND task_key = ?`,
			rec.OwnerSessionID, rec.TaskKey,
		).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			return subagentwork.Record{}, subagentwork.ErrRecordNotFound
		}
		if err != nil {
			return subagentwork.Record{}, fmt.Errorf("update subagent workspace: %w", err)
		}
		return subagentwork.Record{}, subagentwork.ErrStaleRecord
	}
	return rec, nil
}

func (s *subagentWorkspaceStore) List(ctx context.Context, ownerSessionID string) ([]subagentwork.Record, error) {
	rows, err := s.db.sqlDB.QueryContext(ctx,
		`SELECT record_json, revision FROM session_subagent_workspaces
		 WHERE owner_session_id = ? ORDER BY task_key`,
		ownerSessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list subagent workspaces: %w", err)
	}
	defer rows.Close()
	var out []subagentwork.Record
	for rows.Next() {
		var (
			payload  string
			revision int64
		)
		if err := rows.Scan(&payload, &revision); err != nil {
			return nil, fmt.Errorf("list subagent workspaces: %w", err)
		}
		rec, err := unmarshalWorkspaceRecord(payload, uint64(revision))
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list subagent workspaces: %w", err)
	}
	return out, nil
}

func validateWorkspaceRecord(rec subagentwork.Record) error {
	if rec.OwnerSessionID == "" || rec.TaskKey == "" || rec.InvocationID == "" {
		return fmt.Errorf("subagent workspace needs an owner, a task key and an invocation id")
	}
	if rec.Version != 0 && rec.Version != subagentwork.RecordVersion {
		return fmt.Errorf("unsupported subagent workspace record version %d", rec.Version)
	}
	return nil
}

func marshalWorkspaceRecord(rec subagentwork.Record) (string, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("encode subagent workspace record: %w", err)
	}
	return string(payload), nil
}

// unmarshalWorkspaceRecord decodes a stored record and takes its revision from
// the column, which is what compare-and-swap compares. An unknown stored version
// is an explicit error: returning a blank record would report a retained task as
// if no work had been recorded for it.
func unmarshalWorkspaceRecord(payload string, revision uint64) (subagentwork.Record, error) {
	var rec subagentwork.Record
	if err := json.Unmarshal([]byte(payload), &rec); err != nil {
		return subagentwork.Record{}, fmt.Errorf("decode subagent workspace record: %w", err)
	}
	if rec.Version != subagentwork.RecordVersion {
		return subagentwork.Record{}, fmt.Errorf("unsupported subagent workspace record version %d", rec.Version)
	}
	rec.Revision = revision
	return rec, nil
}
