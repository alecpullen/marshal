package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// flexTime is a JSON-unmarshalling helper that tolerates both RFC3339
// timestamps (with timezone) and zoneless timestamps (parsed as UTC).
type flexTime struct {
	time.Time
}

func (f *flexTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		f.Time = t
		return nil
	}
	t, err = time.Parse("2006-01-02T15:04:05", s)
	if err == nil {
		f.Time = t.UTC()
		return nil
	}
	return fmt.Errorf("parse time %q: %w", s, err)
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
	var raws map[string]loadRecord
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("parse trust store: %w", err)
	}
	records := make(map[string]Record, len(raws))
	for k, v := range raws {
		records[k] = Record{
			Trusted:    v.Trusted,
			ConfigHash: v.ConfigHash,
			TrustedAt:  v.TrustedAt.Time,
		}
	}
	return records, nil
}

// loadRecord is the JSON intermediate for Load, using flexTime
// to tolerate zoneless timestamps.
type loadRecord struct {
	Trusted    bool     `json:"trusted"`
	ConfigHash string   `json:"config_hash,omitempty"`
	TrustedAt  flexTime `json:"trusted_at"`
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

// ConfigHashFor returns the SHA-256 hex digest of the project config
// file at workingDir/.marshal/config.toml. Returns an empty string
// (not an error) when the file does not exist, since the absence of a
// project config means no trust-gated sections are loaded. A read
// error returns an error so callers can distinguish "no config" from
// "couldn't read config".
func ConfigHashFor(workingDir string) (string, error) {
	path := filepath.Join(workingDir, ".marshal", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("hash project config: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// StoredConfigHash returns the config_hash that was persisted when
// trust was last recorded for absPath. Returns "" if no record
// exists, so callers can compare against the current hash without
// branching on record presence.
func (s *Store) StoredConfigHash(absPath string) (string, error) {
	records, err := s.Load()
	if err != nil {
		// Corrupted store: treat as no record rather than failing trust
		// resolution — the prompt path will run and re-establish trust.
		return "", nil
	}
	r, ok := records[absPath]
	if !ok {
		return "", nil
	}
	return r.ConfigHash, nil
}

// RefreshConfigHash updates the stored config hash on an existing trusted
// record, preserving Trusted and TrustedAt. It is used when a trusted
// session intentionally modifies the project config (e.g. /settings, /set):
// the user has implicitly approved the new config, so the hash recorded at
// trust time is advanced to match. It never creates or promotes a record —
// absent or untrusted entries are left untouched, so external or agent-made
// config changes still force a re-prompt.
func (s *Store) RefreshConfigHash(absPath string, configHash string) error {
	records, err := s.Load()
	if err != nil {
		// Corrupted store: nothing safe to refresh; leave it for the
		// prompt path to re-establish trust.
		return nil
	}
	r, ok := records[absPath]
	if !ok || !r.Trusted {
		return nil
	}
	r.ConfigHash = configHash
	records[absPath] = r
	return s.Save(records)
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

// Evaluate reports the stored trust state for workingDir without prompting.
// It performs the same checks TerminalResolver.Resolve does up to the point
// of prompting, including config-hash revalidation: needsPrompt is true when
// a project config exists but trust is absent or the config changed since it
// was trusted. The interactive TUI uses this to defer the question to an
// inline panel.
func Evaluate(store *Store, workingDir string) (decision Decision, needsPrompt bool, err error) {
	if !HasProjectConfig(workingDir) {
		return DecisionDontTrust, false, nil
	}
	abs, _ := filepath.Abs(workingDir)
	trusted, err := store.IsTrusted(abs)
	if err != nil {
		return DecisionDontTrust, false, err
	}
	if !trusted {
		return DecisionDontTrust, true, nil
	}
	currentHash, err := ConfigHashFor(workingDir)
	if err != nil {
		return DecisionDontTrust, false, err
	}
	storedHash, _ := store.StoredConfigHash(abs)
	if storedHash == currentHash {
		return DecisionTrustPermanent, false, nil
	}
	// Config changed since trust was recorded: re-prompt.
	return DecisionDontTrust, true, nil
}

func HasProjectConfig(workingDir string) bool {
	_, err := os.Stat(filepath.Join(workingDir, ".marshal", "config.toml"))
	return err == nil
}
