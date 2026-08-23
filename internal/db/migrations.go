package db

import "database/sql"

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    root_path TEXT UNIQUE NOT NULL,
    name TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    language TEXT,
    hash TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    last_indexed_at TEXT NOT NULL,
    UNIQUE(project_id, path)
);

CREATE TABLE IF NOT EXISTS agent_sessions (
    id TEXT PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    summary TEXT,
    leaf_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
    active_root TEXT,
    worktree_branch TEXT,
    worktree_target_branch TEXT,
    worktree_base_sha TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    content_type TEXT,
    reasoning TEXT,
    think_duration_ms INTEGER,
    created_at TEXT NOT NULL,
    final INTEGER DEFAULT 0,
    parent_id INTEGER REFERENCES messages(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT REFERENCES agent_sessions(id) ON DELETE CASCADE,
    agent_role TEXT,
    model TEXT,
    tool_name TEXT,
    args_json TEXT,
    result_summary TEXT,
    risk_level TEXT,
    approval_state TEXT,
    command_exit_code INTEGER,
    files_changed TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    sandbox_backend TEXT,
    sandbox_network_isolated INTEGER,
    sandbox_limits_json TEXT,
    sandbox_killed_reason TEXT,
    duration_ms INTEGER,
    hooks_json TEXT NOT NULL DEFAULT '[]',
    original_args_json TEXT,
    rewritten INTEGER DEFAULT 0,
    sandbox_enabled INTEGER NOT NULL DEFAULT 0,
    resource_limits INTEGER NOT NULL DEFAULT 0,
    output_truncated INTEGER NOT NULL DEFAULT 0,
    finish_reason TEXT
);

CREATE TABLE IF NOT EXISTS symbols (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    receiver TEXT,
    signature TEXT NOT NULL,
    line_start INTEGER NOT NULL,
    line_end INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_symbols_project_name ON symbols(project_id, name);
CREATE INDEX IF NOT EXISTS idx_files_project ON files(project_id);
CREATE INDEX IF NOT EXISTS idx_symbols_project ON symbols(project_id);

CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    content TEXT NOT NULL,
    confidence TEXT NOT NULL,
    source_session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);

CREATE TABLE IF NOT EXISTS turn_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
    started_at TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    class TEXT NOT NULL,
    role TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    goal TEXT NOT NULL,
    iterations INTEGER NOT NULL,
    tool_calls INTEGER NOT NULL,
    tool_errors INTEGER NOT NULL,
    cache_hits INTEGER NOT NULL,
    parse_failures INTEGER NOT NULL,
    soft_stalls INTEGER NOT NULL DEFAULT 0,
    hard_stalls INTEGER NOT NULL,
    outcome TEXT NOT NULL,
    salvage_reason TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_turn_metrics_project ON turn_metrics(project_id, id);

CREATE TABLE IF NOT EXISTS file_reads (
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    read_at TEXT NOT NULL,
    PRIMARY KEY(session_id, path)
);

CREATE TABLE IF NOT EXISTS file_writes (
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    written_at TEXT NOT NULL,
    PRIMARY KEY(session_id, path)
);

CREATE TABLE IF NOT EXISTS snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    turn_index INTEGER NOT NULL,
    hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_session ON snapshots(session_id, turn_index);

CREATE TABLE IF NOT EXISTS snapshot_files (
    snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    path TEXT NOT NULL CHECK (length(path) > 0),
    PRIMARY KEY(snapshot_id, path)
);
CREATE TABLE IF NOT EXISTS session_state (
    session_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (session_id, key)
);

CREATE TABLE IF NOT EXISTS content_blobs (
    hash TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_generations (
    generation_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    seed_digest TEXT,
    digest_source TEXT,
    end_reason TEXT
);

CREATE TABLE IF NOT EXISTS generation_turns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    generation_id TEXT NOT NULL REFERENCES session_generations(generation_id) ON DELETE CASCADE,
    turn_seq INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT,
    content_blob_hash TEXT,
    tool_calls TEXT,
    created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS generation_turns_fts USING fts5(
    content,
    content='',
    tokenize='porter unicode61'
);

-- generation_turns_fts is a contentless FTS5 table, so cascading deletes of
-- generation_turns rows do not automatically remove the index entries. This
-- trigger keeps the FTS index in sync whenever a generation_turns row is
-- deleted (including via ON DELETE CASCADE from session_generations).
-- Contentless FTS5 tables require the special 'delete' command to remove
-- index entries; a plain DELETE FROM the FTS table is rejected.
CREATE TRIGGER IF NOT EXISTS trg_generation_turns_fts_delete
AFTER DELETE ON generation_turns
BEGIN
    INSERT INTO generation_turns_fts(generation_turns_fts, rowid, content)
    VALUES('delete', OLD.id, NULL);
END;

CREATE TABLE IF NOT EXISTS token_calibration (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL,
    session_id TEXT,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    estimator_tokens INTEGER NOT NULL,
    provider_tokens INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_cal_project ON token_calibration(project_id, session_id);

CREATE TABLE IF NOT EXISTS chunks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_path   TEXT NOT NULL,
    file_hash   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    symbol_name TEXT,
    start_line  INTEGER NOT NULL,
    end_line    INTEGER NOT NULL,
    content     TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_project ON chunks(project_id);
CREATE INDEX IF NOT EXISTS idx_chunks_project_file ON chunks(project_id, file_path);

CREATE TABLE IF NOT EXISTS embeddings (
    chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    model    TEXT NOT NULL,
    dim      INTEGER NOT NULL,
    vector   BLOB NOT NULL,
    PRIMARY KEY (chunk_id)
);

CREATE TABLE IF NOT EXISTS prompt_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_prompt_history_project_id ON prompt_history(project_id, id);

CREATE TABLE IF NOT EXISTS project_skills (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    skill_name TEXT NOT NULL,
    loaded_at TEXT NOT NULL,
    scope TEXT NOT NULL,
    PRIMARY KEY (project_id, skill_name)
);
CREATE INDEX IF NOT EXISTS idx_project_skills_project ON project_skills(project_id);

CREATE TABLE IF NOT EXISTS session_mail (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    from_session TEXT NOT NULL,
    to_session TEXT,               -- NULL = broadcast to all sessions
    body TEXT NOT NULL,
    created_at TEXT NOT NULL,
    read_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_session_mail_unread ON session_mail(to_session, from_session, read_at);
`

// migrations is the ordered list of post-CREATE-TABLE schema changes.
// Each function is executed inside a transaction and recorded in
// schema_migrations by its 1-based index.
var migrations []func(*sql.Tx) error

func init() {
	migrations = append(migrations, migrateScratchpadEntries)
	migrations = append(migrations, migrateMemoryContentHash)
}

// migrateMemoryContentHash (version 2) adds memories.content_hash, backfills
// it from normalized content, drops older duplicate rows (keeping the lowest
// id per project+hash), and enforces dedup with a partial unique index.
func migrateMemoryContentHash(tx *sql.Tx) error {
	var hasCol int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = 'content_hash'`).Scan(&hasCol); err != nil {
		return err
	}
	if hasCol == 0 {
		if _, err := tx.Exec(`ALTER TABLE memories ADD COLUMN content_hash TEXT`); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id, content FROM memories WHERE content_hash IS NULL`)
	if err != nil {
		return err
	}
	type memoryRow struct {
		id      int64
		content string
	}
	var pending []memoryRow
	for rows.Next() {
		var r memoryRow
		if err := rows.Scan(&r.id, &r.content); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range pending {
		if _, err := tx.Exec(`UPDATE memories SET content_hash = ? WHERE id = ?`, MemoryContentHash(r.content), r.id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM memories WHERE content_hash IS NOT NULL AND id NOT IN (
		SELECT MIN(id) FROM memories WHERE content_hash IS NOT NULL GROUP BY project_id, content_hash
	)`); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_project_hash ON memories(project_id, content_hash) WHERE content_hash IS NOT NULL`)
	return err
}

func migrateScratchpadEntries(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS scratchpad_entries (
			session_id TEXT NOT NULL,
			entry_key TEXT NOT NULL,
			content TEXT NOT NULL,
			format TEXT NOT NULL,
			updated INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL,
			PRIMARY KEY (session_id, entry_key)
		)
	`)
	return err
}
