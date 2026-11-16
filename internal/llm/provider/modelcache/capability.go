package modelcache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// capabilityFile is the overrides file, kept beside the discovered-model
// cache in the shared data directory.
const capabilityFile = "model_capabilities.json"

// DefaultCapabilityTTL bounds how long an observed rejection is trusted.
// The config hash catches a repointed endpoint sooner; the TTL is what
// catches an upstream model gaining image support.
const DefaultCapabilityTTL = 30 * 24 * time.Hour

// CapabilityOverrides records what was learned about a provider/model pair
// from an observed provider rejection. It is deliberately separate from
// Cache.Entry, which is keyed by HashProvider and expires by TTL: the next
// model-list refresh would otherwise erase a fact we observed.
type CapabilityOverrides struct {
	Rejections map[string]Rejection `json:"rejections"` // "provider/model"
}

// Rejection is one observed refusal of image input.
type Rejection struct {
	ObservedAt time.Time `json:"observed_at"`
	ConfigHash string    `json:"config_hash"`
}

// VisionRejected reports whether provider/model is recorded as refusing
// image input. ttl and the provider config hash expire an entry: a
// repointed endpoint may be a different model.
func (o CapabilityOverrides) VisionRejected(provider, model, configHash string, ttl time.Duration, now time.Time) bool {
	r, ok := o.Rejections[provider+"/"+model]
	if !ok {
		return false
	}
	if r.ConfigHash != configHash {
		return false
	}
	if now.Sub(r.ObservedAt) > ttl {
		return false
	}
	return true
}

// LoadOverrides reads the overrides file. Every failure mode yields empty
// overrides: a missing or corrupt file only costs a retried image request.
func LoadOverrides(dataDir string) CapabilityOverrides {
	empty := CapabilityOverrides{Rejections: map[string]Rejection{}}
	data, err := os.ReadFile(capabilityPath(dataDir))
	if err != nil {
		return empty
	}
	var o CapabilityOverrides
	if err := json.Unmarshal(data, &o); err != nil {
		return empty
	}
	if o.Rejections == nil {
		o.Rejections = map[string]Rejection{}
	}
	return o
}

// SaveOverrides writes the overrides file, creating the directory if
// needed. The file is private (0600) and replaced by rename, so a reader
// never sees a partial file.
func SaveOverrides(dataDir string, o CapabilityOverrides) error {
	path := capabilityPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fmt.Errorf("encode capability overrides: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), capabilityFile+".tmp")
	if err != nil {
		return fmt.Errorf("write capability overrides: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write capability overrides: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write capability overrides: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write capability overrides: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write capability overrides: %w", err)
	}
	return nil
}

func capabilityPath(dataDir string) string {
	return filepath.Join(dataDir, capabilityFile)
}
