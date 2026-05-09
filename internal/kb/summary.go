// Package kb implements Phase 3.8: KB Derived Summaries
// This file contains summary types and storage for LLM-generated code understanding.
package kb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Staleness thresholds
const (
	SummaryFreshDuration    = 24 * time.Hour  // Warning after 24h
	SummaryStaleDuration    = 7 * 24 * time.Hour // Considered stale after 7 days
)

// FileSummary is an LLM-generated summary of a source file.
// Generated on-demand and cached with content-based invalidation.
type FileSummary struct {
	Path           string    `json:"path"`
	ContentHash    string    `json:"content_hash"`     // Hash of source file content
	SymbolsHash    string    `json:"symbols_hash"`     // Hash of symbol set (for invalidation)
	Purpose        string    `json:"purpose"`          // 1-2 sentence description
	PublicSurface  []string  `json:"public_surface"`   // Exported API (verified against symbols)
	DependsOn      []string  `json:"depends_on"`       // Internal/external dependencies
	RelatedTo      []string  `json:"related_to"`       // Related files
	Notes          string    `json:"notes"`            // Implementation notes
	GeneratedAt    time.Time `json:"generated_at"`
	GeneratedBy    string    `json:"generated_by"`     // Model identifier
	ModelBinding   string    `json:"model_binding"`    // Which model config was used
	CostCents      int       `json:"cost_cents"`       // Cost of generation (in cents)
}

// IsStale returns true if the summary is older than 24 hours.
func (fs *FileSummary) IsStale() bool {
	return time.Since(fs.GeneratedAt) > SummaryFreshDuration
}

// IsVeryStale returns true if the summary is older than 7 days.
func (fs *FileSummary) IsVeryStale() bool {
	return time.Since(fs.GeneratedAt) > SummaryStaleDuration
}

// StalenessLabel returns a human-readable staleness indicator.
func (fs *FileSummary) StalenessLabel() string {
	age := time.Since(fs.GeneratedAt)
	
	switch {
	case age > SummaryStaleDuration:
		return "stale"
	case age > SummaryFreshDuration:
		return fmt.Sprintf("%dh old", int(age.Hours()))
	case age > time.Hour:
		return fmt.Sprintf("%dm old", int(age.Minutes()))
	default:
		return "fresh"
	}
}

// NeedsRegeneration returns true if summary should be regenerated.
// Checks both age and symbol set changes.
func (fs *FileSummary) NeedsRegeneration(currentSymbolsHash string) bool {
	// Always regenerate if very stale
	if fs.IsVeryStale() {
		return true
	}
	
	// Regenerate if symbol set changed (structural change)
	if fs.SymbolsHash != currentSymbolsHash {
		return true
	}
	
	return false
}

// ValidatePublicSurface verifies that claimed public surface exists in actual symbols.
// Returns error if LLM hallucinated exports.
func (fs *FileSummary) ValidatePublicSurface(actualSymbols []Symbol) error {
	// Build set of actual exported symbols
	exportedSet := make(map[string]bool)
	for _, sym := range actualSymbols {
		if sym.Exported {
			exportedSet[sym.Name] = true
		}
	}
	
	// Verify each claimed public symbol exists and is exported
	for _, claimed := range fs.PublicSurface {
		if !exportedSet[claimed] {
			return fmt.Errorf("public surface claims '%s' but not found in exported symbols", claimed)
		}
	}
	
	return nil
}

// PackageSummary aggregates understanding of a package/directory.
type PackageSummary struct {
	Path          string    `json:"path"`
	Files         []string  `json:"files"`            // Included file paths
	SymbolsHash   string    `json:"symbols_hash"`     // Hash of all package symbols
	Purpose       string    `json:"purpose"`          // Package responsibility
	PublicAPI     []string  `json:"public_api"`       // Package-level exports
	EntryPoints   []string  `json:"entry_points"`     // Main exports (functions, types)
	Architecture  string    `json:"architecture"`     // Structure description
	Subpackages   []string  `json:"subpackages"`      // Child packages
	Dependencies  []string  `json:"dependencies"`     // External deps
	GeneratedAt   time.Time `json:"generated_at"`
	CostCents     int       `json:"cost_cents"`
}

// IsStale returns true if package summary is older than 24h.
func (ps *PackageSummary) IsStale() bool {
	return time.Since(ps.GeneratedAt) > SummaryFreshDuration
}

// ProjectMap provides high-level project understanding.
type ProjectMap struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	RootPath      string            `json:"root_path"`
	Languages     map[string]int    `json:"languages"`      // Symbol counts by language
	MajorModules  []ModuleSummary   `json:"major_modules"`  // Top-level packages
	Conventions   []string          `json:"conventions"`    // Detected patterns
	EntryPoints   []string          `json:"entry_points"`   // Main package exports
	GeneratedAt   time.Time         `json:"generated_at"`
	SymbolsHash   string            `json:"symbols_hash"`   // Hash of all project symbols
}

// ModuleSummary describes a top-level module/package.
type ModuleSummary struct {
	Path        string   `json:"path"`
	Purpose     string   `json:"purpose"`
	EntryPoints []string `json:"entry_points"`
	FileCount   int      `json:"file_count"`
	SymbolCount int      `json:"symbol_count"`
}

// IsStale returns true if project map is older than 24h.
func (pm *ProjectMap) IsStale() bool {
	return time.Since(pm.GeneratedAt) > SummaryFreshDuration
}

// ExtractedConvention captures a detected codebase pattern with evidence.
// Requires explicit user approval before being used by agents.
type ExtractedConvention struct {
	ID             string    `json:"id"`
	Topic          string    `json:"topic"`           // e.g., "error handling", "naming"
	Description    string    `json:"description"`     // Human-readable explanation
	Evidence       []CodeRef `json:"evidence"`        // Supporting code samples
	Confidence     float64   `json:"confidence"`      // 0.0-1.0
	MinEvidence    int       `json:"min_evidence"`    // Required samples (default 3)
	ApprovedByUser bool      `json:"approved_by_user"` // MUST be false until approved!
	GeneratedAt    time.Time `json:"generated_at"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
}

// CanBeUsed returns true only if approved by user.
func (ec *ExtractedConvention) CanBeUsed() bool {
	return ec.ApprovedByUser
}

// HasMinimumEvidence returns true if enough samples collected.
func (ec *ExtractedConvention) HasMinimumEvidence() bool {
	return len(ec.Evidence) >= ec.MinEvidence
}

// CodeRef references a specific code location as evidence.
type CodeRef struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	Snippet  string `json:"snippet"`  // 3-5 lines of context
}

// SummaryStore extends IndexStore with summary storage capabilities.
type SummaryStore struct {
	*IndexStore
}

// NewSummaryStore creates a summary-aware store.
func NewSummaryStore(db *sql.DB) (*SummaryStore, error) {
	store, err := NewIndexStore(db)
	if err != nil {
		return nil, err
	}
	
	if err := store.initSummarySchema(); err != nil {
		return nil, fmt.Errorf("initializing summary schema: %w", err)
	}
	
	return &SummaryStore{store}, nil
}

// initSummarySchema adds summary tables to the database.
func (s *IndexStore) initSummarySchema() error {
	schema := `
-- File summaries with content tracking
CREATE TABLE IF NOT EXISTS kb_file_summaries (
    file_path TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    symbols_hash TEXT NOT NULL,
    summary_json TEXT NOT NULL,
    generated_at INTEGER NOT NULL,
    cost_cents INTEGER DEFAULT 0
);

-- Package summaries
CREATE TABLE IF NOT EXISTS kb_package_summaries (
    package_path TEXT PRIMARY KEY,
    symbols_hash TEXT NOT NULL,
    files_json TEXT NOT NULL,  -- JSON array of included files
    summary_json TEXT NOT NULL,
    generated_at INTEGER NOT NULL,
    cost_cents INTEGER DEFAULT 0
);

-- Project maps (only one active at a time)
CREATE TABLE IF NOT EXISTS kb_project_maps (
    id INTEGER PRIMARY KEY CHECK (id = 1),  -- Singleton
    project_hash TEXT NOT NULL,
    map_json TEXT NOT NULL,
    generated_at INTEGER NOT NULL,
    cost_cents INTEGER DEFAULT 0
);

-- Extracted conventions with approval workflow
CREATE TABLE IF NOT EXISTS kb_conventions (
    id TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    description TEXT NOT NULL,
    evidence_json TEXT NOT NULL,
    confidence REAL NOT NULL,
    min_evidence INTEGER DEFAULT 3,
    approved_by_user INTEGER DEFAULT 0,
    generated_at INTEGER NOT NULL,
    approved_at INTEGER
);

-- Indexes for faster lookups
CREATE INDEX IF NOT EXISTS idx_kb_file_summaries_generated 
    ON kb_file_summaries(generated_at);
CREATE INDEX IF NOT EXISTS idx_kb_conventions_topic 
    ON kb_conventions(topic);
CREATE INDEX IF NOT EXISTS idx_kb_conventions_approved 
    ON kb_conventions(approved_by_user);
`
	
	_, err := s.db.Exec(schema)
	return err
}

// GetFileSummary retrieves a file summary by path.
func (s *SummaryStore) GetFileSummary(filePath string) (*FileSummary, error) {
	var summaryJSON string
	var generatedAt int64
	
	err := s.db.QueryRow(
		`SELECT summary_json, generated_at FROM kb_file_summaries WHERE file_path = ?`,
		filePath,
	).Scan(&summaryJSON, &generatedAt)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	
	var summary FileSummary
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return nil, fmt.Errorf("unmarshaling file summary: %w", err)
	}
	
	return &summary, nil
}

// StoreFileSummary saves a file summary.
func (s *SummaryStore) StoreFileSummary(summary *FileSummary) error {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshaling file summary: %w", err)
	}
	
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO kb_file_summaries 
		(file_path, content_hash, symbols_hash, summary_json, generated_at, cost_cents)
		VALUES (?, ?, ?, ?, ?, ?)`,
		summary.Path, summary.ContentHash, summary.SymbolsHash,
		summaryJSON, summary.GeneratedAt.Unix(), summary.CostCents,
	)
	
	return err
}

// GetPackageSummary retrieves a package summary.
func (s *SummaryStore) GetPackageSummary(packagePath string) (*PackageSummary, error) {
	var summaryJSON string
	var generatedAt int64
	
	err := s.db.QueryRow(
		`SELECT summary_json, generated_at FROM kb_package_summaries WHERE package_path = ?`,
		packagePath,
	).Scan(&summaryJSON, &generatedAt)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	
	var summary PackageSummary
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return nil, fmt.Errorf("unmarshaling package summary: %w", err)
	}
	
	return &summary, nil
}

// ListFileSummariesByPackage returns all file summaries in a package/directory.
func (s *SummaryStore) ListFileSummariesByPackage(packagePath string) ([]*FileSummary, error) {
	// Match files where path starts with packagePath + "/"
	pattern := packagePath + "/%"

	rows, err := s.db.Query(
		`SELECT file_path, content_hash, symbols_hash, summary_json, generated_at, cost_cents
		 FROM kb_file_summaries WHERE file_path LIKE ?`,
		pattern,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []*FileSummary
	for rows.Next() {
		var summaryJSON string
		var generatedAt int64
		var summary FileSummary

		err := rows.Scan(
			&summary.Path, &summary.ContentHash, &summary.SymbolsHash,
			&summaryJSON, &generatedAt, &summary.CostCents,
		)
		if err != nil {
			continue
		}

		if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
			continue
		}
		summary.GeneratedAt = time.Unix(generatedAt, 0)
		summaries = append(summaries, &summary)
	}

	return summaries, rows.Err()
}

// StorePackageSummary saves a package summary.
func (s *SummaryStore) StorePackageSummary(summary *PackageSummary) error {
	filesJSON, err := json.Marshal(summary.Files)
	if err != nil {
		return fmt.Errorf("marshaling files: %w", err)
	}
	
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshaling package summary: %w", err)
	}
	
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO kb_package_summaries 
		(package_path, symbols_hash, files_json, summary_json, generated_at, cost_cents)
		VALUES (?, ?, ?, ?, ?, ?)`,
		summary.Path, summary.SymbolsHash, filesJSON, summaryJSON,
		summary.GeneratedAt.Unix(), summary.CostCents,
	)
	
	return err
}

// GetProjectMap retrieves the current project map.
func (s *SummaryStore) GetProjectMap() (*ProjectMap, error) {
	var mapJSON string
	var generatedAt int64
	
	err := s.db.QueryRow(
		`SELECT map_json, generated_at FROM kb_project_maps WHERE id = 1`,
	).Scan(&mapJSON, &generatedAt)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	
	var projectMap ProjectMap
	if err := json.Unmarshal([]byte(mapJSON), &projectMap); err != nil {
		return nil, fmt.Errorf("unmarshaling project map: %w", err)
	}
	
	return &projectMap, nil
}

// StoreProjectMap saves the project map.
func (s *SummaryStore) StoreProjectMap(projectMap *ProjectMap) error {
	mapJSON, err := json.Marshal(projectMap)
	if err != nil {
		return fmt.Errorf("marshaling project map: %w", err)
	}
	
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO kb_project_maps 
		(id, project_hash, map_json, generated_at, cost_cents)
		VALUES (1, ?, ?, ?, ?)`,
		projectMap.SymbolsHash, mapJSON, projectMap.GeneratedAt.Unix(), 0,
	)
	
	return err
}

// GetConvention retrieves a convention by ID.
func (s *SummaryStore) GetConvention(id string) (*ExtractedConvention, error) {
	var conv ExtractedConvention
	var evidenceJSON string
	var generatedAt, approvedAt int64
	var approvedInt int
	
	err := s.db.QueryRow(
		`SELECT id, topic, description, evidence_json, confidence, min_evidence,
		        approved_by_user, generated_at, approved_at
		 FROM kb_conventions WHERE id = ?`,
		id,
	).Scan(
		&conv.ID, &conv.Topic, &conv.Description, &evidenceJSON, &conv.Confidence,
		&conv.MinEvidence, &approvedInt, &generatedAt, &approvedAt,
	)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	
	conv.ApprovedByUser = approvedInt == 1
	conv.GeneratedAt = time.Unix(generatedAt, 0)
	if approvedAt > 0 {
		approvedTime := time.Unix(approvedAt, 0)
		conv.ApprovedAt = &approvedTime
	}
	
	if err := json.Unmarshal([]byte(evidenceJSON), &conv.Evidence); err != nil {
		return nil, fmt.Errorf("unmarshaling evidence: %w", err)
	}
	
	return &conv, nil
}

// ListConventions returns all conventions, optionally filtered by approval status.
func (s *SummaryStore) ListConventions(approvedOnly bool) ([]*ExtractedConvention, error) {
	var query string
	var args []interface{}
	
	if approvedOnly {
		query = `SELECT id, topic, description, evidence_json, confidence, min_evidence,
		         approved_by_user, generated_at, approved_at
		         FROM kb_conventions WHERE approved_by_user = 1`
	} else {
		query = `SELECT id, topic, description, evidence_json, confidence, min_evidence,
		         approved_by_user, generated_at, approved_at
		         FROM kb_conventions`
	}
	
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var conventions []*ExtractedConvention
	for rows.Next() {
		var conv ExtractedConvention
		var evidenceJSON string
		var generatedAt, approvedAt int64
		var approvedInt int
		
		err := rows.Scan(
			&conv.ID, &conv.Topic, &conv.Description, &evidenceJSON, &conv.Confidence,
			&conv.MinEvidence, &approvedInt, &generatedAt, &approvedAt,
		)
		if err != nil {
			continue
		}
		
		conv.ApprovedByUser = approvedInt == 1
		conv.GeneratedAt = time.Unix(generatedAt, 0)
		if approvedAt > 0 {
			approvedTime := time.Unix(approvedAt, 0)
			conv.ApprovedAt = &approvedTime
		}
		
		if err := json.Unmarshal([]byte(evidenceJSON), &conv.Evidence); err != nil {
			continue
		}
		
		conventions = append(conventions, &conv)
	}
	
	return conventions, rows.Err()
}

// StoreConvention saves a convention.
func (s *SummaryStore) StoreConvention(conv *ExtractedConvention) error {
	evidenceJSON, err := json.Marshal(conv.Evidence)
	if err != nil {
		return fmt.Errorf("marshaling evidence: %w", err)
	}
	
	approvedInt := 0
	if conv.ApprovedByUser {
		approvedInt = 1
	}
	
	var approvedAt int64
	if conv.ApprovedAt != nil {
		approvedAt = conv.ApprovedAt.Unix()
	}
	
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO kb_conventions 
		(id, topic, description, evidence_json, confidence, min_evidence,
		 approved_by_user, generated_at, approved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		conv.ID, conv.Topic, conv.Description, evidenceJSON, conv.Confidence,
		conv.MinEvidence, approvedInt, conv.GeneratedAt.Unix(), approvedAt,
	)
	
	return err
}

// ApproveConvention marks a convention as approved by user.
func (s *SummaryStore) ApproveConvention(id string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE kb_conventions 
		SET approved_by_user = 1, approved_at = ?
		WHERE id = ?`,
		now, id,
	)
	return err
}

// DeleteConvention removes a convention.
func (s *SummaryStore) DeleteConvention(id string) error {
	_, err := s.db.Exec(`DELETE FROM kb_conventions WHERE id = ?`, id)
	return err
}

// GetSummaryStats returns statistics about summary coverage and costs.
func (s *SummaryStore) GetSummaryStats() (fileCount, packageCount, conventionCount int, totalCostCents int, err error) {
	// File summaries
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(cost_cents), 0) FROM kb_file_summaries`).Scan(&fileCount, &totalCostCents)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	
	// Package summaries
	err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(cost_cents), 0) FROM kb_package_summaries`).Scan(&packageCount, &packageCost)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	totalCostCents += packageCost
	
	// Conventions
	err = s.db.QueryRow(`SELECT COUNT(*) FROM kb_conventions WHERE approved_by_user = 1`).Scan(&conventionCount)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	
	return fileCount, packageCount, conventionCount, totalCostCents, nil
}

var packageCost int  // Helper for scan above

// HashSymbols computes a hash of a symbol set for invalidation detection.
func HashSymbols(symbols []Symbol) string {
	h := sha256.New()
	for _, sym := range symbols {
		h.Write([]byte(sym.Name))
		h.Write([]byte(sym.Kind))
		h.Write([]byte{byte(sym.Range.StartLine)})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]  // First 16 chars sufficient
}

// HashContent computes a hash of file content.
func HashContent(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])[:16]
}