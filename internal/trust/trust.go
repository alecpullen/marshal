package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Decision string

const (
	DecisionTrustPermanent Decision = "trust_permanent"
	DecisionTrustSession   Decision = "trust_session"
	DecisionDontTrust      Decision = "dont_trust"
)

type Record struct {
	Trusted    bool      `json:"trusted"`
	ConfigHash string    `json:"config_hash,omitempty"`
	TrustedAt  time.Time `json:"trusted_at"`
}

type Store struct {
	path string
}

func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "trust.json")}
}

func (s *Store) Load() (map[string]Record, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Record{}, nil
		}
		return nil, fmt.Errorf("read trust store: %w", err)
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse trust store: %w", err)
	}
	return records, nil
}

func (s *Store) Save(records map[string]Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func (s *Store) IsTrusted(absPath string) (bool, error) {
	records, err := s.Load()
	if err != nil {
		// Corrupted or unreadable trust store: default to untrusted.
		return false, nil
	}
	r, ok := records[absPath]
	return ok && r.Trusted, nil
}

func (s *Store) SetTrust(absPath string, permanent bool, configHash string) error {
	if !permanent {
		return nil
	}
	records, err := s.Load()
	if err != nil {
		// If the store is corrupted, start fresh rather than failing to record trust.
		records = map[string]Record{}
	}
	records[absPath] = Record{Trusted: true, ConfigHash: configHash, TrustedAt: time.Now()}
	return s.Save(records)
}

type Resolver interface {
	Resolve(workingDir string, hasProjectConfig bool) (Decision, error)
	Record(workingDir string, decision Decision) error
}

func HasProjectConfig(workingDir string) bool {
	_, err := os.Stat(filepath.Join(workingDir, ".marshal", "config.toml"))
	return err == nil
}
