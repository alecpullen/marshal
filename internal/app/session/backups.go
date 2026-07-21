package session

import (
	"fmt"
	"os"
	"path/filepath"
)

type BackupFile struct {
	Path    string
	Content string
	Mode    os.FileMode
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

func (s *State) RollbackBackup() error {
	s.mu.Lock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	s.lastBackup = nil
	s.mu.Unlock()

	if len(backups) == 0 {
		return fmt.Errorf("no backup available")
	}
	for _, bf := range backups {
		path := filepath.Join(s.WorkingDir, bf.Path)
		if err := os.WriteFile(path, []byte(bf.Content), bf.Mode); err != nil {
			return err
		}
	}

	s.AddMessage(RoleSystem, "System notice: The user has rolled back the last patch. All modified files have been reverted to their original state.", ContentTypePlain)
	return nil
}
