package context

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultInlineThreshold is the size below which content is stored inline.
	DefaultInlineThreshold = 10 * 1024 // 10KB

	// DefaultIndexSizeLimit is the maximum size for FTS5 indexing.
	DefaultIndexSizeLimit = 1024 * 1024 // 1MB

	// BlobSubdirCount is the number of subdirectories for blob sharding.
	BlobSubdirCount = 256
)

// ContentAddresser handles content-addressed storage operations.
type ContentAddresser struct {
	blobDir         string
	inlineThreshold int
	indexSizeLimit  int
}

// NewContentAddresser creates a new content addresser.
func NewContentAddresser(blobDir string) *ContentAddresser {
	return &ContentAddresser{
		blobDir:         blobDir,
		inlineThreshold: DefaultInlineThreshold,
		indexSizeLimit:  DefaultIndexSizeLimit,
	}
}

// WithInlineThreshold sets a custom inline threshold.
func (ca *ContentAddresser) WithInlineThreshold(threshold int) *ContentAddresser {
	ca.inlineThreshold = threshold
	return ca
}

// WithIndexSizeLimit sets a custom index size limit.
func (ca *ContentAddresser) WithIndexSizeLimit(limit int) *ContentAddresser {
	ca.indexSizeLimit = limit
	return ca
}

// ComputeHash computes the SHA256 hash of content.
func (ca *ContentAddresser) ComputeHash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// GetBlobPath returns the path for a blob file.
func (ca *ContentAddresser) GetBlobPath(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(ca.blobDir, hash[:1], hash+".blob")
	}
	prefix := hash[:2]
	return filepath.Join(ca.blobDir, prefix, hash+".blob")
}

// GetRelativeBlobPath returns the path relative to blobDir.
func (ca *ContentAddresser) GetRelativeBlobPath(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(hash[:1], hash+".blob")
	}
	return filepath.Join(hash[:2], hash+".blob")
}

// Store stores content and returns its hash and storage type.
// Returns inline=true if stored inline (small content), false if file-backed.
func (ca *ContentAddresser) Store(content []byte) (hash string, storageType StorageType, err error) {
	hash = ca.ComputeHash(content)

	if len(content) <= ca.inlineThreshold {
		return hash, StorageInline, nil
	}

	blobPath := ca.GetBlobPath(hash)

	// Check if already exists (deduplication)
	if _, err := os.Stat(blobPath); err == nil {
		return hash, StorageFileBacked, nil
	}

	// Ensure directory exists
	dir := filepath.Dir(blobPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating blob directory: %w", err)
	}

	// Write file atomically
	tmpPath := blobPath + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0644); err != nil {
		return "", "", fmt.Errorf("writing blob: %w", err)
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		os.Remove(tmpPath)
		return "", "", fmt.Errorf("finalizing blob: %w", err)
	}

	return hash, StorageFileBacked, nil
}

// Load retrieves content by hash.
func (ca *ContentAddresser) Load(hash string, storageType StorageType, inlineContent []byte) ([]byte, error) {
	if storageType == StorageInline {
		if inlineContent == nil {
			return nil, fmt.Errorf("inline content is nil for inline storage type")
		}
		return inlineContent, nil
	}

	blobPath := ca.GetBlobPath(hash)
	content, err := os.ReadFile(blobPath)
	if err != nil {
		return nil, fmt.Errorf("reading blob %s: %w", hash, err)
	}

	// Verify hash
	actualHash := ca.ComputeHash(content)
	if actualHash != hash {
		return nil, fmt.Errorf("hash mismatch: expected %s, got %s", hash, actualHash)
	}

	return content, nil
}

// Delete removes a blob file (if file-backed).
func (ca *ContentAddresser) Delete(hash string, storageType StorageType) error {
	if storageType == StorageInline {
		return nil
	}

	blobPath := ca.GetBlobPath(hash)
	if err := os.Remove(blobPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing blob %s: %w", hash, err)
	}

	return nil
}

// Exists checks if content exists for a given hash.
func (ca *ContentAddresser) Exists(hash string, storageType StorageType) bool {
	if storageType == StorageInline {
		return true // inline content always "exists" in the database
	}

	blobPath := ca.GetBlobPath(hash)
	_, err := os.Stat(blobPath)
	return err == nil
}

// ShouldIndex returns true if content should be indexed in FTS5.
func (ca *ContentAddresser) ShouldIndex(content []byte) bool {
	return len(content) <= ca.indexSizeLimit
}

// ShouldStoreInline returns true if content should be stored inline.
func (ca *ContentAddresser) ShouldStoreInline(content []byte) bool {
	return len(content) <= ca.inlineThreshold
}

// GetSize returns the blob size, or -1 if not found.
func (ca *ContentAddresser) GetSize(hash string, storageType StorageType, inlineSize int) (int64, error) {
	if storageType == StorageInline {
		return int64(inlineSize), nil
	}

	blobPath := ca.GetBlobPath(hash)
	stat, err := os.Stat(blobPath)
	if err != nil {
		return -1, err
	}
	return stat.Size(), nil
}

// BlobManager manages blob storage lifecycle.
type BlobManager struct {
	addresser *ContentAddresser
}

// NewBlobManager creates a new blob manager.
func NewBlobManager(blobDir string) *BlobManager {
	return &BlobManager{
		addresser: NewContentAddresser(blobDir),
	}
}

// EnsureDirectories creates all blob subdirectories (00-ff).
func (bm *BlobManager) EnsureDirectories() error {
	for i := 0; i < BlobSubdirCount; i++ {
		subdir := fmt.Sprintf("%02x", i)
		path := filepath.Join(bm.addresser.blobDir, subdir)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("creating blob subdirectory %s: %w", subdir, err)
		}
	}
	return nil
}

// CleanEmptyDirectories removes empty blob subdirectories.
func (bm *BlobManager) CleanEmptyDirectories() error {
	entries, err := os.ReadDir(bm.addresser.blobDir)
	if err != nil {
		return fmt.Errorf("reading blob directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdir := filepath.Join(bm.addresser.blobDir, entry.Name())
		subentries, err := os.ReadDir(subdir)
		if err != nil {
			continue
		}
		if len(subentries) == 0 {
			os.Remove(subdir)
		}
	}

	return nil
}

// GetStats returns statistics about blob storage.
func (bm *BlobManager) GetStats() (BlobStats, error) {
	stats := BlobStats{}

	entries, err := os.ReadDir(bm.addresser.blobDir)
	if err != nil {
		return stats, fmt.Errorf("reading blob directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdir := filepath.Join(bm.addresser.blobDir, entry.Name())
		subentries, err := os.ReadDir(subdir)
		if err != nil {
			continue
		}
		stats.SubdirCount++
		for _, subentry := range subentries {
			if subentry.IsDir() || !strings.HasSuffix(subentry.Name(), ".blob") {
				continue
			}
			info, err := subentry.Info()
			if err != nil {
				continue
			}
			stats.BlobCount++
			stats.TotalBytes += info.Size()
		}
	}

	return stats, nil
}

// BlobStats contains statistics about blob storage.
type BlobStats struct {
	SubdirCount int
	BlobCount   int
	TotalBytes  int64
}

// VerifyBlobIntegrity checks that a blob's content matches its hash.
func (bm *BlobManager) VerifyBlobIntegrity(hash string) error {
	blobPath := bm.addresser.GetBlobPath(hash)
	content, err := os.ReadFile(blobPath)
	if err != nil {
		return fmt.Errorf("reading blob: %w", err)
	}

	actualHash := bm.addresser.ComputeHash(content)
	if actualHash != hash {
		return fmt.Errorf("hash mismatch: expected %s, got %s", hash, actualHash)
	}

	return nil
}

// Iterator iterates over all blobs.
type Iterator struct {
	blobDir string
	subdirs []os.DirEntry
	blobs   []os.DirEntry
	subIdx  int
	blobIdx int
}

// NewIterator creates a blob iterator.
func (bm *BlobManager) NewIterator() (*Iterator, error) {
	entries, err := os.ReadDir(bm.addresser.blobDir)
	if err != nil {
		return nil, err
	}

	var subdirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 2 {
			subdirs = append(subdirs, e)
		}
	}

	return &Iterator{
		blobDir: bm.addresser.blobDir,
		subdirs: subdirs,
		subIdx:  -1,
	}, nil
}

// Next returns the next blob hash. Returns empty string when done.
func (it *Iterator) Next() (hash string, err error) {
	for {
		// Load next subdir if needed
		if it.blobIdx >= len(it.blobs) {
			it.subIdx++
			if it.subIdx >= len(it.subdirs) {
				return "", nil
			}
			subdir := filepath.Join(it.blobDir, it.subdirs[it.subIdx].Name())
			entries, err := os.ReadDir(subdir)
			if err != nil {
				continue
			}
			it.blobs = nil
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".blob") {
					it.blobs = append(it.blobs, e)
				}
			}
			it.blobIdx = 0
			continue
		}

		blob := it.blobs[it.blobIdx]
		it.blobIdx++

		hash = strings.TrimSuffix(blob.Name(), ".blob")
		return hash, nil
	}
}
