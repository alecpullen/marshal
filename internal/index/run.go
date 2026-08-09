package index

import (
	"context"
	"fmt"
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
	rep := Report{LangCounts: map[string]int{}}

	scanner := repo.NewScanner(repo.Config{Root: deps.Root, Ignore: deps.Ignore, MaxIndexableFileBytes: deps.MaxBytes})
	scanned, err := scanner.ScanDetailed(ctx)
	if err != nil {
		return rep, fmt.Errorf("scan repo: %w", err)
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
		if deps.LSP != nil {
			if lspSyms, ok := deps.LSP.DocumentSymbols(ctx, sf.Language, sf.Path, sf.Content); ok {
				symbols = append(symbols, lspSyms...)
				symbolsByFile[sf.Path] = lspSyms
				continue
			}
		}
		if sf.Language != "go" {
			continue
		}
		fileSyms, extractErr := repo.ExtractSymbols(ctx, sf.Path, sf.Content)
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
