# Images in Marshal — implementation plan

**Status:** awaiting approval.
**Spec:** [`docs/vision/design.md`](../design.md) — approved 2026-11-17, amended
2026-11-17 (vision resolves to *attempt*, §7).
**Base:** `main` at `9206749b`.
**Executor:** `marshal-executing-plans`, one commit per task:
`images: task N — <task title>`.

## Goal

The agent can see images. A prompt, a tool result, a subagent result, an MCP
result or a web/fleet upload carries `schema.ImageRef` values; the bytes live
in a content-addressed store and are resolved by each provider encoder at send
time. One new tool, `image.view`, shows an existing image. Vision is attempted
by default, refused only for a model known or proven unable, and a loud
rejection is recovered by retrying the turn without images.

## Non-goals

Verbatim from the spec §2: no terminal image rendering, no `image.capture`, no
`image.render`, no image generation, no image-token pricing, no provider files
API, no interleaved content parts.

## Assumptions

- Provider dialects accept inline base64 `data:` URLs (spec §13). Where an
  endpoint rejects them loudly, task 9 recovers the turn.
- `internal/llm/schema` stays dependency-free apart from the standard library:
  it currently imports only `encoding/json` (`internal/llm/schema/chat.go`).
  `schema/image.go` adds `context` and nothing else.
- `internal/tools/registry` does not yet import `internal/llm/schema`. Adding
  that import is safe: `schema` imports no `marshal/internal/...` package.
- Session rows are the only persisted transcript, so one column covers replay.

## Decision pinned by this plan (spec says "on Open and after each Put")

Spec §8 says store GC runs "on `Open` and after each `Put`". `Open(root, cfg)`
takes no callback, and the referenced-hash set comes from a SQLite query, so a
store that swept on its own would need the database. **The store never
deletes on its own.** `internal/app` calls `Store.Sweep` once when the runtime
starts and again after a turn that stored images, when `Store.Size()` exceeds
`MaxStoreBytes`. This keeps `internal/imagepayload` a leaf package, which is
what lets `image.capture` and `image.render` reuse it later.

---

## Contracts

Every declaration below is used by more than one task. Tasks reference these
rather than re-declaring them.

### `internal/llm/schema/image.go` (new)

```go
package schema

import "context"

// ImageRef identifies an image in the payload store. It carries no bytes:
// encoders resolve them through ChatRequest.ImageSource at send time, so
// transcripts, ACP frames and the cached prompt prefix stay small.
type ImageRef struct {
	Hash   string `json:"hash"`            // content hash; the store key
	MIME   string `json:"mime"`            // "image/png" | "image/jpeg"
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`           // stored size, not source size
	Label  string `json:"label,omitempty"` // "screenshot of /tmp/ui.png"
}

// ImageSource resolves an image ref to what an encoder puts on the wire. A
// nil ImageSource on a request means the request cannot carry images: an
// encoder or estimator meeting non-empty Images with a nil source fails with
// ErrVisionUnsupported rather than silently sending text only.
type ImageSource interface {
	Image(ctx context.Context, hash string) (data []byte, mime string, err error)
}

// ErrVisionUnsupported reports that images were supplied to a path that
// cannot carry them. Callers classify with errors.Is.
var ErrVisionUnsupported = errors.New("images are not supported on this route")

// VisionError is a refusal at the prompt boundary. It names the cause so the
// TUI and ACP can offer the fix (one config line, or the "send anyway" key).
type VisionError struct {
	Model  string
	Reason string // VisionReasonPresetOff | VisionReasonRejected
	Detail string // e.g. "rejected image input on 2026-11-02"
}

// VisionError reasons.
const (
	VisionReasonPresetOff = "preset_off"
	VisionReasonRejected  = "rejected"
)

func (e *VisionError) Error() string
func (e *VisionError) Is(target error) bool // true for ErrVisionUnsupported
```

`errors` and `fmt` join the imports of `schema/image.go`, together with
`context`.

### `internal/llm/schema/chat.go` (edit)

```go
type ChatMessage struct {
	// ...existing fields unchanged...
	// Images are attached after Content and never interleaved with it. A
	// message is text followed by its images.
	Images []ImageRef
}

type ChatRequest struct {
	// ...existing fields unchanged...
	// ImageSource resolves every ImageRef in Messages. Nil means this
	// request cannot carry images.
	ImageSource ImageSource
}
```

### `internal/tools/registry` (edit)

```go
type ToolResult struct {
	// ...existing fields unchanged...
	// Images are the images this call produced. Content stays the
	// human-readable text: spill, the audit trail and text-only consumers
	// read only that.
	Images []schema.ImageRef
}
```

### `internal/imagepayload` (new package)

```go
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

type Store struct { /* root string; cfg Config */ }

func Open(root string, cfg Config) (*Store, error)
func (s *Store) Put(ctx context.Context, r io.Reader, o Options) (schema.ImageRef, error)
func (s *Store) Image(ctx context.Context, hash string) (data []byte, mime string, err error)
func (s *Store) Stat(ctx context.Context, hash string) (schema.ImageRef, bool, error)
func (s *Store) Size() (bytes int64, count int, err error)
func (s *Store) Sweep(ctx context.Context, referenced func() (map[string]struct{}, error)) (Sweep, error)

type Sweep struct {
	Removed    int
	FreedBytes int64
}

// EstimateTokens is a budget figure, not a bill (spec §4).
func EstimateTokens(width, height int) int

// Errors, all matchable with errors.Is.
var (
	ErrUnsupportedFormat = errors.New("imagepayload: unsupported image format")
	ErrCorruptImage      = errors.New("imagepayload: corrupt image")
	ErrTooLarge          = errors.New("imagepayload: image exceeds a configured limit")
	ErrNotFound          = errors.New("imagepayload: image not in store")
)
```

`Store` satisfies `schema.ImageSource`.

### `internal/app/config` (edit)

```go
type ImagesConfig struct {
	MaxInputBytes   int64 `toml:"max_input_bytes"`
	MaxStoredBytes  int   `toml:"max_stored_bytes"`
	MaxEdge         int   `toml:"max_edge"`
	JPEGQuality     int   `toml:"jpeg_quality"`
	MaxPerMessage   int   `toml:"max_per_message"`
	MaxRequestBytes int64 `toml:"max_request_bytes"`
	MaxStoreBytes   int64 `toml:"max_store_bytes"`
	RetainDays      int   `toml:"retain_days"`
}

// Config gains: Images ImagesConfig `toml:"images"`

// Normalize replaces zero or negative values with the defaults, so a config
// that sets nothing (or a nonsense value) degrades to the default rather than
// disabling a limit. Written back to the receiving Config by Default/merge.
func (c ImagesConfig) Normalize() ImagesConfig

// PayloadConfig converts to the store's own config type. config importing
// imagepayload is safe: imagepayload imports only internal/llm/schema and the
// standard library.
func (c ImagesConfig) PayloadConfig() imagepayload.Config
```

### `internal/llm/routing` (edit)

```go
type ModelPreset struct {
	// ...existing fields unchanged...
	// Vision is "" (unset, same as "auto"), "auto", "on" or "off". It
	// answers "may this route be sent images", not "what does the model
	// understand".
	Vision string `toml:"vision,omitempty"`
}

// ModelPreset.Vision values.
const (
	VisionAuto = "auto"
	VisionOn   = "on"
	VisionOff  = "off"
)

type Route struct {
	// ...existing fields unchanged...
	// Vision reports that this route may be sent images. Resolved per-turn
	// by resolveRoute: explicit preset, learned rejection, probe, name
	// heuristic, else attempt.
	Vision bool
	// VisionForced is true when the user set vision = "on", so a provider
	// rejection surfaces as itself instead of being retried without images.
	VisionForced bool
}
```

### `internal/llm/provider` (edit)

```go
type ModelCapabilities struct {
	// ...existing fields unchanged...
	// Vision reports whether the model can take images; nil when the probe
	// could not tell.
	Vision *bool
}
```

### `internal/llm/provider/modelcache/capability.go` (new)

```go
// CapabilityOverrides records what was learned about a provider/model pair
// from an observed provider rejection. It is deliberately separate from
// Cache.Entry, which is keyed by HashProvider and expires by TTL: the next
// model-list refresh would otherwise erase a fact we observed.
type CapabilityOverrides struct {
	Rejections map[string]Rejection `json:"rejections"` // "provider/model"
}

type Rejection struct {
	ObservedAt time.Time `json:"observed_at"`
	ConfigHash string    `json:"config_hash"`
}

// VisionRejected reports whether provider/model is recorded as refusing
// image input. ttl and the provider config hash expire an entry: a
// repointed endpoint may be a different model.
func (o CapabilityOverrides) VisionRejected(provider, model, configHash string, ttl time.Duration, now time.Time) bool

func LoadOverrides(dataDir string) CapabilityOverrides
func SaveOverrides(dataDir string, o CapabilityOverrides) error
```

`DefaultCapabilityTTL = 30 * 24 * time.Hour`.

### `internal/app/session` (edit)

```go
type Message struct {
	// ...existing fields unchanged...
	// Images are the images this message carried, replayed like its text.
	Images []schema.ImageRef
}
```

### `internal/acpwire` (new `images.go`, plus edits)

```go
const MethodImagesPut = "session/images_put"

// CodeVisionUnsupported is the refusal for a prompt that carries images on a
// route that cannot take them.
const CodeVisionUnsupported ErrorCode = "vision_unsupported"

// PromptImage carries a ref across the wire. It duplicates schema.ImageRef's
// fields because acpwire is stdlib-only and converts at the seam.
type PromptImage struct {
	Hash   string `json:"hash"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Label  string `json:"label,omitempty"`
}

type ImagesPutParams struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"` // base64
	Label     string `json:"label,omitempty"`
}

type ImagesPutResult struct {
	Image PromptImage `json:"image"`
}

// PromptParams gains: Images []PromptImage `json:"images,omitempty"`
```

### `web/bridge/contract` (new `images.go`)

```go
type PromptImage struct {
	Hash   string `json:"hash"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Label  string `json:"label,omitempty"`
}

type ImageUpload struct {
	Data  string `json:"data"` // base64
	Label string `json:"label,omitempty"`
}

type ImageUploadResult struct {
	Image PromptImage `json:"image"`
}
```

---

## Task 1: Content model and the image store

**Goal.** `schema` carries image refs, and `internal/imagepayload` stores,
normalizes, serves and sweeps them; nothing else in Marshal changes yet.

**Files.**
- `internal/llm/schema/image.go` (new)
- `internal/llm/schema/chat.go` (edit)
- `internal/tools/registry/types.go` (edit)
- `internal/imagepayload/store.go` (new)
- `internal/imagepayload/normalize.go` (new)
- `internal/imagepayload/errors.go` (new)
- `internal/imagepayload/store_test.go` (new)
- `internal/imagepayload/normalize_test.go` (new)
- `internal/imagepayload/main_test.go` (new)
- `go.mod`, `go.sum`

**Steps.**

1. `go get golang.org/x/image@latest` — the plan's one new dependency (spec
   §12.8). Verify `go.mod` records it as a direct requirement, not `// indirect`.

2. Create `internal/llm/schema/image.go` with the `ImageRef`, `ImageSource`,
   `ErrVisionUnsupported`, `VisionError` and the two reason constants exactly as
   declared in Contracts. `VisionError.Error` returns
   `fmt.Sprintf("model %s cannot take images: %s", e.Model, e.Detail)`; `Is`
   returns `target == ErrVisionUnsupported`.

3. In `internal/llm/schema/chat.go`, add `Images []ImageRef` to `ChatMessage`
   (after `Reasoning`) and `ImageSource ImageSource` to `ChatRequest` (after
   `PromptCacheKey`), with the doc comments from Contracts.

4. In `internal/tools/registry/types.go`, add `Images []schema.ImageRef` as the
   last field of `ToolResult`, and add `"marshal/internal/llm/schema"` to the
   file's imports.

5. Create `internal/imagepayload/errors.go` with the four error values.

6. Create `internal/imagepayload/store.go`:

```go
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
	"time"

	"marshal/internal/llm/schema"
)

type Config struct { /* as in Contracts */ }

type Options struct { /* as in Contracts */ }

type Store struct {
	root string
	cfg  Config
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
```

  Then the retention sweep, sidecar and path helpers in the same file:

```go
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
```

  Add `"strings"` to the imports.

7. Create `internal/imagepayload/normalize.go`:

```go
package imagepayload

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// sniff reports the real type from magic bytes. The declared MIME is never
// trusted: a .png that is really a JPEG must decode as a JPEG.
func sniff(src []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(src, []byte("\x89PNG\r\n\x1a\n")):
		return "png", "image/png", nil
	case len(src) >= 3 && bytes.HasPrefix(src, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg", "image/jpeg", nil
	case bytes.HasPrefix(src, []byte("GIF87a")) || bytes.HasPrefix(src, []byte("GIF89a")):
		return "gif", "image/gif", nil
	case len(src) >= 12 && bytes.HasPrefix(src, []byte("RIFF")) && string(src[8:12]) == "WEBP":
		return "webp", "image/webp", nil
	}
	return "", "", fmt.Errorf("%w: not PNG, JPEG, GIF or WebP", ErrUnsupportedFormat)
}

func decode(src []byte) (image.Image, string, error) {
	format, _, err := sniff(src)
	if err != nil {
		return nil, "", err
	}
	var img image.Image
	switch format {
	case "png":
		img, err = png.Decode(bytes.NewReader(src))
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(src))
	case "gif":
		img, err = gif.Decode(bytes.NewReader(src)) // first frame
	case "webp":
		img, err = webp.Decode(bytes.NewReader(src))
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
	}
	return img, format, nil
}

// downscale shrinks img so its long edge is at most maxEdge, preserving
// aspect and never upscaling. Catmull-Rom keeps UI text legible.
func downscale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxEdge <= 0 || (w <= maxEdge && h <= maxEdge) {
		return img
	}
	scale := math.Min(float64(maxEdge)/float64(w), float64(maxEdge)/float64(h))
	nw, nh := int(math.Round(float64(w)*scale)), int(math.Round(float64(h)*scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// flattenOnWhite drops alpha by compositing onto white. JPEG has no alpha
// channel, so an alpha image that is too large for PNG would otherwise lose
// its transparency arbitrarily.
func flattenOnWhite(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
	return dst
}

// normalize is the whole pipeline: decode, downscale, re-encode, then prove
// the result fits. It is deterministic — the same source and options give the
// same bytes, so Put's hash is stable and a repeated view dedupes.
func normalize(src []byte, cfg Config, o Options) ([]byte, string, int, int, error) {
	img, _, err := decode(src)
	if err != nil {
		return nil, "", 0, 0, err
	}
	maxEdge := cfg.MaxEdge
	if o.MaxEdge > 0 {
		maxEdge = o.MaxEdge
	}
	limit := cfg.MaxStoredBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	quality := cfg.JPEGQuality
	if quality <= 0 {
		quality = 85
	}
	if o.Quality > 0 {
		quality = o.Quality
	}

	current := img
	for attempt := 0; ; attempt++ {
		current = downscale(current, maxEdge)
		data, mime, err := encodeOnce(current, o.Format, quality, limit)
		if err != nil {
			return nil, "", 0, 0, err
		}
		b := current.Bounds()
		if len(data) <= limit {
			return data, mime, b.Dx(), b.Dy(), nil
		}
		if attempt >= 2 || maxEdge <= 32 {
			return nil, "", 0, 0, fmt.Errorf("%w: %d bytes at %dx%d still exceeds %d bytes",
				ErrTooLarge, len(data), b.Dx(), b.Dy(), limit)
		}
		maxEdge /= 2
	}
}

// encodeOnce applies the format rule: an explicit format wins; otherwise PNG
// when it fits, JPEG after it. Screenshots are text, so PNG is the baseline
// and JPEG is the fallback ladder (spec §12.4).
func encodeOnce(img image.Image, format string, quality, limit int) ([]byte, string, error) {
	var buf bytes.Buffer
	pngEncode := func() ([]byte, string, error) {
		buf.Reset()
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
		}
		return buf.Bytes(), "image/png", nil
	}
	jpegEncode := func(q int) ([]byte, string, error) {
		buf.Reset()
		if err := jpeg.Encode(&buf, flattenOnWhite(img), &jpeg.Options{Quality: q}); err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrCorruptImage, err)
		}
		return buf.Bytes(), "image/jpeg", nil
	}
	switch format {
	case "png":
		return pngEncode()
	case "jpeg":
		return jpegEncode(quality)
	}
	data, mime, err := pngEncode()
	if err != nil || len(data) <= limit {
		return data, mime, err
	}
	for _, q := range []int{quality, 75, 60} {
		if data, mime, err = jpegEncode(q); err != nil || len(data) <= limit {
			return data, mime, err
		}
	}
	return data, mime, nil
}
```

  Add `"image/color"` to the imports.

8. Tests. `internal/imagepayload/main_test.go`:

```go
func TestMain(m *testing.M) { testenv.Run(m) }
```

  with `"marshal/internal/testenv"`.

  `normalize_test.go` — one test per behaviour:
  - `TestPutIsDeterministic` — the same 10×10 PNG twice gives the same hash.
  - `TestDownscalesLongEdge` — a 3000×10 PNG stores at 1568×5.
  - `TestNeverUpscales` — a 10×10 stays 10×10 with `MaxEdge: 1568`.
  - `TestKeepsPNGForScreenshot` — a 200×200 PNG with no alpha stores as
    `image/png`.
  - `TestFallsBackToJPEGWhenPNGExceedsLimit` — `MaxStoredBytes: 400` on a
    noisy 400×400 PNG stores `image/jpeg`.
  - `TestRejectsUnsupportedFormat` — a BMP header returns
    `errors.Is(err, ErrUnsupportedFormat)`.
  - `TestRejectsCorruptImage` — a PNG magic prefix with garbage returns
    `ErrCorruptImage`.
  - `TestRejectsOversizeInput` — `MaxInputBytes: 16` returns `ErrTooLarge`.
  - `TestEstimateTokensPinned` — `EstimateTokens(1568, 882) == 1929` and
    `EstimateTokens(0, 10) == 0`.
  - `TestFormatOverrideWins` — `Options{Format: "jpeg"}` on a PNG source stores
    `image/jpeg` and a different hash than the default call.

  `store_test.go`:
  - `TestStoreFilesArePrivate` — the shard dir is `0700`, the entry `0600`.
  - `TestImageMissingIsNotFound` — `errors.Is(err, ErrNotFound)`.
  - `TestSweepKeepsReferencedAndYoung` — three entries with old `last_used`,
    one referenced, one recent; only the unreferenced-old one is removed.
  - `TestSweepNoOpUnderCap` — `MaxStoreBytes` huge ⇒ `Sweep{Removed: 0}`.
  - `TestCrashedWriteLeavesNoEntry` — call `writeFileAtomic` with a directory
    that does not exist and assert no `*.json` appears.

  For the sweep fixtures, set `last_used` by writing the sidecar directly.

**Verify.**

```bash
CGO_ENABLED=1 go build ./internal/imagepayload ./internal/llm/schema ./internal/tools/registry
go test ./internal/imagepayload -count=1
go vet ./internal/imagepayload ./internal/llm/schema ./internal/tools/registry
```

Expected: build clean, every test above passes, `golang.org/x/image` is a
direct requirement in `go.mod`.

**Parallel-safe with: Task 2.**

---

## Task 2: The `[images]` config section

**Goal.** `[images]` exists in config, defaults sanely, merges per key, and
converts to the store's own config type.

**Files.**
- `internal/app/config/types.go` (edit)
- `internal/app/config/file_types.go` (edit)
- `internal/app/config/defaults.go` (edit)
- `internal/app/config/merge.go` (edit)
- `internal/app/config/images_test.go` (new)

**Steps.**

1. `internal/app/config/types.go`: add `Images ImagesConfig \`toml:"images"\`` as
   the last field of `Config` before `SessionRouting`, and declare
   `ImagesConfig`, its `Normalize` and its `PayloadConfig` per Contracts, next
   to `SkillsConfig`. `Normalize` returns the receiver with every zero or
   negative field replaced by the default from spec §10; `PayloadConfig`
   returns `imagepayload.Config` with the same numbers, importing
   `marshal/internal/imagepayload`.

2. `internal/app/config/file_types.go`: add `Images *ImagesConfig` to
   `configFile`, so only a `[images]` table that is actually present merges.

3. `internal/app/config/defaults.go`: inside `Default()`, set `Images:` to the
   spec §10 numbers (20 MiB, 4 MiB, 1568, 85, 10, 12 MiB, 512 MiB, 14).

4. `internal/app/config/merge.go`: add, following the `file.Skills` stanza at
   `merge.go:73`:

```go
	if file.Images != nil {
		set(&cfg.Images.MaxInputBytes, file.Images.MaxInputBytes)
		set(&cfg.Images.MaxStoredBytes, file.Images.MaxStoredBytes)
		set(&cfg.Images.MaxEdge, file.Images.MaxEdge)
		set(&cfg.Images.JPEGQuality, file.Images.JPEGQuality)
		set(&cfg.Images.MaxPerMessage, file.Images.MaxPerMessage)
		set(&cfg.Images.MaxRequestBytes, file.Images.MaxRequestBytes)
		set(&cfg.Images.MaxStoreBytes, file.Images.MaxStoreBytes)
		set(&cfg.Images.RetainDays, file.Images.RetainDays)
	}
```

5. Add `cfg.Images = cfg.Images.Normalize()` as the last statement of
   `merge` (`internal/app/config/merge.go:24`). A config with no `[images]`
   table at all never reaches the merge and keeps `Default()`'s already-valid
   values, so `merge` is the one place a file-supplied nonsense value can
   enter.

6. Tests in `internal/app/config/images_test.go`:
   - `TestImagesDefaults` — `Default().Images` equals the spec §10 numbers.
   - `TestImagesMergePerKey` — a file setting only `max_edge = 800` keeps the
     default for every other key.
   - `TestImagesNormalizeReplacesNonPositive` — `Normalize()` on a zero value
     equals the default.
   - `TestImagesPayloadConfigMatches` — `PayloadConfig()` carries the same
     eight numbers.

**Verify.**

```bash
go test ./internal/app/config -run 'TestImages' -count=1
go test ./internal/app/config -count=1
```

**Parallel-safe with: Task 1.**

---

## Task 3: Provider encoders

**Goal.** All four dialects put image bytes on the wire, and no encoder
silently sends text-only when a message carries images.

**Files.**
- `internal/llm/provider/imagepayload_wire.go` (new)
- `internal/llm/provider/anthropic.go` (edit)
- `internal/llm/provider/openai_compatible_wire.go` (edit)
- `internal/llm/provider/openai_compatible.go` (edit)
- `internal/llm/provider/ollama_native.go` (edit)
- `internal/llm/provider/imagepayload_wire_test.go` (new)

**Steps.**

1. Create `internal/llm/provider/imagepayload_wire.go` with the shared helper
   and the dialect blocks:

```go
package provider

import (
	"context"
	"encoding/base64"
	"fmt"

	"marshal/internal/llm/schema"
)

// imageDataURL resolves one ref and formats it as an OpenAI-style data URL.
// A message that carries images on a request with no ImageSource is a
// programming error at the send boundary, not something to paper over: the
// model would otherwise be asked about an image it never received.
func imageDataURL(ctx context.Context, src schema.ImageSource, ref schema.ImageRef) (string, error) {
	if src == nil {
		return "", fmt.Errorf("%w: %s", schema.ErrVisionUnsupported, ref.Label)
	}
	data, mime, err := src.Image(ctx, ref.Hash)
	if err != nil {
		return "", fmt.Errorf("resolve image %s: %w", ref.Hash, err)
	}
	if mime == "" {
		mime = ref.MIME
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func imageB64(ctx context.Context, src schema.ImageSource, ref schema.ImageRef) (string, error) {
	if src == nil {
		return "", fmt.Errorf("%w: %s", schema.ErrVisionUnsupported, ref.Label)
	}
	data, _, err := src.Image(ctx, ref.Hash)
	if err != nil {
		return "", fmt.Errorf("resolve image %s: %w", ref.Hash, err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
```

2. **Anthropic.** In `internal/llm/provider/anthropic.go`, four edits.

   a. Add the image source type and widen the two message types:

```go
// anthropicImageSource is the base64 arm of an image block's source.
type anthropicImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// anthropicToolResultBlock is a tool_result whose content is a block array
// (text plus images). A text-only tool result keeps using
// anthropicContentBlock, whose Content is a plain string.
type anthropicToolResultBlock struct {
	Type    string                  `json:"type"` // "tool_result"
	ID      string                  `json:"tool_use_id"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}
```

   b. Add `Source *anthropicImageSource \`json:"source,omitempty"\`` to
   `anthropicContentBlock` (`anthropic.go:97`); leave every existing field
   alone.

   c. Change `appendAnthropicUserBlock`'s block parameter from
   `anthropicContentBlock` to `any`. Every existing call site compiles
   unchanged. `anthropicBlockRef{msg, block int}` indexing is unaffected: a
   heterogeneous `[]any` counts blocks by index exactly as before.

   d. Give `buildAnthropicRequestBody` a leading `ctx context.Context`
   parameter and pass it through to `toAnthropicMessages`, which gains
   `(ctx context.Context, src schema.ImageSource)`. Both callers already have
   a `ctx`: the native Anthropic `Chat`, and
   `openai_compatible_messages.go:28`'s `messagesChat`. A resolution failure
   propagates as `schema.ErrVisionUnsupported` or the store error — never a
   silent text-only request.

   Then in `toAnthropicMessages`:
   - `schema.RoleTool` with `len(m.Images) == 0`: unchanged.
   - `schema.RoleTool` with images: append `anthropicToolResultBlock` whose
     `Content` is `[{Type:"text", Text:m.Content}]` followed by one
     `{Type:"image", Source:&anthropicImageSource{Type:"base64", MediaType:img.MIME, Data:b64}}`
     per ref, resolved with `imageB64`.
   - `schema.RoleUser` with images: append a `{Type:"text"}` block then one
     image block per ref, in that order.

3. **OpenAI-compatible.** In `internal/llm/provider/openai_compatible_wire.go`:
   - Change `chatMessageBody.Content` from `string` to `json.RawMessage`.
   - Add:

```go
// openAIContentPart is one entry of a multimodal content array.
type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

func textContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func partsContent(parts []openAIContentPart) json.RawMessage {
	b, _ := json.Marshal(parts)
	return b
}

// contentText reads either wire shape, so response parsing keeps working.
func (m chatMessageBody) contentText() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []openAIContentPart
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var out string
		for _, p := range parts {
			if p.Type == "text" {
				out += p.Text
			}
		}
		return out
	}
	return ""
}
```

   - In `internal/llm/provider/openai_compatible.go`, the single construction
     site at `openai_compatible.go:433` changes `Content: <string>` to
     `Content: textContent(<string>)`.
   - Add the image path: a helper that, given the converted message list,
     appends a synthetic `role:"user"` message whose `Content` is
     `partsContent` with one `text` part naming the image and one `image_url`
     part per ref. Use it after a `role:"tool"` message that carries images,
     and for a user message with images emit one parts-array message instead
     of a string one. The rule to implement: **text first, images after; a
     tool message's images always ride a synthetic user message immediately
     after it**, because OpenAI's `role:"tool"` content is text-only.
   - Every response-path read of `.Content` on a `chatMessageBody` switches to
     `.contentText()`.

4. **Ollama-native.** In `internal/llm/provider/ollama_native.go`, add
   `Images []string \`json:"images,omitempty"\`` to `ollamaMessage` (line 138).
   Populate it from `m.Images` on any role — Ollama accepts images on the
   message itself.

5. **Codex / Responses.** `internal/llm/provider/openai_codex.go` builds its
   body from `schema.ChatRequest` without an image path today. Add the
   `input_image` part only where the converted message is a user message, using
   `imageDataURL`; every other role keeps text. A rejection is recovered by
   task 9, so no capability gate is needed here.

6. Tests in `internal/llm/provider/imagepayload_wire_test.go` (package `provider`):
   - `TestAnthropicToolResultCarriesImages` — one message with an image ref and
     a fake `ImageSource` produces a `tool_result` whose `content` is an array
     of a text block and an `image` block with `source.type == "base64"`.
   - `TestAnthropicUserMessageCarriesImages` — text block then image block.
   - `TestOpenAIToolImagesRideASyntheticUserMessage` — the tool message's
     `Content` is text, and the next message is a user parts array with one
     `image_url` whose URL starts `data:image/png;base64,`.
   - `TestOpenAIUserMessageBecomesParts` — one message, parts array.
   - `TestOllamaImagesOnTheMessage` — `message.images` has one base64 string.
   - `TestNilImageSourceFailsLoudly` — for each of the four builders,
     non-empty `Images` with a nil `ImageSource` returns
     `errors.Is(err, schema.ErrVisionUnsupported)`.
   - `TestImageSourceErrorPropagates` — a fake returning `imagepayload.ErrNotFound`
     surfaces it with `errors.Is`.

   Use a 1×1 PNG fixture and a fake source:

```go
type fakeSource map[string][]byte

func (f fakeSource) Image(_ context.Context, hash string) ([]byte, string, error) {
	if b, ok := f[hash]; ok {
		return b, "image/png", nil
	}
	return nil, "", imagepayload.ErrNotFound
}
```

**Verify.**

```bash
CGO_ENABLED=1 go build ./internal/llm/provider
go test ./internal/llm/provider -run 'TestAnthropic|TestOpenAI|TestOllama|TestNilImageSource|TestImageSource' -count=1
go test ./internal/llm/provider -count=1
```

Expected: all pass. The existing provider suite is the regression guard for the
`chatMessageBody.Content` type change.

---

## Task 4: Agent loop — tool-result images, token accounting, prune shedding

**Goal.** A tool result's images reach the model, count against the budget,
and are shed only where prune already sheds.

**Files.**
- `internal/agent/prompts.go` (edit)
- `internal/agent/compact.go` (edit)
- `internal/agent/prune.go` (edit)
- `internal/agent/runner.go` (edit)
- `internal/agent/images_test.go` (new)
- `internal/agent/prune_test.go` (edit)

**Steps.**

1. `internal/agent/prompts.go`, in `buildToolResultMessage` (line 1007): after
   the existing `content` assembly, append one line per image and copy the
   refs onto the returned message:

```go
	const maxPerMessage = 10
	if n := len(result.Images); n > 0 {
		if n > maxPerMessage {
			result.Images = result.Images[:maxPerMessage]
			n = maxPerMessage
		}
		for _, img := range result.Images {
			content += fmt.Sprintf("\n[image: %d×%d %s %s %s]",
				img.Width, img.Height, shortMIME(img.MIME), humanBytes(img.Bytes), shortHash(img.Hash))
		}
	}
```

   and set `Images: result.Images` on both returned `schema.ChatMessage`
   values (the `RoleTool` one and the `RoleUser` one). Add the three small
   helpers to the same file:

```go
func shortMIME(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	}
	return mime
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
```

   The `[image: …]` line is what prune stubs and what the transcript renders, so
   a text-only consumer still reads sensibly.

2. `internal/agent/compact.go`: in `estimateTokens`, inside the per-message
   loop, add:

```go
		for _, img := range m.Images {
			runes += imagepayload.EstimateTokens(img.Width, img.Height) * 4
		}
```

   The function still returns `runes / 4`, so multiplying by 4 converts the
   image token estimate into the rune currency it counts in. Import
   `marshal/internal/imagepayload`.

3. `internal/agent/prune.go`: in pass 2, where a superseded tag's `Content` is
   replaced with the stub, also clear `Images` on that message:

```go
	wire[tag.msgIdx].Images = nil
```

   Add `shedImages`, called from `runner.go` only under the existing overflow
   gate:

```go
// shedImages drops images oldest-first until the estimated prompt fits
// budget. It runs only where pruning already runs — after the prompt has
// overflowed — so the cached prefix is never rewritten on the happy path.
// Tool results shed before user messages: the user's own screenshots are the
// thing they asked about.
func shedImages(wire []schema.ChatMessage, budget int) ([]schema.ChatMessage, int) {
	if estimateTokens(wire) <= budget {
		return wire, 0
	}
	out := append([]schema.ChatMessage(nil), wire...)
	shed := 0
	for pass := 0; pass < 2 && estimateTokens(out) > budget; pass++ {
		for i := range out {
			if len(out[i].Images) == 0 {
				continue
			}
			if pass == 0 && out[i].Role != schema.RoleTool {
				continue
			}
			out[i].Images = nil
			out[i].Content += "\n[image omitted to fit context]"
			shed++
			if estimateTokens(out) <= budget {
				break
			}
		}
	}
	return out, shed
}
```

4. `internal/agent/runner.go`: at the two `pruneStaleToolOutputs` call sites
   (lines 1609 and 1666), after the prune call and inside the same overflow
   branch, call `shedImages(prunedMsgs, turnThreshold)` and use its result. The
   1609 site keeps its `r.calibratedEstimate(...) <= turnThreshold` guard; the
   1666 site (the hard prune) applies shedding unconditionally because it is
   already past the gate.

5. Tests in `internal/agent/images_test.go`:
   - `TestBuildToolResultMessageCarriesImages` — two refs produce two
     `[image: …]` lines and `len(msg.Images) == 2`.
   - `TestBuildToolResultMessageCapsImages` — twelve refs produce ten.
   - `TestEstimateTokensCountsImages` — adding one 1568×882 image raises the
     estimate by `imagepayload.EstimateTokens(1568, 882)`.
   - `TestShedImagesNoOpBelowBudget` — empty images are untouched and `shed == 0`
     when under budget.
   - `TestShedImagesDropsToolResultsFirst` — a tool message and a user message
     both carrying images, budget between them: the tool message loses its
     images, the user message keeps its.
   - `TestShedImagesAppendsNotice` — the shed message's content gains
     `[image omitted to fit context]`.

   In `internal/agent/prune_test.go`, extend the superseded-result test:
   `TestPruneStaleToolOutputsDropsImages` — a superseded result's `Images` is
   nil after pruning while the newest result's images survive.

**Verify.**

```bash
go test ./internal/agent -run 'TestBuildToolResult|TestEstimateTokens|TestShedImages|TestPruneStale' -count=1
go test ./internal/agent -count=1
```

Expected: all pass, including the existing `TestPromptPrefixStableAcrossSteps`
(no change to prefix behaviour yet — images are only in tool results).

---

## Task 5: Vision capability resolution and registry gating

**Goal.** A route knows whether it may be sent images, and a text-only route
is never offered `image.view`.

**Files.**
- `internal/llm/routing/types.go` (edit)
- `internal/llm/routing/router.go` (edit)
- `internal/llm/provider/capabilities.go` (edit)
- `internal/llm/provider/modelcache/capability.go` (new)
- `internal/llm/provider/modelcache/capability_test.go` (new)
- `internal/llm/routing/vision_test.go` (new)

**Steps.**

1. `internal/llm/routing/types.go`: add the `Vision` field to `ModelPreset`
   (after `ToolCalling`) and the `VisionAuto`/`VisionOn`/`VisionOff`
   constants; add `Vision bool` and `VisionForced bool` to `Route` (after
   `MaxOutput`), per Contracts.

2. Create `internal/llm/provider/modelcache/capability.go` per Contracts:
   `CapabilityOverrides`, `Rejection`, `VisionRejected`, `LoadOverrides`,
   `SaveOverrides`, `DefaultCapabilityTTL`. Store in
   `filepath.Join(dataDir, "model_capabilities.json")` through
   `writeFileAtomic`-style tmp+rename with `0600`, mirroring
   `modelcache.Save` at `modelcache.go:89`. `VisionRejected` returns false
   when the entry is older than `ttl` or `ConfigHash` differs.

3. `internal/llm/provider/capabilities.go`: add `Vision *bool` to
   `ModelCapabilities` with its doc comment.

4. `internal/llm/routing/router.go`: add the resolution ladder as a new
   unexported function, called from `Resolve` after the route is built:

```go
// resolveVision answers one question: may this route be sent images. It is
// not a claim about what the model understands.
//
// First match wins: an explicit preset, a learned rejection, a probe, a
// text-only name heuristic, else attempt. Attempt is the default because a
// loud rejection is recoverable (the runner retries without images) while a
// wrong "off" blocks the feature at first use.
func resolveVision(preset ModelPreset, probeVision *bool, rejected bool) (vision, forced bool) {
	switch preset.Vision {
	case VisionOn:
		return true, true
	case VisionOff:
		return false, false
	}
	if rejected {
		return false, false
	}
	if probeVision != nil {
		return *probeVision, false
	}
	if textOnlyModelName(preset.Model) {
		return false, false
	}
	return true, false
}

// textOnlyModelName names families with no vision input at all. It is a
// heuristic, so it only ever answers "no": an unrecognised model is attempted.
func textOnlyModelName(model string) bool {
	m := strings.ToLower(model)
	for _, s := range []string{"whisper", "text-embedding", "embed-", "bge-", "e5-", "rerank"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
```

   Wire it: `Resolve` sets `route.Vision, route.VisionForced = resolveVision(...)`.
   It takes a `*bool` (the probe's `ModelCapabilities.Vision`) rather than the
   `ModelCapabilities` struct itself: `internal/llm/provider` imports
   `internal/llm/routing`, so routing must not import provider.

5. Tests in `internal/llm/routing/vision_test.go`:
   - `TestResolveVisionLadder` — a table over preset on/off/auto × probe
     nil/true/false × rejected absent/present, asserting both return values.
   - `TestResolveVisionExplicitOnIsForced` — `VisionOn` returns `forced == true`
     even when a rejection is recorded.
   - `TestResolveVisionUnrecognisedModelAttempts` — an empty preset, nil probe,
     unknown name yields `vision == true, forced == false`.
   - `TestResolveVisionTextOnlyNameRefused` — `whisper-large-v3` yields false.

   In `internal/llm/provider/modelcache/capability_test.go`:
   - `TestOverridesRoundTrip` — save, load, `VisionRejected` true.
   - `TestOverridesExpire` — a 31-day-old entry is not rejected.
   - `TestOverridesConfigHashMismatch` — a different hash is not rejected.
   - `TestOverridesFileIsPrivate` — `0600`.

6. Registry gating lands with task 8, where `image.view` exists; this task
   only resolves the flag and asserts it.

**Verify.**

```bash
go test ./internal/llm/routing -count=1
go test ./internal/llm/provider/modelcache -count=1
go vet ./internal/llm/routing ./internal/llm/provider/modelcache
```

---

## Task 6: Transcript persistence

**Goal.** An image ref survives a save and a reload, and an old database still
loads.

**Files.**
- `internal/db/migrations.go` (edit)
- `internal/db/sessions.go` (edit)
- `internal/app/session/messages.go` (edit)
- `internal/app/session/images_test.go` (new)
- `internal/db/images_test.go` (new)

**Steps.**

1. `internal/db/migrations.go`: append a fourth migration in `init()`:

```go
	migrations = append(migrations, migrateMessageImages)
```

   and add, following `migrateMemoryScopes` (line 313):

```go
// migrateMessageImages (version 4) adds images_json to messages: the JSON
// array of image refs a message carried. The default '' parses to no images,
// which is what every existing row holds.
func migrateMessageImages(tx *sql.Tx) error {
	var has int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'images_json'`).Scan(&has); err != nil {
		return err
	}
	if has > 0 {
		return nil
	}
	_, err := tx.Exec(`ALTER TABLE messages ADD COLUMN images_json TEXT NOT NULL DEFAULT ''`)
	return err
}
```

2. `internal/db/sessions.go`:
   - Add `ImagesJSON string` to `db.Message` (after `StepSeq`), documented as
     the JSON array of image refs the message carried; `""` means none.
   - Extend `SaveMessageStep`'s INSERT (line 241) with `images_json`, passing a
     new parameter. To avoid changing `SaveMessage`'s exported signature (used
     from `internal/app/session` and tests), add a sibling:

```go
// SaveMessageWithImages is SaveMessageStep plus the message's image refs,
// already marshalled to JSON. An empty imagesJSON stores ''.
func (db *DB) SaveMessageWithImages(sessionID, role, content, contentType string, createdAt time.Time, reasoning string, thinkDuration time.Duration, final bool, parentID, stepSeq int64, imagesJSON string) (int64, error)
```

     and have `SaveMessageStep` call it with `""`, so existing callers are
     unchanged.
   - `MessagesOnBranch` and `GetMessages` select `images_json` into
     `db.Message.ImagesJSON`.

3. `internal/app/session/messages.go`:
   - Add `Images []schema.ImageRef` to `Message` (after `StepID`).
   - In `appendMessage` (line 321) and `appendMessageStepped` (line 327), add
     an `images []schema.ImageRef` parameter. That is a wide signature change;
     keep it narrow instead by adding one new entry point and leaving the
     eight existing callers alone:

```go
// AddMessageWithImages is AddMessage plus the message's images. Every other
// AddMessage* keeps its signature and passes no images.
func (s *State) AddMessageWithImages(role Role, content string, contentType ContentType, images []schema.ImageRef) {
	s.appendMessageStepped(role, content, contentType, false, false, "", 0, "", 0, images)
}
```

     Add the `images []schema.ImageRef` parameter as the last parameter of
     `appendMessage` and `appendMessageStepped`, and pass `nil` from every
     existing caller in the file.
   - Marshal in the persist path: before calling `db.SaveMessageWithImages`,
     `json.Marshal(m.Images)` and pass the string; a marshal error is logged
     and stored as `""`.
   - In `loadFromDB`, after each row becomes a `Message`, unmarshal
     `row.ImagesJSON` into `Images`. A parse failure is a logged error and an
     empty slice — never a failed load.

4. Tests. `internal/db/images_test.go`:
   - `TestMigrationAddsImagesJSON` — open a DB, assert the column exists and an
     old row reads as `""`.
   - `TestSaveMessageWithImagesRoundTrip` — save with a JSON array, read it
     back through `MessagesOnBranch`.

   `internal/app/session/images_test.go`:
   - `TestImagesSurviveLoadFromDB` — add a message with two refs, reload the
     state, assert both refs are present.
   - `TestCorruptImagesJSONLoadsEmpty` — write a non-JSON value directly, assert
     the load succeeds with no images.

**Verify.**

```bash
go test ./internal/db -count=1
go test ./internal/app/session -count=1
```

---

## Task 7: ACP — `images.put`, prompt attachments, refusal

**Goal.** A marshal client can upload an image once and attach its ref to a
prompt; a refusal arrives as `vision_unsupported` naming the cause.

**Files.**
- `internal/acpwire/images.go` (new)
- `internal/acpwire/turns.go` (edit)
- `internal/acpwire/errors.go` (edit)
- `internal/acp/service/` (edit: the method handler and prompt conversion)
- `internal/acp/images_test.go` (new)
- `internal/acpwire/images_test.go` (new)

**Steps.**

1. Create `internal/acpwire/images.go` with `MethodImagesPut`, `PromptImage`,
   `ImagesPutParams`, `ImagesPutResult` per Contracts.

2. `internal/acpwire/turns.go`: add `Images []PromptImage \`json:"images,omitempty"\``
   to `PromptParams` (after `Queued`).

3. `internal/acpwire/errors.go`: add `CodeVisionUnsupported` in the const block.

4. `internal/acp/service`: register `session/images_put`. It takes
   `ImagesPutParams`, base64-decodes `Data`, calls `Store.Put` with
   `Options{Label: p.Label}`, and returns `ImagesPutResult`. Locate the
   existing method-handler table with `repo.search` for `acpwire.MethodPrompt`
   in `internal/acp/` and follow its shape; the store is reached through the
   service's deps. If `Store` is nil, return `acpwire.CodeNoRunner`-style
   unavailability — pick the existing code for "this dependency is not wired"
   from `internal/acpwire/errors.go` rather than inventing one.

5. In the prompt path (`internal/acp/controls.go:86` is one caller; the
   service's `Prompt` is the other), convert `params.Images` to
   `[]schema.ImageRef` at the seam and hand them to the session's prompt. A ref
   whose hash is not in the store is a *store* failure, not a vision refusal:
   it fails the prompt before the turn starts with a plain error whose message
   is `image <hash> is not in the store`. `CodeVisionUnsupported` is reserved
   for the route refusal in step 6.

6. The route refusal: when `PromptParams.Images` is non-empty and the resolved
   route has `Vision == false`, return an `acpwire.Error` with
   `Code: acpwire.CodeVisionUnsupported` and the message built from
   `schema.VisionError.Detail`, matching spec §7.5's two variants:
   `model <M> has vision off; set [models.presets.<M>] vision = "on" or switch model`
   and `model <M> rejected image input on 2006-01-02`.

7. Tests. `internal/acpwire/images_test.go`:
   - `TestImagesPutParamsRoundTrip` — JSON keys are exactly `sessionId`,
     `data`, `label`, `image`.
   - `TestCodeVisionUnsupported` — the constant is `"vision_unsupported"`.

   `internal/acp/images_test.go` (through the `acptest` seam):
   - `TestImagesPutThenPrompt` — put a 1×1 PNG, prompt with the returned ref,
     assert the replayed `session.Message.Images` holds it.
   - `TestPromptWithUnknownRef` — a made-up hash fails the prompt with the
     "not in the store" message.
   - `TestPromptRefusedWithoutVision` — a route with `Vision == false` returns
     `CodeVisionUnsupported` and the message names the preset line.

**Verify.**

```bash
go test ./internal/acpwire -count=1
go test ./internal/acp -run 'TestImages|TestPromptWithUnknown|TestPromptRefused' -count=1
go test ./internal/acp -count=1
```

---

## Task 8: The `image.view` tool

**Goal.** The model can view an image by path or by ref, and never sees the
tool on a route that cannot take images.

**Files.**
- `internal/tools/native/image.go` (new)
- `internal/tools/native/image_test.go` (new)
- `internal/tools/native/native.go` (edit)
- `internal/app/app.go` (edit)

**Steps.**

1. Create `internal/tools/native/image.go` following the shape of
   `internal/tools/native/file.go`'s `fileReadTool`: a `registry.Tool` whose
   handler parses the schema from spec §9, resolves `path` through the same
   policy/read-only path `file.read` uses (call the existing resolver helper
   in `internal/tools/native/saferesolve.go` rather than re-implementing it),
   opens the file, and calls `Store.Put` with `Options{MaxEdge, Format,
   Quality, Label}` from the arguments.

   Declare `image.view` with `registry.RiskReadOnly` and the JSON schema from
   spec §9 — `path`, `ref`, `max_edge`, `format`, `quality`, `label`, and the
   `oneOf` requiring exactly one of `path`/`ref`. Both set is a validation
   error.

   On `ref`, call `Store.Stat`; a missing hash is an error result naming the
   hash.

   Result text and notice per spec §9:

```go
	result := registry.ToolResult{
		Summary: fmt.Sprintf("%s → %d×%d %s (%s)", label, ref.Width, ref.Height, shortMIME(ref.MIME), humanBytes(ref.Bytes)),
		Content: fmt.Sprintf("hash %s", ref.Hash),
		Images:  []schema.ImageRef{ref},
	}
```

   Add `[downscaled from %d×%d]` to `Content` when the stored dimensions are
   smaller than the source, and a `registry.ToolNotice` of the existing shape
   for a downscale or a format change. `shortMIME`/`humanBytes` live in
   `internal/agent` from task 4 — duplicate them as unexported helpers in
   `internal/tools/native/image.go` rather than importing `agent` (which would
   invert the dependency).

2. `internal/tools/native/native.go`: add `Images *imagepayload.Store` to
   `Options` and gate registration in `RegisterAll` the way `symbols` is
   gated (line 310):

```go
	if tools.images != nil {
		all = append(all, tools.imageViewTool())
	}
```

   Store `opts.Images` on the toolset in `newToolSet`.

3. `internal/app/app.go`: build the store from
   `config.DataDir(home)/images` with `cfg.Images.PayloadConfig()` at runtime
   construction, pass it in `native.Options.Images`, and wrap the registry:

```go
	if !route.Vision {
		r.Registry = agent.DenylistView(r.Registry, []string{"image.view"})
	}
```

   Place it beside the existing custom-agent denylist at `app.go:1801`, so the
   two compose. Also call `store.Sweep` once here with the referenced-hash
   callback (a `SELECT images_json FROM messages JOIN agent_sessions …` query
   over sessions newer than `RetainDays`) — the pinned decision at the top of
   this plan.

4. Tests in `internal/tools/native/image_test.go`:
   - `TestImageViewByPath` — a PNG in a temp workspace returns one ref with the
     file's dimensions.
   - `TestImageViewByRef` — `Stat` hit returns the same ref.
   - `TestImageViewUnknownRef` — a made-up hash is an error result.
   - `TestImageViewRequiresExactlyOneOfPathAndRef` — both set, and neither set,
     are validation errors.
   - `TestImageViewNotRegisteredWithoutStore` — `RegisterAll` with
     `Options{}` does not register `image.view`.
   - `TestImageViewIsReadOnly` — the registered tool's risk is
     `registry.RiskReadOnly`.
   - `TestDenylistRemovesImageView` — `agent.DenylistView` against a registry
     containing `image.view` drops it. (Lives in `internal/agent`'s existing
     `registry_scope_test.go` if the import is awkward here.)

**Verify.**

```bash
go test ./internal/tools/native -run 'TestImageView' -count=1
go test ./internal/tools/native -count=1
go test ./internal/agent -run 'TestDenylist' -count=1
```

---

## Task 9: Learned rejection, retry without images, send-anyway

**Goal.** A loud provider rejection of image input becomes a working text-only
turn and is remembered; an explicit `vision = "on"` surfaces the error
instead.

**Files.**
- `internal/agent/vision_retry.go` (new)
- `internal/agent/vision_retry_test.go` (new)
- `internal/agent/chat.go` (edit: the retry at the request boundary)
- `internal/app/app.go` (edit: wiring the override store)
- `internal/app/session/vision.go` (new: the session override)
- `internal/app/session/vision_test.go` (new)

**Steps.**

1. Create `internal/agent/vision_retry.go`:

```go
// isVisionRejection reports whether err is a provider response that refuses
// image input. It is deliberately narrow: a 400 about request size or an
// unknown parameter must never be read as "this model is blind".
func isVisionRejection(err error) bool {
	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.StatusCode {
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
	default:
		return false
	}
	body := strings.ToLower(pe.Body)
	for _, s := range []string{"image", "vision", "multimodal", "image_url", "does not support images"} {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// withoutImages returns a copy of msgs with every Images list cleared and a
// notice appended to the last message that carried any.
func withoutImages(msgs []schema.ChatMessage) ([]schema.ChatMessage, bool) {
	out := append([]schema.ChatMessage(nil), msgs...)
	last := -1
	for i := range out {
		if len(out[i].Images) > 0 {
			out[i].Images = nil
			last = i
		}
	}
	if last < 0 {
		return msgs, false
	}
	out[last].Content += "\n[images omitted: model rejected image input]"
	return out, true
}
```

   Add `net/http`, `errors`, `strings` and
   `marshal/internal/llm/provider/modelcache` to `vision_retry.go`'s imports.

2. `internal/agent/chat.go`: at the submission point, after the request is
   built and `p.Chat(ctx, req)` returns an error, add at most one retry:

```go
	events, err := p.Chat(ctx, req)
	if err != nil && r.turnRequestOptions.visionForced == false && carriesImages(req.Messages) && isVisionRejection(err) {
		if retryMsgs, ok := withoutImages(req.Messages); ok {
			retryReq := req
			retryReq.Messages = retryMsgs
			if retryEvents, retryErr := p.Chat(ctx, retryReq); retryErr == nil {
				r.recordVisionRejection(p.Name(), model) // persist the fact
				r.noteVisionRetry()                     // a user-visible notice
				events, err = retryEvents, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
```

   Add three helpers to `internal/agent/vision_retry.go`:

```go
func carriesImages(msgs []schema.ChatMessage) bool {
	for _, m := range msgs {
		if len(m.Images) > 0 {
			return true
		}
	}
	return false
}

// recordVisionRejection persists "this provider/model refuses images".
// Nil-safe: tests and headless paths wire nothing and learn nothing.
func (r *Runner) recordVisionRejection(providerName, model string) {
	if r.VisionOverrides == nil || r.SaveVisionOverrides == nil {
		return
	}
	key := providerName + "/" + model
	r.VisionOverrides.Rejections[key] = modelcache.Rejection{
		ObservedAt: time.Now().UTC(),
		ConfigHash: r.ProviderConfigHash,
	}
	_ = r.SaveVisionOverrides(*r.VisionOverrides)
}

// noteVisionRetry records the user-visible "images omitted" notice on the run
// through the runner's existing notice list.
func (r *Runner) noteVisionRetry() {
	r.notes = append(r.notes, "the model rejected image input; resent without images")
}
```

   `Runner` gains three fields beside the other provider wiring:
   `VisionOverrides *modelcache.CapabilityOverrides`,
   `SaveVisionOverrides func(modelcache.CapabilityOverrides) error`, and
   `ProviderConfigHash string`. `r.notes` is the runner's existing per-run
   notice slice — locate its declaration in `runner.go` and append to it; do
   not add a second notice channel.

   **Exemption:** `turnRequestOptions` gains `visionForced bool`, set from
   `route.VisionForced` (task 5) wherever the other per-turn request options
   are populated.

3. `internal/app/session/vision.go`: the session-scoped send-anyway override.

```go
// ForceVision records that the user chose to send images on this model
// anyway. It is in-memory and session-scoped: the user is answering a
// question about this image on this model, not editing their setup mid-turn.
func (s *State) ForceVision(providerName, model string)
func (s *State) VisionForced(providerName, model string) bool
```

   A `map[string]bool` under `s.mu`.

4. `internal/app/app.go`: load `modelcache.LoadOverrides(dataDir)` at runtime
   construction, pass it to the runner, and consult it when building the route
   (task 5's `rejected` argument). Persist through a save callback that calls
   `modelcache.SaveOverrides`.

5. Tests. `internal/agent/vision_retry_test.go`:
   - `TestIsVisionRejectionMatrix` — a table over status × body: 400 with
     `"does not support images"` true; 400 with `"context length exceeded"`
     false; 415 and 422 with `"image"` true; 500 with `"image"` false.
   - `TestWithoutImagesAppendsNotice` — clears every list, appends the notice
     once, to the last image-bearing message.
   - `TestRetryOnceOnVisionRejection` — a fake provider failing once with a
     vision 400 then succeeding: the turn succeeds, the notice is recorded, the
     override is persisted.
   - `TestNoRetryForSizeRejection` — a 400 about size neither retries nor
     persists.
   - `TestNoRetryWhenForced` — `visionForced: true` surfaces the original error.
   - `TestNoPersistWhenRetryAlsoFails` — the second attempt failing leaves the
     override untouched.

   `internal/app/session/vision_test.go`:
   - `TestForceVisionIsSessionScoped` — set, read true, a different model reads
     false.

**Verify.**

```bash
go test ./internal/agent -run 'TestIsVisionRejection|TestWithoutImages|TestRetryOnce|TestNoRetry|TestNoPersist' -count=1
go test ./internal/agent -count=1
go test ./internal/app/session -run 'TestForceVision' -count=1
```

---

## Task 10: MCP and subagent images

**Goal.** An MCP server's image block and a subagent's image-bearing result
reach the parent model.

**Files.**
- `internal/tools/mcp/protocol.go` (edit)
- `internal/tools/mcp/manager.go` (edit)
- `internal/app/app.go` (edit: pass the store to the MCP manager)
- `internal/tools/mcp/images_test.go` (new)
- `internal/agent/execute.go` (edit: subagent hoist)
- `internal/agent/subagent_images_test.go` (new)

**Steps.**

1. `internal/tools/mcp/protocol.go`: extend `MCPContent` (line 68) with the
   image fields:

```go
type MCPContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`     // base64, for type "image"
	MimeType string `json:"mimeType,omitempty"` // for type "image"
}
```

2. `internal/tools/mcp/manager.go`: in the content loop at line 419, handle
   `content.Type == "image"`: base64-decode `Data`, call the manager's
   `Images *imagepayload.Store` with `Options{Label: serverName + ": image"}`
   and append the ref to a `images []schema.ImageRef`. Bound it to four per
   result. A block that fails to store is dropped and a notice line appended to
   `fullContent`; the rest of the result survives. Set `Images: images` on the
   returned `registry.ToolResult`.

   Add the manager field and its injection point beside the existing
   dependencies; a nil store skips image content entirely (text-only behaviour,
   as today).

3. `internal/agent/execute.go`: at the `agent.run` result handling
   (line 1372), copy up to `MaxPerMessage` refs from the child's last
   image-bearing tool result onto the parent's `registry.ToolResult.Images`,
   labelled `"<agent>: <label>"`. Nothing else is hoisted: the child's
   transcript stays the child's.

4. Tests. `internal/tools/mcp/images_test.go`:
   - `TestMCPImageBlockBecomesRef` — a fake server returning one `type:"image"`
     block yields one ref carrying the server name in its label.
   - `TestMCPImageFailureKeepsText` — a block with invalid base64 leaves the
     text content intact and adds a notice.
   - `TestMCPImageBlocksCappedAtFour` — five blocks yield four refs.

   `internal/agent/subagent_images_test.go`:
   - `TestSubagentImagesHoisted` — a child whose last tool result carried two
     refs yields two on the parent's result.
   - `TestSubagentImagesLabelled` — each hoisted label starts with the agent
     name.

**Verify.**

```bash
go test ./internal/tools/mcp -run 'TestMCPImage' -count=1
go test ./internal/agent -run 'TestSubagentImages' -count=1
```

---

## Task 11: TUI clipboard paste, chips, `/image`

**Goal.** `ctrl+v` inserts an image chip in the composer and `/image <path>`
attaches a file, both reaching the prompt as refs.

**Files.**
- `internal/app/clipboard/image.go` (new)
- `internal/app/clipboard/image_test.go` (new)
- `internal/app/tui/keypress.go` (edit)
- `internal/app/tui/model.go` (edit: the draft + chip)
- `internal/app/tui/images_test.go` (new)
- `internal/commands/commands.go` (edit: `/image`)

**Steps.**

1. Create `internal/app/clipboard/image.go` beside the existing text writer,
   reusing its injected `LookPath`/`Run`/`GOOS`/`Env`/`Timeout` seam:

```go
// ErrNoImage reports that the clipboard holds no image.
var ErrNoImage = errors.New("clipboard has no image")

// Reader reads an image out of the local clipboard.
type Reader interface {
	ReadImage(ctx context.Context) ([]byte, error)
}

// LocalImageReader reads through a local clipboard executable.
//
// The zero value is usable. Candidates, in order: darwin pngpaste, then an
// osascript fallback writing a temp PNG; linux wl-paste under Wayland, else
// xclip. A missing executable is ErrUnavailable, so the caller can say which
// package to install.
type LocalImageReader struct {
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, path string, args []string) ([]byte, error)
	GOOS     string
	Env      func(string) string
	Timeout  time.Duration
}
```

   Candidates: `pngpaste -`; `wl-paste --type image/png --no-newline`;
   `xclip -selection clipboard -t image/png -o`. The darwin osascript fallback
   writes to a temp file and reads it back. An empty stdout is `ErrNoImage`.

2. `internal/app/tui/keypress.go`: `ctrl+v` currently has no binding; add one
   that calls `ReadImage` through the injected seam and, on success, sends the
   bytes over `images.put` and stores the returned ref in the composer's draft.
   Failure modes are notices, not silent no-ops: "clipboard has no image",
   "install pngpaste" / "install wl-clipboard or xclip" (map
   `ErrUnavailable`/`ErrNoImage` to those strings).

3. `internal/app/tui/model.go`: the draft gains `images []acpwire.PromptImage`.
   The composer renders one chip per image — dimensions, size, label — with
   "will downscale" appended when either edge exceeds the configured
   `max_edge`, and "model can't see images" when the route is known text-only.
   The chip is **not** refused (routing can change before send; spec §7.5).
   `submitLine` (`keypress.go:394`) sends the chips with the prompt.

4. `internal/commands/commands.go`: register `/image <path>` in `RegisterAll`
   (line 51). It reads the file, sends `images.put`, and returns a `Result`
   naming the attached image. It is `TUIOnly` — the agent has `image.view`
   instead. On a missing argument, return the usage line rather than an error.

5. Tests. `internal/app/clipboard/image_test.go`:
   - `TestReadImageCandidates` — `GOOS: "linux"` with and without
     `WAYLAND_DISPLAY` picks `wl-paste` vs `xclip`.
   - `TestReadImageMissingToolIsUnavailable` — `LookPath` failing returns
     `ErrUnavailable`.
   - `TestReadImageEmptyIsNoImage` — a `Run` returning nothing returns
     `ErrNoImage`.

   `internal/app/tui/images_test.go`:
   - `TestCtrlVPasteInsertChip` — through the injected reader and an `acptest`
     agent: one `images.put` call, one chip in the draft.
   - `TestCtrlVNoImageNotices` — `ErrNoImage` produces the notice and no chip.
   - `TestChipMarksDownscale` — a 4000×3000 ref with `max_edge: 1568` renders
     "will downscale".
   - `TestSubmitSendsImageRefs` — submitting sends `PromptParams.Images` with
     the chip's refs and no bytes.
   - `TestSlashImageAttachesPath` — `/image /tmp/x.png` produces one chip.

**Verify.**

```bash
go test ./internal/app/clipboard -count=1
go test ./internal/app/tui -run 'TestCtrlV|TestChip|TestSubmitSendsImage|TestSlashImage' -count=1
go test ./internal/app/tui -run 'TestTUIStaysOnTheClientSeam' -count=1
```

Expected: the boundary test passes with **no allowlist change** — the
`clipboard` package is already allowlisted (`internal/app/tui/boundary_test.go`).

---

## Task 12: Web and fleet image upload

**Goal.** The bridge accepts an image over HTTP, relays it to the project
child, and returns a ref the prompt can carry.

**Files.**
- `web/bridge/contract/images.go` (new)
- `web/ui/src/lib/contract.ts` (regenerated)
- `web/bridge/images.go` (new)
- `web/bridge/http.go` (edit: the route)
- `web/bridge/images_test.go` (new)

**Steps.**

1. Create `web/bridge/contract/images.go` with `PromptImage`, `ImageUpload`,
   `ImageUploadResult` per Contracts. Standard library only — no
   `marshal/internal` import.

2. Regenerate the TypeScript contract:

```bash
go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent -update
```

3. Create `web/bridge/images.go` with a handler for
   `POST /api/projects/images`. Follow `web/bridge/attach.go`'s project-child
   relay: resolve the child for the project root, open its viewer socket, call
   `session/images_put` with `acpwire.ImagesPutParams`, and return
   `contract.ImageUploadResult`. Bearer-token protection comes from the
   existing `/api/` middleware — verify by calling the route without the token
   in a test.

   Body limit: reject a body larger than twice `[images].max_input_bytes`
   (base64 inflates by 4/3) with 413 before decoding.

4. Register the route in `web/bridge/http.go` `routes()` (line 210), beside
   `GET /api/projects`:

```go
	s.mux.HandleFunc("POST /api/projects/images", s.uploadImage)
```

5. Tests in `web/bridge/images_test.go`:
   - `TestUploadImageReturnsRef` — an `httptest` bridge with a fake child
     returns a `PromptImage` whose hash matches the stored bytes.
   - `TestUploadImageRequiresToken` — no bearer token is 401/403.
   - `TestUploadImageRejectsOversizeBody` — a body over the cap is 413 and the
     child is never called.
   - `TestUploadImageChildErrorSurfaces` — a child error becomes a JSON error
     response, not a 500 with a Go stack.

6. The SPA consumes the type; no Svelte change is required for v1 (spec §13:
   the fleet screen and web studio show an image's label). Ensure
   `npm run check` passes.

**Verify.**

```bash
go test ./web/bridge -run 'TestUploadImage' -count=1
go test ./web/bridge/contract/tsgen -count=1
CGO_ENABLED=1 go test ./web/bridge -run 'TestWebIsStdlibOnly' -count=1
cd web/ui && npm run check
```

Expected: all pass, `contract.ts` regenerated and current, `web/` still
standard-library only.

---

## Final verification

Run from the repository root, after the last task:

```bash
gofmt -l .
CGO_ENABLED=1 go build ./cmd/marshal
CGO_ENABLED=1 go build ./cmd/webbridge
go vet ./...
scripts/test.sh quick
scripts/test.sh full
```

Then the boundary and isolation guards explicitly, because they are the ones
this plan is most likely to break:

```bash
CGO_ENABLED=1 go test ./internal/app/tui -run 'TestTUIStaysOnTheClientSeam' -count=1
CGO_ENABLED=1 go test ./web/bridge -run 'TestWebIsStdlibOnly' -count=1
CGO_ENABLED=1 go test ./internal/agent -run 'TestPromptPrefixStableAcrossSteps' -count=1
CGO_ENABLED=1 go test ./internal/agent -run 'TestCrossTurnStableRegionIsAppendOnly' -count=1
```

Expected: `gofmt -l .` prints nothing; both binaries build; `go vet` clean; both
suite modes pass; all four guards pass — in particular the prefix-stability
tests, which this feature must not weaken.

Finally, a manual smoke test, because the automated suite cannot see:

```bash
CGO_ENABLED=1 go build -o ./marshal ./cmd/marshal
./marshal --embedded
```

In the TUI: `/image <a screenshot>` → the chip shows dimensions → submit with
"what does this say?" → the model describes the content. Then switch to a
preset with `vision = "off"` and confirm the prompt is refused with the config
hint, and that "send anyway" resends.

## Integration notes

- **No `web/` import of `marshal/internal`.** The bridge reaches images through
  ACP, and `contract.PromptImage` is the shared shape. `TestWebIsStdlibOnly`
  enforces it.
- **The TUI stays on the client seam.** Clipboard reading is in
  `internal/app/clipboard`, already allowlisted; image upload is an ACP method.
- **Cache stability.** Images are encoded where they already are. The only two
  prefix rewrites are prune's existing shed (task 4) and the task 9 retry,
  which only runs after a request has already failed.
- **`image.capture` and `image.render`** plug in later by calling
  `Store.Put` and setting `ToolResult.Images`; no change to this plan is
  needed for them.
