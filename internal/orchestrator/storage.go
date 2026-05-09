package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alecpullen/marshal/internal/pipeline"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// GraphStore provides SQLite persistence for task graphs.
type GraphStore struct {
	db *sql.DB
}

// NewGraphStore creates a new graph store.
func NewGraphStore(dbPath string) (*GraphStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening graph database: %w", err)
	}

	store := &GraphStore{db: db}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return store, nil
}

// NewGraphStoreFromDB creates a store from an existing database connection.
func NewGraphStoreFromDB(db *sql.DB) (*GraphStore, error) {
	store := &GraphStore{db: db}
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("initializing schema: %w", err)
	}
	return store, nil
}

// Close closes the database connection.
func (s *GraphStore) Close() error {
	return s.db.Close()
}

// initSchema creates the necessary tables.
func (s *GraphStore) initSchema() error {
	schema := `
-- Graph metadata
CREATE TABLE IF NOT EXISTS graphs (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	root_goal TEXT NOT NULL,
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	config_json TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

-- Individual tasks
CREATE TABLE IF NOT EXISTS graph_tasks (
	graph_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	role TEXT NOT NULL,
	goal TEXT NOT NULL,
	description TEXT,
	depends_on_json TEXT NOT NULL,
	files_json TEXT NOT NULL,
	output_schema TEXT,
	context_policy_json TEXT,
	max_iterations INTEGER,
	timeout_ms INTEGER,
	status TEXT NOT NULL,
	version INTEGER NOT NULL,
	parent_id TEXT,
	output_json TEXT,
	started_at INTEGER,
	completed_at INTEGER,
	error TEXT,
	PRIMARY KEY (graph_id, task_id),
	FOREIGN KEY (graph_id) REFERENCES graphs(id) ON DELETE CASCADE
);

-- Dependency edges
CREATE TABLE IF NOT EXISTS graph_edges (
	graph_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	depends_on TEXT NOT NULL,
	PRIMARY KEY (graph_id, task_id, depends_on),
	FOREIGN KEY (graph_id) REFERENCES graphs(id) ON DELETE CASCADE
);

-- Mutation history
CREATE TABLE IF NOT EXISTS graph_mutations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	graph_id TEXT NOT NULL,
	mutation_type TEXT NOT NULL,
	target_id TEXT,
	new_spec_json TEXT,
	new_edges_json TEXT,
	remove_edges_json TEXT,
	reason TEXT,
	trigger TEXT,
	timestamp INTEGER NOT NULL,
	FOREIGN KEY (graph_id) REFERENCES graphs(id) ON DELETE CASCADE
);

-- Indexes for faster lookups
CREATE INDEX IF NOT EXISTS idx_graphs_session ON graphs(session_id);
CREATE INDEX IF NOT EXISTS idx_graphs_status ON graphs(status);
CREATE INDEX IF NOT EXISTS idx_tasks_graph ON graph_tasks(graph_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON graph_tasks(status);
CREATE INDEX IF NOT EXISTS idx_edges_graph ON graph_edges(graph_id);
CREATE INDEX IF NOT EXISTS idx_mutations_graph ON graph_mutations(graph_id);
CREATE INDEX IF NOT EXISTS idx_mutations_timestamp ON graph_mutations(timestamp);
`

	_, err := s.db.Exec(schema)
	return err
}

// Save persists a graph to the database.
func (s *GraphStore) Save(g *Graph) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Save graph metadata
	configJSON, err := json.Marshal(g.Config)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	_, err = tx.Exec(
		`INSERT OR REPLACE INTO graphs 
		(id, session_id, root_goal, version, status, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.SessionID, g.RootGoal, g.Version, string(g.Status), configJSON,
		g.CreatedAt.Unix(), g.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("saving graph: %w", err)
	}

	// Delete existing tasks and edges for this graph
	_, err = tx.Exec(`DELETE FROM graph_tasks WHERE graph_id = ?`, g.ID)
	if err != nil {
		return fmt.Errorf("deleting old tasks: %w", err)
	}
	_, err = tx.Exec(`DELETE FROM graph_edges WHERE graph_id = ?`, g.ID)
	if err != nil {
		return fmt.Errorf("deleting old edges: %w", err)
	}

	// Save tasks
	for _, task := range g.Tasks {
		if err := s.saveTask(tx, g.ID, task); err != nil {
			return fmt.Errorf("saving task %s: %w", task.ID, err)
		}
	}

	// Save edges
	for taskID, deps := range g.Edges {
		for _, dep := range deps {
			_, err = tx.Exec(
				`INSERT INTO graph_edges (graph_id, task_id, depends_on) VALUES (?, ?, ?)`,
				g.ID, taskID, dep,
			)
			if err != nil {
				return fmt.Errorf("saving edge %s -> %s: %w", dep, taskID, err)
			}
		}
	}

	return tx.Commit()
}

// saveTask persists a single task.
func (s *GraphStore) saveTask(tx *sql.Tx, graphID string, task *pipeline.TaskSpec) error {
	dependsOnJSON, err := json.Marshal(task.DependsOn)
	if err != nil {
		return err
	}

	filesJSON, err := json.Marshal(task.Files)
	if err != nil {
		return err
	}

	var contextPolicyJSON []byte
	if task.ContextPolicy.Inherit != nil || task.ContextPolicy.Exclude != nil {
		contextPolicyJSON, err = json.Marshal(task.ContextPolicy)
		if err != nil {
			return err
		}
	}

	var outputJSON []byte
	if task.Output != nil {
		outputJSON, err = json.Marshal(task.Output)
		if err != nil {
			return err
		}
	}

	var startedAt, completedAt *int64
	if task.StartedAt != nil {
		t := task.StartedAt.Unix()
		startedAt = &t
	}
	if task.CompletedAt != nil {
		t := task.CompletedAt.Unix()
		completedAt = &t
	}

	_, err = tx.Exec(
		`INSERT INTO graph_tasks (
			graph_id, task_id, role, goal, description,
			depends_on_json, files_json, output_schema, context_policy_json,
			max_iterations, timeout_ms, status, version, parent_id,
			output_json, started_at, completed_at, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		graphID, task.ID, task.Role, task.Goal, task.Description,
		dependsOnJSON, filesJSON, string(task.OutputSchema), contextPolicyJSON,
		task.MaxIterations, int(task.Timeout.Milliseconds()),
		pipeline.TaskStateString(task.Status), task.Version, task.ParentID,
		outputJSON, startedAt, completedAt, task.Error,
	)

	return err
}

// Load retrieves a graph from the database.
func (s *GraphStore) Load(graphID string) (*Graph, error) {
	// Load graph metadata
	var g Graph
	var configJSON string
	var createdAt, updatedAt int64

	err := s.db.QueryRow(
		`SELECT id, session_id, root_goal, version, status, config_json, created_at, updated_at
		FROM graphs WHERE id = ?`,
		graphID,
	).Scan(&g.ID, &g.SessionID, &g.RootGoal, &g.Version, &g.Status, &configJSON, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("graph not found: %s", graphID)
	}
	if err != nil {
		return nil, fmt.Errorf("loading graph: %w", err)
	}

	if err := json.Unmarshal([]byte(configJSON), &g.Config); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	g.CreatedAt = time.Unix(createdAt, 0)
	g.UpdatedAt = time.Unix(updatedAt, 0)
	g.Tasks = make(map[string]*pipeline.TaskSpec)
	g.Edges = make(map[string][]string)

	// Load tasks
	rows, err := s.db.Query(
		`SELECT task_id, role, goal, description, depends_on_json, files_json,
			output_schema, context_policy_json, max_iterations, timeout_ms,
			status, version, parent_id, output_json, started_at, completed_at, error
		FROM graph_tasks WHERE graph_id = ?`,
		graphID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading tasks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var task pipeline.TaskSpec
		var dependsOnJSON, filesJSON, contextPolicyJSON, outputSchema, outputJSON string
		var maxIter int
		var timeoutMs int64
		var statusStr string
		var startedAt, completedAt *int64
		var taskError string

		err := rows.Scan(
			&task.ID, &task.Role, &task.Goal, &task.Description,
			&dependsOnJSON, &filesJSON, &outputSchema, &contextPolicyJSON,
			&maxIter, &timeoutMs, &statusStr, &task.Version, &task.ParentID,
			&outputJSON, &startedAt, &completedAt, &taskError,
		)
		if err != nil {
			continue
		}

		task.MaxIterations = maxIter
		task.Timeout = time.Duration(timeoutMs) * time.Millisecond
		task.Status = parseTaskState(statusStr)
		task.OutputSchema = []byte(outputSchema)
		task.Error = taskError

		if err := json.Unmarshal([]byte(dependsOnJSON), &task.DependsOn); err != nil {
			task.DependsOn = []string{}
		}
		if err := json.Unmarshal([]byte(filesJSON), &task.Files); err != nil {
			task.Files = []string{}
		}
		if contextPolicyJSON != "" {
			json.Unmarshal([]byte(contextPolicyJSON), &task.ContextPolicy)
		}
		if outputJSON != "" {
			json.Unmarshal([]byte(outputJSON), &task.Output)
		}

		if startedAt != nil {
			t := time.Unix(*startedAt, 0)
			task.StartedAt = &t
		}
		if completedAt != nil {
			t := time.Unix(*completedAt, 0)
			task.CompletedAt = &t
		}

		g.Tasks[task.ID] = &task
	}

	// Load edges
	edgeRows, err := s.db.Query(
		`SELECT task_id, depends_on FROM graph_edges WHERE graph_id = ?`,
		graphID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading edges: %w", err)
	}
	defer edgeRows.Close()

	for edgeRows.Next() {
		var taskID, dependsOn string
		if err := edgeRows.Scan(&taskID, &dependsOn); err != nil {
			continue
		}
		g.Edges[taskID] = append(g.Edges[taskID], dependsOn)
	}

	return &g, nil
}

// LoadSessionGraphs retrieves all graphs for a session.
func (s *GraphStore) LoadSessionGraphs(sessionID string) ([]*Graph, error) {
	rows, err := s.db.Query(
		`SELECT id FROM graphs WHERE session_id = ? ORDER BY created_at DESC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying graphs: %w", err)
	}
	defer rows.Close()

	var graphs []*Graph
	for rows.Next() {
		var graphID string
		if err := rows.Scan(&graphID); err != nil {
			continue
		}
		g, err := s.Load(graphID)
		if err != nil {
			continue
		}
		graphs = append(graphs, g)
	}

	return graphs, nil
}

// Delete removes a graph from storage.
func (s *GraphStore) Delete(graphID string) error {
	_, err := s.db.Exec(`DELETE FROM graphs WHERE id = ?`, graphID)
	return err
}

// List returns all graph IDs with optional filtering.
func (s *GraphStore) List(status string, limit int) ([]string, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = `SELECT id FROM graphs WHERE status = ? ORDER BY updated_at DESC LIMIT ?`
		args = []interface{}{status, limit}
	} else {
		query = `SELECT id FROM graphs ORDER BY updated_at DESC LIMIT ?`
		args = []interface{}{limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing graphs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}

	return ids, nil
}

// SaveMutation persists a single mutation to the history.
func (s *GraphStore) SaveMutation(graphID string, m pipeline.GraphMutation) error {
	var newSpecJSON []byte
	if m.NewSpec != nil {
		var err error
		newSpecJSON, err = json.Marshal(m.NewSpec)
		if err != nil {
			return fmt.Errorf("marshaling new spec: %w", err)
		}
	}

	newEdgesJSON, err := json.Marshal(m.NewEdges)
	if err != nil {
		return fmt.Errorf("marshaling new edges: %w", err)
	}

	removeEdgesJSON, err := json.Marshal(m.RemoveEdges)
	if err != nil {
		return fmt.Errorf("marshaling remove edges: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO graph_mutations 
		(graph_id, mutation_type, target_id, new_spec_json, new_edges_json, remove_edges_json, reason, trigger, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		graphID, string(m.Type), m.TargetID, newSpecJSON, newEdgesJSON, removeEdgesJSON,
		m.Reason, m.Trigger, m.Timestamp.Unix(),
	)

	return err
}

// LoadMutations retrieves mutation history for a graph.
func (s *GraphStore) LoadMutations(graphID string) ([]pipeline.GraphMutation, error) {
	rows, err := s.db.Query(
		`SELECT mutation_type, target_id, new_spec_json, new_edges_json, remove_edges_json, reason, trigger, timestamp
		FROM graph_mutations WHERE graph_id = ? ORDER BY timestamp ASC`,
		graphID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading mutations: %w", err)
	}
	defer rows.Close()

	var mutations []pipeline.GraphMutation
	for rows.Next() {
		var m pipeline.GraphMutation
		var typeStr, newSpecJSON, newEdgesJSON, removeEdgesJSON string
		var timestamp int64

		err := rows.Scan(&typeStr, &m.TargetID, &newSpecJSON, &newEdgesJSON, &removeEdgesJSON,
			&m.Reason, &m.Trigger, &timestamp)
		if err != nil {
			continue
		}

		m.Type = pipeline.MutationType(typeStr)
		m.Timestamp = time.Unix(timestamp, 0)

		if newSpecJSON != "" {
			json.Unmarshal([]byte(newSpecJSON), &m.NewSpec)
		}
		json.Unmarshal([]byte(newEdgesJSON), &m.NewEdges)
		json.Unmarshal([]byte(removeEdgesJSON), &m.RemoveEdges)

		mutations = append(mutations, m)
	}

	return mutations, nil
}

// UpdateTaskStatus updates only the status of a task (efficient partial update).
func (s *GraphStore) UpdateTaskStatus(graphID, taskID string, status pipeline.TaskState) error {
	now := time.Now().Unix()

	var startedAt, completedAt interface{}
	if status == pipeline.TaskRunning {
		startedAt = now
	}
	if status.IsTerminal() {
		completedAt = now
	}

	_, err := s.db.Exec(
		`UPDATE graph_tasks SET status = ?, started_at = COALESCE(?, started_at), completed_at = ?
		WHERE graph_id = ? AND task_id = ?`,
		pipeline.TaskStateString(status), startedAt, completedAt, graphID, taskID,
	)

	return err
}

// UpdateGraphStatus updates only the graph status (efficient partial update).
func (s *GraphStore) UpdateGraphStatus(graphID string, status GraphStatus) error {
	_, err := s.db.Exec(
		`UPDATE graphs SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().Unix(), graphID,
	)
	return err
}

// parseTaskState converts a string to TaskState.
func parseTaskState(s string) pipeline.TaskState {
	switch s {
	case "pending":
		return pipeline.TaskPending
	case "running":
		return pipeline.TaskRunning
	case "passed":
		return pipeline.TaskPassed
	case "failed":
		return pipeline.TaskFailed
	case "waiting":
		return pipeline.TaskWaiting
	case "blocked":
		return pipeline.TaskBlocked
	case "cancelled":
		return pipeline.TaskCancelled
	case "superseded":
		return pipeline.TaskSuperseded
	default:
		return pipeline.TaskPending
	}
}

// CreateSessionGraphStore creates a graph store in a session's directory.
func CreateSessionGraphStore(sessionID, sessionsDir string) (*GraphStore, error) {
	dbPath := fmt.Sprintf("%s/%s/graph.db", sessionsDir, sessionID)
	return NewGraphStore(dbPath)
}

// GenerateGraphID creates a new unique graph ID.
func GenerateGraphID() string {
	return fmt.Sprintf("graph-%s", uuid.New().String()[:8])
}

// WithTx executes a function within a transaction.
func (s *GraphStore) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

// GetGraphStats returns statistics for all graphs.
func (s *GraphStore) GetGraphStats() (total int, byStatus map[string]int, err error) {
	byStatus = make(map[string]int)

	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM graphs GROUP BY status`)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	total = 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			continue
		}
		byStatus[status] = count
		total += count
	}

	return total, byStatus, nil
}

// CleanupOldGraphs removes graphs older than the specified duration.
func (s *GraphStore) CleanupOldGraphs(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	
	result, err := s.db.Exec(
		`DELETE FROM graphs WHERE updated_at < ?`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}

	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// Vacuum reclaims storage space.
func (s *GraphStore) Vacuum() error {
	_, err := s.db.Exec(`VACUUM`)
	return err
}
