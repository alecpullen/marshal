// Package imagepayload stores agent-visible images.
//
// It is a leaf: it imports internal/llm/schema and the standard library, so
// the tools that produce images (image.view today, image.capture and
// image.render later) share one normalization and one store.
//
// The store is content-addressed. Put hashes the *normalized* bytes, so the
// same screenshot viewed twice occupies one entry and keeps one hash — which
// is what makes an image cheap to replay in a cached prompt prefix.
package imagepayload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"marshal/internal/llm/schema"
)

type Config struct {
	MaxInputBytes   int64 // 20 MiB, source file
	MaxStoredBytes  int   // 4 MiB, per stored image
	MaxEdge         int   // 1568 px on the long edge
	JPEGQuality     int   // 85
	MaxPerMessage   int   // 10 — enforced by the message builder, not here
	MaxRequestBytes int64 // 12 MiB of base64 — enforced by the request path
	MaxStoreBytes   int64 // 512 MiB on disk
	RetainDays      int   // 14
}

type Options struct { // per-call overrides
	MaxEdge int    // 0 = Config.MaxEdge
	Format  string // "" | "png" | "jpeg"
	Quality int    // 0 = Config.JPEGQuality
	Label   string
}

type Store struct {
	root string
	cfg  Config
}

type Sweep struct {
	Removed    int
	FreedBytes int64
}

// Open prepares the store directory. Every write path creates its own
// subdirectory, so Open only has to check that root is usable.
func Open(root string, cfg Config) (*Store, error) {
	if root == "" {
		return nil, errors.New("imagepayload: empty store root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("imagepayload: create store: %w", err)
	}
	return &Store{root: root, cfg: cfg}, nil
}

// Config reports the effective configuration.
func (s *Store) Config() Config { return s.cfg }

// Put normalizes r and stores the result. The same input and the same
// options always produce the same hash, so a repeat Put refreshes the
// entry's last_used instead of growing the store.
func (s *Store) Put(ctx context.Context, r io.Reader, o Options) (schema.ImageRef, error) {
	if err := ctx.Err(); err != nil {
		return schema.ImageRef{}, err
	}
	limit := s.cfg.MaxInputBytes
	if limit <= 0 {
		limit = 20 << 20
	}
	src, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return schema.ImageRef{}, fmt.Errorf("%w: %v", ErrCorruptImage, err)
	}
	if int64(len(src)) > limit {
		return schema.ImageRef{}, fmt.Errorf("%w: source exceeds %d bytes", ErrTooLarge, limit)
	}
	data, mime, w, h, err := normalize(src, s.cfg, o)
	if err != nil {
		return schema.ImageRef{}, err
	}
	sum := sha256.Sum256(data)
	ref := schema.ImageRef{
		Hash:   hex.EncodeToString(sum[:]),
		MIME:   mime,
		Width:  w,
		Height: h,
		Bytes:  len(data),
		Label:  o.Label,
	}
	if err := s.write(ref, data); err != nil {
		return schema.ImageRef{}, err
	}
	return ref, nil
}

// Image implements schema.ImageSource.
func (s *Store) Image(ctx context.Context, hash string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !validHash(hash) {
		return nil, "", fmt.Errorf("%w: bad hash %q", ErrNotFound, hash)
	}
	data, err := os.ReadFile(s.path(hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("%w: %s", ErrNotFound, hash)
		}
		return nil, "", err
	}
	mime := ""
	if sc, _, err := s.readSidecar(hash); err == nil {
		mime = sc.MIME
		sc.LastUsed = time.Now().UTC()
		_ = s.writeSidecar(hash, sc)
	}
	if mime == "" {
		_, mime, _ = sniff(data)
	}
	return data, mime, nil
}

// Stat reports a stored ref without reading its bytes.
func (s *Store) Stat(ctx context.Context, hash string) (schema.ImageRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return schema.ImageRef{}, false, err
	}
	sc, ok, err := s.readSidecar(hash)
	if err != nil || !ok {
		return schema.ImageRef{}, false, err
	}
	return sc.ImageRef, true, nil
}

// Size reports the store's total bytes and entry count.
func (s *Store) Size() (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(s.root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(p) == ".json" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		total += info.Size()
		count++
		return nil
	})
	return total, count, err
}

// Sweep evicts entries oldest-first until the store is under MaxStoreBytes.
// referenced is the set of hashes a transcript still names; an entry in it,
// or one touched inside RetainDays, is never removed. The store cannot build
// that set itself (it needs the session database), which is why the caller
// passes it. The store never deletes on its own: internal/app calls Sweep.
func (s *Store) Sweep(ctx context.Context, referenced func() (map[string]struct{}, error)) (Sweep, error) {
	var out Sweep
	if s.cfg.MaxStoreBytes <= 0 {
		return out, nil
	}
	total, _, err := s.Size()
	if err != nil || total <= s.cfg.MaxStoreBytes {
		return out, err
	}
	refs := map[string]struct{}{}
	if referenced != nil {
		if refs, err = referenced(); err != nil {
			return out, err
		}
	}
	type entry struct {
		hash     string
		lastUsed time.Time
		bytes    int64
	}
	var entries []entry
	err = filepath.WalkDir(s.root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() || filepath.Ext(p) != ".json" {
			return werr
		}
		hash := strings.TrimSuffix(filepath.Base(p), ".json")
		sc, _, rerr := s.readSidecar(hash)
		if rerr != nil {
			return nil
		}
		entries = append(entries, entry{hash: hash, lastUsed: sc.LastUsed, bytes: int64(sc.Bytes)})
		return nil
	})
	if err != nil {
		return out, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].lastUsed.Before(entries[j].lastUsed) })
	cutoff := time.Now().UTC().AddDate(0, 0, -s.cfg.RetainDays)
	for _, e := range entries {
		if total <= s.cfg.MaxStoreBytes {
			break
		}
		if _, ok := refs[e.hash]; ok {
			continue
		}
		if e.lastUsed.After(cutoff) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := os.Remove(s.path(e.hash)); err != nil && !os.IsNotExist(err) {
			continue
		}
		_ = os.Remove(s.path(e.hash) + ".json")
		total -= e.bytes
		out.Removed++
		out.FreedBytes += e.bytes
	}
	return out, nil
}

func (s *Store) path(hash string) string {
	return filepath.Join(s.root, hash[:2], hash)
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

type sidecar struct {
	schema.ImageRef
	LastUsed time.Time `json:"last_used"`
}

func (s *Store) write(ref schema.ImageRef, data []byte) error {
	dir := filepath.Dir(s.path(ref.Hash))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("imagepayload: create shard: %w", err)
	}
	if err := writeFileAtomic(s.path(ref.Hash), data); err != nil {
		return err
	}
	return s.writeSidecar(ref.Hash, sidecar{ImageRef: ref, LastUsed: time.Now().UTC()})
}

func (s *Store) writeSidecar(hash string, sc sidecar) error {
	data, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(hash)+".json", data)
}

func (s *Store) readSidecar(hash string) (sidecar, bool, error) {
	var sc sidecar
	data, err := os.ReadFile(s.path(hash) + ".json")
	if err != nil {
		if os.IsNotExist(err) {
			return sc, false, nil
		}
		return sc, false, err
	}
	if err := json.Unmarshal(data, &sc); err != nil {
		return sc, false, nil
	}
	return sc, true, nil
}

// writeFileAtomic writes a temp file beside the destination and renames it,
// so a crash never leaves a truncated entry a hash points at.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// EstimateTokens is the budget figure for one image: a fixed overhead plus
// area/750, close enough for every provider in scope (spec §13).
func EstimateTokens(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	return 85 + (width*height+749)/750
}
