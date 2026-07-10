package db

const schema = `
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
    leaf_message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL
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
    hooks_json TEXT NOT NULL DEFAULT '[]'
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
    soft_stalls INTEGER NOT NULL,
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
    path TEXT NOT NULL,
    PRIMARY KEY(snapshot_id, path)
);
CREATE TABLE IF NOT EXISTS session_state (
    session_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (session_id, key)
);
`
