package native

import (
	"context"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

// symbolsForEdit maps one file's diff to the symbols it changed.
//
// source must be the file's POST-edit content, which both write tools have
// in hand at the point they build their result — so this never re-reads
// from disk and can never race a later write.
//
// It never returns an error. Attribution is a nicety layered on a write
// that already succeeded: an unsupported language, a malformed diff, or a
// parse failure must leave the tool call successful with no symbols, and
// the transcript falls back to its file-only row.
func symbolsForEdit(ctx context.Context, path, source, diff string) []registry.SymbolRef {
	lang := repo.DetectLanguage(path)
	if !repo.SupportedLanguage(lang) {
		return nil
	}
	ranges := repo.DiffRanges(diff)[path]
	if len(ranges) == 0 {
		// The diff header may carry a different spelling of the path than
		// the tool's own (absolute vs relative, "b/" prefixes already
		// stripped). When the diff covers exactly one file, it is this one.
		all := repo.DiffRanges(diff)
		if len(all) != 1 {
			return nil
		}
		for _, v := range all {
			ranges = v
		}
	}
	hits, err := repo.ChangedSymbols(ctx, lang, path, []byte(source), ranges)
	if err != nil || len(hits) == 0 {
		return nil
	}
	refs := make([]registry.SymbolRef, 0, len(hits))
	for _, h := range hits {
		ref := registry.SymbolRef{
			File:     path,
			Name:     h.Name,
			Kind:     h.Kind,
			Receiver: h.Receiver,
		}
		if h.Resolved() {
			// THE conversion: internal/repo is 1-based throughout, LSP is
			// 0-based. This is the only place in the feature it happens.
			ref.Line = h.NameLine - 1
			ref.Col = h.NameCol
			ref.Resolved = true
		}
		refs = append(refs, ref)
	}
	return refs
}
