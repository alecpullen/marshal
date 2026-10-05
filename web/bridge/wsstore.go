package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// templateNameRe bounds Studio template names: a slug that is safe as a
// directory name and an image-tag component.
var templateNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// maxTemplatePool is the largest warm-pool size a template may ask for.
const maxTemplatePool = 4

// Template store errors.
var (
	ErrTemplateName     = errors.New("bridge: template name must match ^[a-z0-9][a-z0-9-]{0,40}$")
	ErrTemplateExists   = errors.New("bridge: template already exists")
	ErrTemplateNotFound = errors.New("bridge: template not found")
	ErrTemplateInUse    = errors.New("bridge: template is in use by running agents")
	ErrTemplateNoDraft  = errors.New("bridge: template has no draft")
	ErrTemplatePool     = errors.New("bridge: pool size must be between 0 and 4")
)

// TemplateVersion is one published, immutable version of a template.
type TemplateVersion struct {
	N           int       `json:"n"`
	At          time.Time `json:"at"`
	By          string    `json:"by"`
	SHA256      string    `json:"sha256"`
	ImageTag    string    `json:"imageTag,omitempty"`
	BuildStatus string    `json:"buildStatus"`
	SizeBytes   int64     `json:"sizeBytes,omitempty"`
	BuildMs     int64     `json:"buildMs,omitempty"`
}

// TemplateMeta is a template's meta.json.
type TemplateMeta struct {
	Name      string            `json:"name"`
	OwnerID   string            `json:"ownerId"`
	CreatedAt time.Time         `json:"createdAt"`
	Published int               `json:"published"`
	Pool      int               `json:"pool"`
	Versions  []TemplateVersion `json:"versions"`
}

// version returns the record for version n.
func (m *TemplateMeta) version(n int) (*TemplateVersion, bool) {
	for i := range m.Versions {
		if m.Versions[i].N == n {
			return &m.Versions[i], true
		}
	}
	return nil, false
}

// TemplateStore keeps Studio templates under <state>/workspaces/<name>/.
// Published version files are immutable; the draft and meta.json are
// replaced atomically.
type TemplateStore struct {
	dir string
	mu  sync.Mutex
	// rename is the atomic-replace seam; tests inject a failing one.
	rename func(oldpath, newpath string) error
	now    func() time.Time
}

// NewTemplateStore roots a store at <stateDir>/workspaces.
func NewTemplateStore(stateDir string) *TemplateStore {
	return &TemplateStore{dir: filepath.Join(stateDir, "workspaces"), rename: os.Rename, now: time.Now}
}

func validTemplateName(name string) error {
	if !templateNameRe.MatchString(name) {
		return ErrTemplateName
	}
	return nil
}

func (s *TemplateStore) tdir(name string) string { return filepath.Join(s.dir, name) }

// FilesDir is where a template's shared-file sources live.
func (s *TemplateStore) FilesDir(name string) string { return filepath.Join(s.tdir(name), "files") }

// writeAtomic writes data to path through a temp file, fsync and rename.
func (s *TemplateStore) writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := s.rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *TemplateStore) readMeta(name string) (TemplateMeta, error) {
	var m TemplateMeta
	data, err := os.ReadFile(filepath.Join(s.tdir(name), "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrTemplateNotFound
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("bridge: template %s meta: %w", name, err)
	}
	return m, nil
}

func (s *TemplateStore) writeMeta(m TemplateMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return s.writeAtomic(filepath.Join(s.tdir(m.Name), "meta.json"), data)
}

// Create writes meta.json and draft.toml for a new template.
func (s *TemplateStore) Create(name string, source []byte, by string) (TemplateMeta, error) {
	if err := validTemplateName(name); err != nil {
		return TemplateMeta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(filepath.Join(s.tdir(name), "meta.json")); err == nil {
		return TemplateMeta{}, ErrTemplateExists
	}
	m := TemplateMeta{Name: name, OwnerID: DefaultOwnerID, CreatedAt: s.now().UTC(), Versions: []TemplateVersion{}}
	if err := s.writeAtomic(filepath.Join(s.tdir(name), "draft.toml"), source); err != nil {
		return TemplateMeta{}, err
	}
	if err := s.writeMeta(m); err != nil {
		return TemplateMeta{}, err
	}
	return m, nil
}

// SaveDraft overwrites draft.toml.
func (s *TemplateStore) SaveDraft(name string, source []byte) error {
	if err := validTemplateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(name); err != nil {
		return err
	}
	return s.writeAtomic(filepath.Join(s.tdir(name), "draft.toml"), source)
}

// Publish copies the draft to v<n+1>.toml and appends a pending version.
func (s *TemplateStore) Publish(name, by string) (TemplateVersion, error) {
	if err := validTemplateName(name); err != nil {
		return TemplateVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(name)
	if err != nil {
		return TemplateVersion{}, err
	}
	draft, err := os.ReadFile(filepath.Join(s.tdir(name), "draft.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return TemplateVersion{}, ErrTemplateNoDraft
	}
	if err != nil {
		return TemplateVersion{}, err
	}
	n := 1
	if len(m.Versions) > 0 {
		n = m.Versions[len(m.Versions)-1].N + 1
	}
	if err := s.writeAtomic(filepath.Join(s.tdir(name), fmt.Sprintf("v%d.toml", n)), draft); err != nil {
		return TemplateVersion{}, err
	}
	sum := sha256.Sum256(draft)
	v := TemplateVersion{N: n, At: s.now().UTC(), By: by, SHA256: hex.EncodeToString(sum[:]), BuildStatus: "pending"}
	m.Versions = append(m.Versions, v)
	m.Published = n
	if err := s.writeMeta(m); err != nil {
		return TemplateVersion{}, err
	}
	return v, nil
}

// List returns every template's meta, sorted by name.
func (s *TemplateStore) List() ([]TemplateMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []TemplateMeta{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []TemplateMeta{}
	for _, e := range entries {
		if !e.IsDir() || validTemplateName(e.Name()) != nil {
			continue
		}
		m, err := s.readMeta(e.Name())
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Meta returns one template's meta.
func (s *TemplateStore) Meta(name string) (TemplateMeta, error) {
	if err := validTemplateName(name); err != nil {
		return TemplateMeta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMeta(name)
}

// Read returns a template's source: n = 0 is the draft, otherwise the
// published version n.
func (s *TemplateStore) Read(name string, n int) ([]byte, error) {
	if err := validTemplateName(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(name)
	if err != nil {
		return nil, err
	}
	file := "draft.toml"
	if n != 0 {
		if _, ok := m.version(n); !ok {
			return nil, fmt.Errorf("%w: %s@%d", ErrTemplateNotFound, name, n)
		}
		file = fmt.Sprintf("v%d.toml", n)
	}
	data, err := os.ReadFile(filepath.Join(s.tdir(name), file))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTemplateNoDraft
	}
	return data, err
}

// Delete removes a template unless inUse reports its name in use.
func (s *TemplateStore) Delete(name string, inUse func(string) bool) error {
	if err := validTemplateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(name); err != nil {
		return err
	}
	if inUse != nil && inUse(name) {
		return ErrTemplateInUse
	}
	return os.RemoveAll(s.tdir(name))
}

// SetBuild records a build result on version n.
func (s *TemplateStore) SetBuild(name string, n int, status, tag string, size, ms int64) error {
	if err := validTemplateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(name)
	if err != nil {
		return err
	}
	v, ok := m.version(n)
	if !ok {
		return fmt.Errorf("%w: %s@%d", ErrTemplateNotFound, name, n)
	}
	v.BuildStatus, v.ImageTag, v.SizeBytes, v.BuildMs = status, tag, size, ms
	return s.writeMeta(m)
}

// FailInterrupted marks every version still `building` as failed. A build
// only stays in that state when the bridge stopped mid-build. It returns
// the interrupted versions as name -> version numbers.
func (s *TemplateStore) FailInterrupted() (map[string][]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string][]int{}
	for _, e := range ents {
		if !e.IsDir() || validTemplateName(e.Name()) != nil {
			continue
		}
		m, err := s.readMeta(e.Name())
		if err != nil {
			continue
		}
		changed := false
		for i := range m.Versions {
			if m.Versions[i].BuildStatus == "building" {
				m.Versions[i].BuildStatus = "failed"
				out[m.Name] = append(out[m.Name], m.Versions[i].N)
				changed = true
			}
		}
		if changed {
			if err := s.writeMeta(m); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// DraftChanged reports whether the draft differs from the published
// version. A template that was never published always has draft changes.
func (s *TemplateStore) DraftChanged(m TemplateMeta) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, err := os.ReadFile(filepath.Join(s.tdir(m.Name), "draft.toml"))
	if err != nil {
		return false
	}
	if m.Published == 0 {
		return true
	}
	pub, err := os.ReadFile(filepath.Join(s.tdir(m.Name), fmt.Sprintf("v%d.toml", m.Published)))
	if err != nil {
		return true
	}
	return !bytes.Equal(draft, pub)
}

// SetPool sets the warm-pool size, 0 to 4.
func (s *TemplateStore) SetPool(name string, n int) error {
	if err := validTemplateName(name); err != nil {
		return err
	}
	if n < 0 || n > maxTemplatePool {
		return ErrTemplatePool
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readMeta(name)
	if err != nil {
		return err
	}
	m.Pool = n
	return s.writeMeta(m)
}
