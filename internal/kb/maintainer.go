package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/alecpullen/marshal/internal/git"
)

// Maintainer watches the filesystem and keeps the symbol index up-to-date.
// It uses fsnotify for efficient change detection and BLAKE3 hashing for
// content-based deduplication.
// Phase 3.8: Also manages summary generation queue and cascade triggers.
type Maintainer struct {
	store     *IndexStore
	parserReg *ParserRegistry
	watcher   *fsnotify.Watcher
	ignore    *git.Ignorer

	// Phase 3.8: Summary infrastructure
	summariser *Summariser
	extractor  *ConventionExtractor

	rootPath    string
	isRunning   bool
	stopChan    chan struct{}
	eventChan   chan fsnotify.Event
	wg          sync.WaitGroup

	// Debounce duration for batching rapid changes
	debounceDuration time.Duration

	// Changed files pending reindex
	pendingChanges map[string]time.Time
	mu             sync.Mutex

	// Phase 3.8: Summary queue
	summaryQueue     chan string // file paths queued for summary generation
	summaryWg        sync.WaitGroup
	summaryThreshold int         // trigger package summary after N file summaries
}

// MaintainerOption configures the maintainer.
type MaintainerOption func(*Maintainer)

// WithDebounce sets the debounce duration for file changes.
func WithDebounce(d time.Duration) MaintainerOption {
	return func(m *Maintainer) {
		m.debounceDuration = d
	}
}

// WithSummaryThreshold sets the number of file summaries to trigger package summary.
func WithSummaryThreshold(n int) MaintainerOption {
	return func(m *Maintainer) {
		m.summaryThreshold = n
	}
}

// NewMaintainer creates a new maintainer instance.
// Phase 3.8: summary infrastructure optional (nil ok for symbol-only mode).
func NewMaintainer(store *IndexStore, parserReg *ParserRegistry, rootPath string, ignore *git.Ignorer, opts ...MaintainerOption) (*Maintainer, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	m := &Maintainer{
		store:            store,
		parserReg:        parserReg,
		watcher:          watcher,
		ignore:           ignore,
		rootPath:         rootPath,
		stopChan:         make(chan struct{}),
		eventChan:        make(chan fsnotify.Event, 100),
		pendingChanges:   make(map[string]time.Time),
		debounceDuration: 500 * time.Millisecond,
		summaryQueue:     make(chan string, 100),
		summaryThreshold: 3, // Default: trigger package summary after 3 files
	}

	for _, opt := range opts {
		opt(m)
	}

	return m, nil
}

// SetSummariser sets the summariser for summary generation.
func (m *Maintainer) SetSummariser(s *Summariser) {
	m.summariser = s
}

// SetConventionExtractor sets the convention extractor.
func (m *Maintainer) SetConventionExtractor(e *ConventionExtractor) {
	m.extractor = e
}

// Start begins watching the repository for file changes.
// It recursively adds all directories to the watcher.
// Phase 3.8: Also starts summary queue processor if summariser configured.
func (m *Maintainer) Start() error {
	if m.isRunning {
		return nil
	}

	m.isRunning = true

	// Add root directory and all subdirectories to watcher
	if err := m.addWatchRecursive(m.rootPath); err != nil {
		return fmt.Errorf("adding watch: %w", err)
	}

	// Start event processing goroutines
	m.wg.Add(2)
	go m.watchLoop()
	go m.processLoop()

	// Phase 3.8: Start summary queue processor if summariser configured
	if m.summariser != nil {
		m.summaryWg.Add(1)
		go m.summaryQueueProcessor()
	}

	return nil
}

// Stop stops the maintainer and cleans up resources.
// Phase 3.8: Also stops summary queue processor.
func (m *Maintainer) Stop() error {
	if !m.isRunning {
		return nil
	}

	m.isRunning = false
	close(m.stopChan)

	// Phase 3.8: Close summary queue and wait for processor
	if m.summariser != nil {
		close(m.summaryQueue)
		m.summaryWg.Wait()
	}

	m.wg.Wait()

	return m.watcher.Close()
}

// addWatchRecursive recursively adds directories to the watcher.
func (m *Maintainer) addWatchRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if !info.IsDir() {
			return nil
		}

		// Skip ignored directories
		rel, _ := filepath.Rel(m.rootPath, path)
		if m.ignore != nil && m.ignore.Match(rel) {
			return filepath.SkipDir
		}

		// Skip common non-source directories
		base := filepath.Base(path)
		if shouldSkipDir(base) {
			return filepath.SkipDir
		}

		if err := m.watcher.Add(path); err != nil {
			// Log but continue
			return nil
		}

		return nil
	})
}

// shouldSkipDir returns true for directories that shouldn't be watched.
func shouldSkipDir(name string) bool {
	skipDirs := []string{
		".git", ".svn", ".hg",
		"node_modules", "vendor",
		".swarm", ".marshal",
		"dist", "build", "out",
		"__pycache__", ".pytest_cache",
		".next", ".nuxt",
		"target", // Rust
		"bin", "obj", // C#
	}
	
	for _, skip := range skipDirs {
		if name == skip {
			return true
		}
	}
	return false
}

// watchLoop processes fsnotify events.
func (m *Maintainer) watchLoop() {
	defer m.wg.Done()

	for {
		select {
		case <-m.stopChan:
			return
			
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			
			// Filter relevant events
			if !m.isRelevantEvent(event) {
				continue
			}
			
			// Send to processing channel
			select {
			case m.eventChan <- event:
			case <-m.stopChan:
				return
			}
			
		case err, ok := <-m.watcher.Errors:
			if !ok {
				return
			}
			// Log error but continue
			_ = err
		}
	}
}

// isRelevantEvent checks if an fsnotify event should be processed.
func (m *Maintainer) isRelevantEvent(event fsnotify.Event) bool {
	// Only care about write, create, and remove events
	if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) && !event.Has(fsnotify.Remove) {
		return false
	}

	// Skip ignored files
	rel, _ := filepath.Rel(m.rootPath, event.Name)
	if m.ignore != nil && m.ignore.Match(rel) {
		return false
	}

	// Check if file extension is supported
	parser := m.parserReg.GetParser(event.Name)
	if parser == nil {
		return false // Not a source file we can parse
	}

	return true
}

// processLoop batches and processes file changes.
func (m *Maintainer) processLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.debounceDuration)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			// Process any pending changes before stopping
			m.processPendingChanges()
			return
			
		case event := <-m.eventChan:
			m.mu.Lock()
			m.pendingChanges[event.Name] = time.Now()
			m.mu.Unlock()
			
		case <-ticker.C:
			m.processPendingChanges()
		}
	}
}

// processPendingChanges reindexes files that have changed.
func (m *Maintainer) processPendingChanges() {
	m.mu.Lock()
	changes := m.pendingChanges
	m.pendingChanges = make(map[string]time.Time)
	m.mu.Unlock()

	if len(changes) == 0 {
		return
	}

	for path := range changes {
		if err := m.reindexFile(path); err != nil {
			// Log error but continue with other files
			_ = err
		}
	}
}

// reindexFile reindexes a single file if its content has changed.
func (m *Maintainer) reindexFile(path string) error {
	// Check if file still exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File was deleted, remove from index
			return m.store.Remove(path)
		}
		return err
	}

	if info.IsDir() {
		return nil // Skip directories
	}

	// Read file content
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	// Compute hash
	hash := computeHash(content)

	// Check if already indexed with same hash
	existing, err := m.store.Get(path)
	if err != nil {
		return fmt.Errorf("checking existing index: %w", err)
	}

	if existing != nil && existing.ContentHash == hash {
		// No change, skip
		return nil
	}

	// Parse file
	parser := m.parserReg.GetParser(path)
	if parser == nil {
		return nil // Unsupported file type
	}

	parsed, err := parser.Parse(content, path)
	if err != nil {
		return fmt.Errorf("parsing file: %w", err)
	}

	// Create index entry
	entry := &IndexEntry{
		FilePath:    path,
		ContentHash: hash,
		Parser:      parser.Name(),
		Symbols:     parsed.Symbols,
		Imports:     parsed.Imports,
		IndexedAt:   time.Now(),
	}

	// Store entry
	if err := m.store.Put(entry); err != nil {
		return fmt.Errorf("storing index entry: %w", err)
	}

	// Phase 3.8: Queue for summary generation if summariser configured
	if m.summariser != nil {
		select {
		case m.summaryQueue <- path:
			// Queued successfully
		default:
			// Queue full, skip (summary will be generated on-demand)
		}
	}

	return nil
}

// summaryQueueProcessor processes the summary queue.
// It generates summaries respecting budget constraints.
func (m *Maintainer) summaryQueueProcessor() {
	defer m.summaryWg.Done()

	for {
		select {
		case <-m.stopChan:
			return

		case path, ok := <-m.summaryQueue:
			if !ok {
				return
			}

			// Generate summary with budget check
			_, err := m.summariser.SummariseFile(path)
			if err != nil {
				if err == ErrBudgetExceeded {
					// Budget exceeded, stop processing queue
					return
				}
				// Other errors, log but continue
				continue
			}

			// Trigger cascade check
			m.maybeTriggerPackageSummary(path)
		}
	}
}

// maybeTriggerPackageSummary checks if package summary should be regenerated.
// Triggers when m.summaryThreshold file summaries updated in a package.
func (m *Maintainer) maybeTriggerPackageSummary(filePath string) {
	if m.summariser == nil {
		return
	}

	dir := filepath.Dir(filePath)
	if dir == "" || dir == "." {
		return
	}

	// Count how many files in this directory have recent summaries
	files, err := m.summariser.store.ListFileSummariesByPackage(dir)
	if err != nil {
		return
	}

	// Count summaries generated in last 5 minutes
	recentCount := 0
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, f := range files {
		if f.GeneratedAt.After(cutoff) {
			recentCount++
		}
	}

	// Trigger package summary if threshold reached
	if recentCount >= m.summaryThreshold {
		// Queue package summary generation (async)
		go func(pkg string) {
			_, _ = m.summariser.SummarisePackage(pkg)
		}(dir)
	}
}

// computeHash computes a BLAKE3 hash of content.
// In a real implementation, this would use github.com/zeebo/blake3.
// For now, we use a simple hash as a placeholder.
func computeHash(content []byte) string {
	// Simple hash for now - replace with BLAKE3
	// This is just for demonstration; real code should use:
	// hash := blake3.Sum256(content)
	// return hex.EncodeToString(hash[:])
	
	h := 0
	for _, b := range content {
		h = h*31 + int(b)
	}
	return fmt.Sprintf("%x", h)
}

// Bootstrap performs initial indexing of the repository.
// It indexes files in order of priority (recently modified first).
func (m *Maintainer) Bootstrap() error {
	// Collect all source files
	var files []fileInfo
	err := filepath.Walk(m.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(m.rootPath, path)
			if m.ignore != nil && m.ignore.Match(rel) {
				return filepath.SkipDir
			}
			if shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if it's a supported source file
		if m.parserReg.GetParser(path) == nil {
			return nil
		}

		files = append(files, fileInfo{
			path:    path,
			modTime: info.ModTime(),
		})

		return nil
	})
	if err != nil {
		return err
	}

	// Sort by modification time (recent first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	// Index files
	for _, file := range files {
		if err := m.reindexFile(file.path); err != nil {
			// Log but continue
			continue
		}
	}

	return nil
}

type fileInfo struct {
	path    string
	modTime time.Time
}

// FullRebuild clears and rebuilds the entire index.
func (m *Maintainer) FullRebuild() error {
	// Clear existing index
	if err := m.store.Clear(); err != nil {
		return fmt.Errorf("clearing index: %w", err)
	}

	// Rebuild from scratch
	return m.Bootstrap()
}

// Stats returns current indexing statistics.
func (m *Maintainer) Stats() (totalFiles, totalSymbols int, lastIndexed time.Time, err error) {
	return m.store.Stats()
}

// AddWatch adds a new path to be watched.
func (m *Maintainer) AddWatch(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return m.addWatchRecursive(path)
	}

	return m.watcher.Add(path)
}

// RemoveWatch removes a path from being watched.
func (m *Maintainer) RemoveWatch(path string) error {
	return m.watcher.Remove(path)
}