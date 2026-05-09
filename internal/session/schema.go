package session

// SchemaVersion is the current database schema version.
const SchemaVersion = 2

// SchemaV2 contains all SQL statements for schema version 2.
// This includes the original tables plus context store tables with FTS5.
const SchemaV2 = `
-- Schema version tracking
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

-- Original tables (v1)
CREATE TABLE IF NOT EXISTS sessions (
    id                 TEXT PRIMARY KEY,
    target_branch      TEXT NOT NULL,
    target_start_sha   TEXT NOT NULL,
    staging_branch     TEXT NOT NULL,
    started_at         TEXT NOT NULL,
    shipped_at         TEXT,
    shipped_target_sha TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    id                 TEXT PRIMARY KEY,
    session_id         TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    prompt             TEXT NOT NULL,
    parent_staging_sha TEXT NOT NULL,
    staging_sha        TEXT,
    status             TEXT NOT NULL DEFAULT 'running',
    started_at         TEXT NOT NULL,
    ended_at           TEXT,
    summary            TEXT
);

CREATE TABLE IF NOT EXISTS rounds (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id        TEXT    NOT NULL,
    task_id           TEXT    NOT NULL,
    round             INTEGER NOT NULL,
    role              TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    content           TEXT    NOT NULL DEFAULT '',
    verdict_json      TEXT,
    think_blocks      TEXT
);

CREATE TABLE IF NOT EXISTS plans (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT,
    source_task  TEXT NOT NULL,
    goals_json   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS goals (
    id              TEXT PRIMARY KEY,
    plan_id         TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    description     TEXT,
    priority        TEXT NOT NULL,
    files_json      TEXT,
    depends_on_json TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      TEXT NOT NULL,
    started_at      TEXT,
    completed_at    TEXT,
    task_id         TEXT,
    findings_json   TEXT
);

CREATE TABLE IF NOT EXISTS read_only_files (
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    added_at    TEXT NOT NULL,
    PRIMARY KEY (session_id, path)
);

-- v2: Context store tables
-- Context entries metadata (content is stored inline or in blob files)
CREATE TABLE IF NOT EXISTS ctx_entries (
    ref             TEXT PRIMARY KEY,
    key             TEXT NOT NULL,
    kind            TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    size            INTEGER NOT NULL,
    size_tokens     INTEGER,
    produced_by     TEXT,
    produced_at     TEXT NOT NULL,
    tags            TEXT,                       -- JSON array
    ttl_seconds     INTEGER,
    superseded_by   TEXT,
    session_id      TEXT NOT NULL,
    storage_type    TEXT NOT NULL DEFAULT 'inline', -- 'inline' or 'file'
    inline_content  BLOB,                       -- Only for storage_type='inline'
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (superseded_by) REFERENCES ctx_entries(ref) ON DELETE SET NULL
);

-- FTS5 virtual table for full-text search
-- Uses external content pattern to stay synchronized with ctx_entries
CREATE VIRTUAL TABLE IF NOT EXISTS ctx_entries_fts USING fts5(
    content,
    tags,
    tokenize = 'unicode61 remove_diacritics 2',
    content='ctx_entries',
    content_rowid='rowid'
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_tasks_session ON tasks(session_id);
CREATE INDEX IF NOT EXISTS idx_rounds_task   ON rounds(task_id);
CREATE INDEX IF NOT EXISTS idx_plans_session ON plans(session_id);
CREATE INDEX IF NOT EXISTS idx_goals_plan    ON goals(plan_id);
CREATE INDEX IF NOT EXISTS idx_goals_session ON goals(session_id);
CREATE INDEX IF NOT EXISTS idx_goals_status  ON goals(status);
CREATE INDEX IF NOT EXISTS idx_rof_session   ON read_only_files(session_id);

-- Context store indexes
CREATE INDEX IF NOT EXISTS idx_ctx_entries_session      ON ctx_entries(session_id);
CREATE INDEX IF NOT EXISTS idx_ctx_entries_key          ON ctx_entries(key);
CREATE INDEX IF NOT EXISTS idx_ctx_entries_kind         ON ctx_entries(kind);
CREATE INDEX IF NOT EXISTS idx_ctx_entries_produced_by  ON ctx_entries(produced_by);
CREATE INDEX IF NOT EXISTS idx_ctx_entries_produced_at  ON ctx_entries(produced_at);
CREATE INDEX IF NOT EXISTS idx_ctx_entries_superseded   ON ctx_entries(superseded_by) WHERE superseded_by IS NOT NULL;

-- FTS5 synchronization triggers
-- Insert: Add to FTS if indexable (size <= 1MB)
CREATE TRIGGER IF NOT EXISTS ctx_entries_fts_insert AFTER INSERT ON ctx_entries
WHEN NEW.size <= 1048576  -- 1MB index limit
BEGIN
    INSERT INTO ctx_entries_fts(rowid, content, tags)
    VALUES (
        NEW.rowid,
        COALESCE(NEW.inline_content, ''),
        NEW.tags
    );
END;

-- Update: Sync FTS when entry updated
CREATE TRIGGER IF NOT EXISTS ctx_entries_fts_update AFTER UPDATE ON ctx_entries
WHEN NEW.size <= 1048576
BEGIN
    INSERT INTO ctx_entries_fts(ctx_entries_fts, rowid, content, tags)
    VALUES ('delete', OLD.rowid, '', '');
    INSERT INTO ctx_entries_fts(rowid, content, tags)
    VALUES (
        NEW.rowid,
        COALESCE(NEW.inline_content, ''),
        NEW.tags
    );
END;

-- Delete: Remove from FTS
CREATE TRIGGER IF NOT EXISTS ctx_entries_fts_delete AFTER DELETE ON ctx_entries
BEGIN
    INSERT INTO ctx_entries_fts(ctx_entries_fts, rowid, content, tags)
    VALUES ('delete', OLD.rowid, '', '');
END;
`
