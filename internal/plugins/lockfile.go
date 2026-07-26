// Package plugins implements third-party plugin installation, integrity
// pinning, and loading. Phase 1 supports skill bundles only; the installer
// and lockfile are designed to carry commands/hooks/MCP content in phase 2.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LockEntry records one installed plugin: where it came from, the exact
// commit installed, and the hash of its on-disk contents at install time.
type LockEntry struct {
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Ref         string    `json:"ref,omitempty"`
	Commit      string    `json:"commit"`
	ContentHash string    `json:"content_hash"`
	InstalledAt time.Time `json:"installed_at"`
}

// Lockfile is the on-disk record of installed plugins for one scope.
type Lockfile struct {
	Plugins []LockEntry `json:"plugins"`
}

// ReadLockfile reads the lockfile at path. A missing file yields an empty
// lockfile; a malformed file is an error.
func ReadLockfile(path string) (*Lockfile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Lockfile{}, nil
		}
		return nil, fmt.Errorf("read lockfile: %w", err)
	}
	var lf Lockfile
	if err := json.Unmarshal(raw, &lf); err != nil {
		return nil, fmt.Errorf("parse lockfile %s: %w", path, err)
	}
	return &lf, nil
}

// Write persists the lockfile, creating parent directories as needed.
// Entries are sorted by name for stable diffs when the file is committed.
func (l *Lockfile) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lockfile directory: %w", err)
	}
	sort.Slice(l.Plugins, func(i, j int) bool { return l.Plugins[i].Name < l.Plugins[j].Name })
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encode lockfile: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	return nil
}

// Find returns the entry for name, if present.
func (l *Lockfile) Find(name string) (LockEntry, bool) {
	for _, e := range l.Plugins {
		if e.Name == name {
			return e, true
		}
	}
	return LockEntry{}, false
}

// Upsert replaces the entry with the same name, or appends a new one.
func (l *Lockfile) Upsert(entry LockEntry) {
	for i, e := range l.Plugins {
		if e.Name == entry.Name {
			l.Plugins[i] = entry
			return
		}
	}
	l.Plugins = append(l.Plugins, entry)
}

// Remove deletes the entry with the given name, returning whether it existed.
func (l *Lockfile) Remove(name string) bool {
	for i, e := range l.Plugins {
		if e.Name == name {
			l.Plugins = append(l.Plugins[:i], l.Plugins[i+1:]...)
			return true
		}
	}
	return false
}
