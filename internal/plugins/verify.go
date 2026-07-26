package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// HashDir returns "sha256:<hex>" covering every regular file under dir:
// each file contributes its slash-separated relative path and its contents,
// visited in lexical order (filepath.WalkDir guarantees this), so the hash
// is deterministic across runs and platforms. Non-regular files (symlinks,
// sockets, devices) are skipped. This is the tamper signal compared against
// the lockfile's content_hash at startup.
func HashDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash directory %s: %w", dir, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
