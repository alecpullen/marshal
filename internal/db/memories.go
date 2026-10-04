package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxMemoryRows = 1000

const (
	MemoryConfidenceTentative = "tentative"
	MemoryConfidenceConfirmed = "confirmed"
	MemoryConfidenceStale     = "stale"
)

// NormalizeMemoryContent canonicalizes memory text for dedup: lowercase,
// whitespace-collapsed, trimmed.
func NormalizeMemoryContent(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(content)), " ")
}

// MemoryContentHash returns the SHA-256 hex digest of the normalized content.
// Shared by SaveMemory dedup and the content_hash migration backfill.
func MemoryContentHash(content string) string {
	sum := sha256.Sum256([]byte(NormalizeMemoryContent(content)))
	return hex.EncodeToString(sum[:])
}

// Memory scopes. Project memories belong to one project, workspace
// memories to every project sharing a workspace (ScopeKey is its name), and
// global memories to all projects.
const (
	MemoryScopeProject   = "project"
	MemoryScopeWorkspace = "workspace"
	MemoryScopeGlobal    = "global"
)

type Memory struct {
	ID              int64
	ProjectID       int64
	Kind            string // "fact", "architecture", "decision"
	Content         string
	Confidence      string // "tentative", "confirmed", "stale"
	SourceSessionID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Scope           string // "project", "workspace", "global"
	ScopeKey        string // workspace name for the workspace scope
	OwnerID         string
	LearnedAgent    string
	LearnedStep     int64
	ConfirmedBy     []string
}

// MemoryInput is the argument to SaveMemoryWith.
type MemoryInput struct {
	Kind            string
	Content         string
	SourceSessionID string
	LearnedAgent    string
	LearnedStep     int64
	Now             time.Time
}

// SaveMemory inserts a new memory row with confidence "tentative". A row whose
// normalized content matches an existing memory (same project, same
// content_hash) is refreshed in place instead of duplicating: its kind is
// promoted to the current classification, its source session moves to the
// current writer, and updated_at only ever moves forward (never regresses),
// preserving most-recent ordering for rankMemories.
func (db *DB) SaveMemory(projectID int64, kind, content, sourceSessionID string, now time.Time) error {
	return db.SaveMemoryWith(projectID, MemoryInput{Kind: kind, Content: content, SourceSessionID: sourceSessionID, Now: now})
}

// SaveMemoryWith is SaveMemory plus provenance: the agent label and step that
// learned the memory. A refresh of an existing row keeps its original
// provenance unless none was recorded. New rows are project-scoped.
func (db *DB) SaveMemoryWith(projectID int64, in MemoryInput) error {
	kind, content, sourceSessionID := in.Kind, in.Content, in.SourceSessionID
	nowStr := in.Now.UTC().Format(time.RFC3339)
	hash := MemoryContentHash(content)
	res, err := db.sqlDB.Exec(
		`UPDATE memories
		 SET kind = ?,
		     source_session_id = COALESCE(NULLIF(?, ''), source_session_id),
		     learned_agent = CASE WHEN learned_agent = '' THEN ? ELSE learned_agent END,
		     learned_step = CASE WHEN learned_agent = '' THEN ? ELSE learned_step END,
		     updated_at = CASE WHEN julianday(updated_at) < julianday(?) THEN ? ELSE updated_at END
		 WHERE project_id = ? AND content_hash = ?`,
		kind, sourceSessionID, in.LearnedAgent, in.LearnedStep, nowStr, nowStr, projectID, hash,
	)
	if err != nil {
		return fmt.Errorf("refresh memory: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return nil
	}
	_, err = db.sqlDB.Exec(
		`INSERT INTO memories (project_id, kind, content, content_hash, confidence, source_session_id, created_at, updated_at, learned_agent, learned_step)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
		projectID, kind, content, hash, MemoryConfidenceTentative, sourceSessionID, nowStr, nowStr, in.LearnedAgent, in.LearnedStep,
	)
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	if err := db.pruneMemories(projectID); err != nil {
		return err
	}
	return nil
}

// pruneMemories caps a project's own project-scoped rows. Promoted rows
// (workspace, global) are exempt: they are shared and must not be evicted
// because their origin project grew past the cap.
func (db *DB) pruneMemories(projectID int64) error {
	_, err := db.sqlDB.Exec(
		`DELETE FROM memories WHERE id IN (
			SELECT id FROM memories WHERE project_id = ? AND scope = 'project' ORDER BY id DESC LIMIT -1 OFFSET ?
		)`,
		projectID, maxMemoryRows,
	)
	if err != nil {
		return fmt.Errorf("prune memories: %w", err)
	}
	return nil
}

const memoryColumns = `id, project_id, kind, content, confidence, source_session_id, created_at, updated_at,
	scope, scope_key, owner_id, learned_agent, learned_step, confirmed_by`

type rowScanner interface{ Scan(dest ...any) error }

// scanMemory reads one row selected with memoryColumns and parses the
// RFC3339 timestamps into UTC and confirmed_by from its JSON array.
func scanMemory(row rowScanner) (Memory, error) {
	var (
		m                    Memory
		sourceSessionID      sql.NullString
		createdAt, updatedAt string
		confirmedBy          string
	)
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Kind, &m.Content, &m.Confidence, &sourceSessionID, &createdAt, &updatedAt,
		&m.Scope, &m.ScopeKey, &m.OwnerID, &m.LearnedAgent, &m.LearnedStep, &confirmedBy); err != nil {
		return Memory{}, err
	}
	if sourceSessionID.Valid {
		m.SourceSessionID = sourceSessionID.String
	}
	parsedCreated, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return m, fmt.Errorf("parse created_at: %w", err)
	}
	m.CreatedAt = parsedCreated.UTC()
	parsedUpdated, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return m, fmt.Errorf("parse updated_at: %w", err)
	}
	m.UpdatedAt = parsedUpdated.UTC()
	m.ConfirmedBy = []string{}
	if err := json.Unmarshal([]byte(confirmedBy), &m.ConfirmedBy); err != nil {
		return m, fmt.Errorf("parse confirmed_by: %w", err)
	}
	return m, nil
}

func scanMemories(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	var memories []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return memories, nil
}

// GetMemories returns all memory rows for a project, ordered by id.
func (db *DB) GetMemories(projectID int64) ([]Memory, error) {
	rows, err := db.sqlDB.Query(`SELECT `+memoryColumns+` FROM memories WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	return scanMemories(rows)
}

// GetScopedMemories returns what a session in projectID sees: the project's
// own project-scoped rows, then workspace-scoped rows whose scope_key is
// workspace (skipped when workspace is empty), then global rows from any
// project. Each group is ordered by id, as GetMemories orders.
func (db *DB) GetScopedMemories(projectID int64, workspace string) ([]Memory, error) {
	rows, err := db.sqlDB.Query(`SELECT `+memoryColumns+` FROM memories
		WHERE (scope = 'project' AND project_id = ?)
		   OR (scope = 'workspace' AND ? <> '' AND scope_key = ?)
		   OR scope = 'global'
		ORDER BY CASE scope WHEN 'project' THEN 0 WHEN 'workspace' THEN 1 ELSE 2 END, id`,
		projectID, workspace, workspace)
	if err != nil {
		return nil, fmt.Errorf("query scoped memories: %w", err)
	}
	return scanMemories(rows)
}

// GetMemory returns a single memory row by id.
func (db *DB) GetMemory(id int64) (Memory, error) {
	m, err := scanMemory(db.sqlDB.QueryRow(`SELECT `+memoryColumns+` FROM memories WHERE id = ?`, id))
	if err != nil {
		return Memory{}, fmt.Errorf("query memory: %w", err)
	}
	return m, nil
}

// Suggestion proposes promoting a project-scoped memory because another
// project holds one with the same content.
type Suggestion struct {
	MemoryID       int64
	MatchProjectID int64
	MatchRoot      string
	SuggestedScope string
}

// MemorySuggestions returns one suggestion per project-scoped memory of
// projectID that another project also holds (same content_hash). The match
// reported is the lowest-id project. Twins that are already global count, so
// the redundant project copy can be merged into them. Suggested scope is
// always global: a project-scoped row records no workspace, so the spec's
// second case (a match on the same workspace suggests workspace) cannot be
// computed.
func (db *DB) MemorySuggestions(projectID int64) ([]Suggestion, error) {
	rows, err := db.sqlDB.Query(`
		SELECT m.id, o.project_id, p.root_path
		FROM memories m
		JOIN memories o ON o.content_hash = m.content_hash AND o.project_id <> m.project_id
		JOIN projects p ON p.id = o.project_id
		WHERE m.project_id = ? AND m.scope = 'project' AND o.scope IN ('project', 'global')
		ORDER BY m.id, o.project_id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query memory suggestions: %w", err)
	}
	defer rows.Close()
	var out []Suggestion
	seen := map[int64]bool{}
	for rows.Next() {
		var s Suggestion
		if err := rows.Scan(&s.MemoryID, &s.MatchProjectID, &s.MatchRoot); err != nil {
			return nil, fmt.Errorf("scan memory suggestion: %w", err)
		}
		if seen[s.MemoryID] {
			continue
		}
		seen[s.MemoryID] = true
		s.SuggestedScope = MemoryScopeGlobal
		out = append(out, s)
	}
	return out, rows.Err()
}

// PromoteMemory moves a memory to scope (scopeKey names the workspace for
// the workspace scope) and merges the duplicates it now covers in one
// transaction: those rows are deleted and their learned_agent labels appended
// to confirmed_by. Promoting to global merges other projects' copies of any
// scope; to workspace, other projects' rows of the same workspace; to project
// (a demotion), nothing.
func (db *DB) PromoteMemory(id int64, scope, scopeKey string, now time.Time) error {
	switch scope {
	case MemoryScopeProject, MemoryScopeGlobal:
		scopeKey = ""
	case MemoryScopeWorkspace:
		if scopeKey == "" {
			return fmt.Errorf("promote memory: workspace scope requires a scope key")
		}
	default:
		return fmt.Errorf("promote memory: invalid scope %q", scope)
	}
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("promote memory: %w", err)
	}
	defer tx.Rollback()
	m, err := scanMemory(tx.QueryRow(`SELECT `+memoryColumns+` FROM memories WHERE id = ?`, id))
	if err != nil {
		return fmt.Errorf("promote memory: %w", err)
	}
	var hash string
	if err := tx.QueryRow(`SELECT content_hash FROM memories WHERE id = ?`, id).Scan(&hash); err != nil {
		return fmt.Errorf("promote memory: %w", err)
	}
	confirmed := m.ConfirmedBy
	// Merge only the copies the promoted row now covers, so no other project
	// loses a memory it still sees: a global row covers every other
	// project's copy of any scope; a workspace row covers other projects'
	// rows in the same workspace (project rows record no workspace, so those
	// stay); a project row covers nothing.
	var dupQuery string
	var dupArgs []any
	switch scope {
	case MemoryScopeGlobal:
		dupQuery = `SELECT id, learned_agent FROM memories WHERE content_hash = ? AND project_id <> ? AND id <> ? ORDER BY id`
		dupArgs = []any{hash, m.ProjectID, id}
	case MemoryScopeWorkspace:
		dupQuery = `SELECT id, learned_agent FROM memories WHERE content_hash = ? AND project_id <> ? AND id <> ? AND scope = 'workspace' AND scope_key = ? ORDER BY id`
		dupArgs = []any{hash, m.ProjectID, id, scopeKey}
	}
	var dupIDs []int64
	if dupQuery != "" {
		rows, err := tx.Query(dupQuery, dupArgs...)
		if err != nil {
			return fmt.Errorf("promote memory: %w", err)
		}
		for rows.Next() {
			var dupID int64
			var agent string
			if err := rows.Scan(&dupID, &agent); err != nil {
				rows.Close()
				return fmt.Errorf("promote memory: %w", err)
			}
			dupIDs = append(dupIDs, dupID)
			confirmed = appendUnique(confirmed, agent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("promote memory: %w", err)
		}
		rows.Close()
	}
	for _, dupID := range dupIDs {
		if _, err := tx.Exec(`DELETE FROM memories WHERE id = ?`, dupID); err != nil {
			return fmt.Errorf("promote memory: %w", err)
		}
	}
	confirmedJSON, _ := json.Marshal(confirmed)
	if _, err := tx.Exec(`UPDATE memories SET scope = ?, scope_key = ?, confirmed_by = ?, updated_at = ? WHERE id = ?`,
		scope, scopeKey, string(confirmedJSON), now.UTC().Format(time.RFC3339), id); err != nil {
		return fmt.Errorf("promote memory: %w", err)
	}
	return tx.Commit()
}

// ConfirmMemory records that agent also holds this memory. Agents already
// listed (and blank labels) are ignored.
func (db *DB) ConfirmMemory(id int64, agent string) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("confirm memory: %w", err)
	}
	defer tx.Rollback()
	m, err := scanMemory(tx.QueryRow(`SELECT `+memoryColumns+` FROM memories WHERE id = ?`, id))
	if err != nil {
		return fmt.Errorf("confirm memory: %w", err)
	}
	confirmedJSON, _ := json.Marshal(appendUnique(m.ConfirmedBy, agent))
	if _, err := tx.Exec(`UPDATE memories SET confirmed_by = ? WHERE id = ?`, string(confirmedJSON), id); err != nil {
		return fmt.Errorf("confirm memory: %w", err)
	}
	return tx.Commit()
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, e := range list {
		if e == v {
			return list
		}
	}
	return append(list, v)
}

// DeleteMemory removes a single memory row by id.
func (db *DB) DeleteMemory(id int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

// SetMemoryConfidence updates a single memory's confidence state and
// updated_at timestamp.
func (db *DB) SetMemoryConfidence(id int64, confidence string, now time.Time) error {
	_, err := db.sqlDB.Exec(
		`UPDATE memories SET confidence = ?, updated_at = ? WHERE id = ?`,
		confidence, now.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("set memory confidence: %w", err)
	}
	return nil
}
