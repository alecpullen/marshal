package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"marshal/internal/db"
	"marshal/internal/llm/embedding"
	"marshal/internal/repo"
)

// LSPSymbols is an abstraction over an LSP document-symbol provider.
// The bool reports whether a ready server handled the request.
type LSPSymbols interface {
	DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool)
}

type Deps struct {
	DB         *db.DB
	Root       string
	Ignore     []string
	MaxBytes   int64
	Embedder   embedding.Embedder   // nil => embeddings skipped
	LSP        LSPSymbols           // nil => LSP symbols not available
	OnProgress func(message string) // nil => no progress reporting
	// MaxTotalContentBytes caps the bytes of file content a pass holds in
	// memory at once. Zero means repo.DefaultMaxTotalContentBytes. Files
	// past the cap are indexed by path but their content is omitted, so
	// their symbols and embeddings are skipped rather than guessed.
	MaxTotalContentBytes int64
	// Warnings are conditions the caller already knows about (an embedding
	// backend that could not be reached, say). They are reported ahead of
	// the ones this pass finds.
	Warnings []string
}

type Report struct {
	Files         int
	Symbols       int
	FilesEmbedded int
	ChunksWritten int
	LangCounts    map[string]int
	Warnings      []string
}

// Run performs one full incremental index pass: scan → file index (full
// replace) → tree-sitter symbols (full replace) → embeddings (incremental).
func Run(ctx context.Context, deps Deps, projectID int64) (Report, error) {
	rep := Report{LangCounts: map[string]int{}, Warnings: append([]string(nil), deps.Warnings...)}
	if kind := pathologicalIndexRoot(deps.Root); kind != "" {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"index root %s is %s, so this pass walks far more than a project tree; set indexing.root or run Marshal from a project directory",
			deps.Root, kind))
	}

	scanner := repo.NewScanner(repo.Config{
		Root:                  deps.Root,
		Ignore:                deps.Ignore,
		MaxIndexableFileBytes: deps.MaxBytes,
		MaxTotalContentBytes:  deps.MaxTotalContentBytes,
	})
	scanned, err := scanner.ScanDetailed(ctx)
	if err != nil {
		return rep, fmt.Errorf("scan repo: %w", err)
	}

	rep.Warnings = append(rep.Warnings, scanner.Warnings()...)
	// Skips are summarised in one bounded line rather than one warning per
	// path: a mis-rooted workspace skips tens of thousands of entries, and a
	// per-file list would bury the diagnostics that matter.
	if skipped := scanner.Skipped(); len(skipped) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"scan skipped %d entries; first: %s (%s)",
			len(skipped), skipped[0].Path, skipped[0].Reason))
	}

	files := make([]db.FileIndex, len(scanned))
	now := time.Now().UTC()
	for i, sf := range scanned {
		files[i] = sf.FileIndex
		files[i].LastIndexedAt = now
		if files[i].Language != "" {
			rep.LangCounts[files[i].Language]++
		}
	}
	if err := deps.DB.SaveFileIndex(projectID, files); err != nil {
		return rep, fmt.Errorf("save file index: %w", err)
	}
	rep.Files = len(files)

	var symbols []db.Symbol
	symbolsByFile := map[string][]db.Symbol{}
	for _, sf := range scanned {
		if sf.ReadErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": read error")
			continue
		}
		if sf.ContentOmitted {
			// The file index above still covers this file; only its symbols are
			// unavailable, because symbol extraction needs the bytes.
			continue
		}
		if deps.LSP != nil {
			if lspSyms, ok := deps.LSP.DocumentSymbols(ctx, sf.Language, sf.Path, sf.Content); ok {
				symbols = append(symbols, lspSyms...)
				symbolsByFile[sf.Path] = lspSyms
				continue
			}
		}
		if !repo.SupportedLanguage(sf.Language) {
			continue
		}
		fileSyms, extractErr := repo.ExtractSymbols(ctx, sf.Language, sf.Path, sf.Content)
		if extractErr != nil {
			rep.Warnings = append(rep.Warnings, sf.Path+": parse error")
			continue
		}
		for i := range fileSyms {
			fileSyms[i].Source = "treesitter"
		}
		symbols = append(symbols, fileSyms...)
		symbolsByFile[sf.Path] = fileSyms
	}
	if err := deps.DB.SaveSymbols(projectID, symbols); err != nil {
		return rep, fmt.Errorf("save symbols: %w", err)
	}
	rep.Symbols = len(symbols)

	ix := NewIndexer(deps.DB, deps.Embedder)
	ix.onProgress = deps.OnProgress
	st, err := ix.Reindex(ctx, projectID, scanned, symbolsByFile)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "embedding: "+err.Error())
	}
	rep.FilesEmbedded = st.FilesEmbedded
	rep.ChunksWritten = st.ChunksWritten
	return rep, nil
}

// pathologicalIndexRoot names the kind of root that makes an index pass walk
// orders of magnitude more than a project tree, or returns "" for an ordinary
// root. Such a root is still indexed — refusing would break non-git project
// directories — but the pass says so rather than silently spending minutes on
// it.
func pathologicalIndexRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return "a filesystem root"
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == abs {
		return "the user's home directory"
	}
	return ""
}

