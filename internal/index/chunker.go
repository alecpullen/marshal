package index

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"marshal/internal/db"
	"marshal/internal/repo"
)

const (
	maxChunkLines = 200 // oversized symbol split threshold
	windowLines   = 60  // symbol-less fallback window
	windowOverlap = 10
)

// estimateTokens is a local rune/4 estimate (kept out of contextpack so the
// write path does not depend on the passive layer).
func estimateTokens(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}

// Chunk splits a scanned file into embeddable chunks. symbols are the file's
// extracted symbols (may be empty).
func Chunk(file repo.ScannedFile, symbols []db.Symbol) []db.Chunk {
	lines := strings.Split(string(file.Content), "\n")
	switch {
	case isProse(file.Language, file.Path):
		return chunkProse(file, lines)
	case len(codeSymbols(symbols)) > 0:
		return chunkBySymbols(file, lines, codeSymbols(symbols))
	default:
		return chunkWindows(file, lines)
	}
}

func isProse(lang, path string) bool {
	return lang == "markdown" || strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".markdown")
}

// codeSymbols keeps only function/method/type symbols (imports excluded).
func codeSymbols(symbols []db.Symbol) []db.Symbol {
	var out []db.Symbol
	for _, s := range symbols {
		if s.Kind == "function" || s.Kind == "method" || s.Kind == "type" {
			out = append(out, s)
		}
	}
	return out
}

func header(file repo.ScannedFile, s db.Symbol) string {
	recv := ""
	if s.Receiver != "" {
		recv = s.Receiver + "."
	}
	sig := s.Signature
	if sig == "" {
		sig = recv + s.Name
	}
	return fmt.Sprintf("// %s — %s\n", file.Path, sig)
}

func sliceLines(lines []string, start, end int) string {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func chunkBySymbols(file repo.ScannedFile, lines []string, symbols []db.Symbol) []db.Chunk {
	var out []db.Chunk
	for _, s := range symbols {
		h := header(file, s)
		if s.LineEnd-s.LineStart+1 <= maxChunkLines {
			body := sliceLines(lines, s.LineStart, s.LineEnd)
			out = append(out, newChunk(file, "code", s.Name, s.LineStart, s.LineEnd, h+body))
			continue
		}
		for start := s.LineStart; start <= s.LineEnd; start += maxChunkLines {
			end := start + maxChunkLines - 1
			if end > s.LineEnd {
				end = s.LineEnd
			}
			body := sliceLines(lines, start, end)
			out = append(out, newChunk(file, "code", s.Name, start, end, h+body))
		}
	}
	return out
}

func chunkWindows(file repo.ScannedFile, lines []string) []db.Chunk {
	var out []db.Chunk
	h := fmt.Sprintf("// %s\n", file.Path)
	step := windowLines - windowOverlap
	if step < 1 {
		step = windowLines
	}
	for start := 1; start <= len(lines); start += step {
		end := start + windowLines - 1
		if end > len(lines) {
			end = len(lines)
		}
		body := sliceLines(lines, start, end)
		if strings.TrimSpace(body) == "" {
			continue
		}
		out = append(out, newChunk(file, "code", "", start, end, h+body))
		if end == len(lines) {
			break
		}
	}
	return out
}

func chunkProse(file repo.ScannedFile, lines []string) []db.Chunk {
	var out []db.Chunk
	sectionStart := 1
	heading := ""
	flush := func(end int) {
		body := sliceLines(lines, sectionStart, end)
		if strings.TrimSpace(body) == "" {
			return
		}
		out = append(out, newChunk(file, "doc", heading, sectionStart, end, body))
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			if i+1 > sectionStart {
				flush(i) // flush previous section up to the line before this heading
			}
			sectionStart = i + 1
			heading = strings.TrimLeft(strings.TrimSpace(line), "# ")
		}
	}
	flush(len(lines))
	return out
}

func newChunk(file repo.ScannedFile, kind, symbol string, start, end int, content string) db.Chunk {
	return db.Chunk{
		FilePath:   file.Path,
		FileHash:   file.Hash,
		Kind:       kind,
		SymbolName: symbol,
		StartLine:  start,
		EndLine:    end,
		Content:    content,
		TokenCount: estimateTokens(content),
	}
}
