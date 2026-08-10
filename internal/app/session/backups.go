package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BackupFile struct {
	Path    string
	Content string
	Mode    os.FileMode
	// Exists records whether the file existed before the write. When false,
	// rollback removes the file rather than restoring empty content.
	Exists bool
}

func (s *State) StoreBackup(backups []BackupFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = backups
}

func (s *State) Backup() []BackupFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	return backups
}

func (s *State) ClearBackup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = nil
}

func (s *State) HasBackup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lastBackup) > 0
}

// RollbackBackup restores every file captured in the last backup.
//
// The backup is retained until all writes succeed, so a partial failure is
// retryable. It previously cleared s.lastBackup before writing and returned on
// the first error, which left the workspace half-reverted with the backup
// already destroyed — unrecoverable, and the caller reported success because
// it discarded the error.
//
// On partial failure it returns an error naming what was and was not restored,
// and records the same in the transcript so the agent is not left reasoning
// about a workspace state that does not exist.
func (s *State) RollbackBackup() error {
	s.mu.Lock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	s.mu.Unlock()

	if len(backups) == 0 {
		return fmt.Errorf("no backup available")
	}

	var restored, failed []string
	var firstErr error
	for _, bf := range backups {
		path := filepath.Join(s.WorkingDir, bf.Path)
		var err error
		if bf.Exists {
			err = os.WriteFile(path, []byte(bf.Content), bf.Mode)
		} else {
			// The file did not exist before the write; roll back by removing
			// it rather than restoring empty content.
			err = os.Remove(path)
		}
		if err != nil {
			// Keep going: stopping here would strand the remaining files for
			// no benefit, and the caller needs the full picture either way.
			failed = append(failed, bf.Path)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		restored = append(restored, bf.Path)
	}

	if firstErr != nil {
		s.AddMessage(RoleSystem, fmt.Sprintf(
			"System notice: Rollback FAILED partway. Restored %d file(s): %s. Could not restore %d: %s (%v). "+
				"The working tree is in a mixed state and the backup has been kept so the rollback can be retried.",
			len(restored), strings.Join(restored, ", "),
			len(failed), strings.Join(failed, ", "), firstErr,
		), ContentTypePlain)
		return fmt.Errorf("rollback restored %d of %d files; %s failed: %w",
			len(restored), len(backups), strings.Join(failed, ", "), firstErr)
	}

	// Only drop the backup once every file is safely back.
	s.mu.Lock()
	s.lastBackup = nil
	s.mu.Unlock()

	s.AddMessage(RoleSystem, "System notice: The user has rolled back the last patch. All modified files have been reverted to their original state.", ContentTypePlain)
	return nil
}
