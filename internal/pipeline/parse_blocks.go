// internal/pipeline/parse_blocks.go
package pipeline

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// marshalBlock is one extracted marshal.* fenced code block. Kind is the
// full fence info string (e.g. "marshal.patch"). Attrs holds key=value
// attributes parsed from the info string. Content is the raw text inside
// the fence. LineStart and LineEnd are 0-indexed line numbers within the
// source body.
type marshalBlock struct {
	kind      string
	attrs     map[string]string
	content   string
	lineStart int
	lineEnd   int
}

// parseMarshalBlocks scans body for fenced code blocks whose info string
// starts with "marshal." and extracts them. Non-marshal blocks and ordinary
// prose are ignored. lineOffset is added to line numbers so callers can
// track positions in the full plan file.
func parseMarshalBlocks(body string, lineOffset int) ([]marshalBlock, error) {
	lines := strings.Split(body, "\n")
	var blocks []marshalBlock
	inFence := false
	var fenceInfo string
	var contentStart int
	var contentLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if strings.HasPrefix(trimmed, "```") {
				info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				if strings.HasPrefix(info, "marshal.") {
					inFence = true
					fenceInfo = info
					contentStart = i + 1
					contentLines = nil
				}
			}
		} else {
			if strings.HasPrefix(trimmed, "```") {
				kind := fenceInfo
				if parts := splitInfoString(fenceInfo); len(parts) > 0 {
					kind = parts[0]
				}
				blk := marshalBlock{
					kind:      kind,
					attrs:     parseInfoAttrs(fenceInfo),
					content:   strings.Join(contentLines, "\n"),
					lineStart: contentStart - 1 + lineOffset,
					lineEnd:   i + lineOffset,
				}
				blocks = append(blocks, blk)
				inFence = false
				fenceInfo = ""
				contentLines = nil
			} else {
				contentLines = append(contentLines, line)
			}
		}
	}

	if inFence {
		return nil, fmt.Errorf("parse blocks: unclosed marshal.* fence starting at line %d", contentStart+lineOffset)
	}

	return blocks, nil
}

// parseInfoAttrs extracts key=value attributes from the fence info string.
// The first token is the kind (e.g. "marshal.patch"); the rest are
// space-separated key="value" pairs.
func parseInfoAttrs(info string) map[string]string {
	attrs := map[string]string{}
	parts := splitInfoString(info)
	if len(parts) <= 1 {
		return attrs
	}
	for _, part := range parts[1:] {
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		val = strings.Trim(val, "\"")
		attrs[key] = val
	}
	return attrs
}

// splitInfoString splits the info string by spaces, respecting quoted
// values that may contain spaces.
func splitInfoString(s string) []string {
	var parts []string
	inQuote := false
	var current strings.Builder
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// compileBlock converts one marshalBlock into a typed Operation. It parses
// the block's content as key=value lines (TOML-like, not full TOML) and
// dispatches to the appropriate operation constructor.
func compileBlock(blk marshalBlock) (Operation, error) {
	props := parseBlockProps(blk.content)
	lineStart, lineEnd := blk.lineStart, blk.lineEnd

	switch blk.kind {
	case "marshal.patch":
		return &PatchOp{
			File:      blk.attrs["file"],
			Search:    extractSEARCH(blk.content),
			Replace:   extractREPLACE(blk.content),
			LineStart: lineStart,
			LineEnd:   lineEnd,
		}, nil

	case "marshal.file":
		return &FileOp{
			Path:      blk.attrs["path"],
			Content:   blk.content,
			Replace:   props["replace"] == "true",
			LineStart: lineStart,
			LineEnd:   lineEnd,
		}, nil

	case "marshal.run":
		cmd, err := parseStringSlice(props["command"])
		if err != nil {
			return nil, fmt.Errorf("compile run: command: %w", err)
		}
		expectExit := 0
		if v, ok := props["expect_exit"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				expectExit = n
			}
		}
		phase := props["phase"]
		if phase == "" {
			phase = "prepare"
		}
		return &RunOp{
			Command:    cmd,
			Phase:      phase,
			ExpectExit: expectExit,
			LineStart:  lineStart,
			LineEnd:    lineEnd,
		}, nil

	case "marshal.assert":
		kind := AssertKind(props["kind"])
		op := &AssertOp{
			Kind:      kind,
			File:      props["file"],
			Symbol:    props["symbol"],
			Content:   props["content"],
			Package:   props["package"],
			LineStart: lineStart,
			LineEnd:   lineEnd,
		}
		if cmd, err := parseStringSlice(props["command"]); err == nil {
			op.Command = cmd
		}
		return op, nil

	case "marshal.agent":
		scope, _ := parseStringSlice(props["scope"])
		return &AgentOp{
			AllowedFiles: scope,
			Reason:       props["reason"],
			LineStart:    lineStart,
			LineEnd:      lineEnd,
		}, nil

	default:
		return nil, fmt.Errorf("compile: unknown marshal block kind %q", blk.kind)
	}
}

// parseBlockProps parses key=value lines from block content. Values may be
// quoted strings or bare words. Lines that look like SEARCH/REPLACE markers
// or patch content (not key=value) are ignored.
func parseBlockProps(content string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "<<") || strings.HasPrefix(trimmed, ">>") || strings.HasPrefix(trimmed, "==") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		val := strings.TrimSpace(trimmed[eq+1:])
		val = strings.Trim(val, "\"")
		props[key] = val
	}
	return props
}

// parseStringSlice parses a JSON-like array string: ["a", "b", "c"].
func parseStringSlice(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// extractSEARCH pulls the SEARCH portion out of a SEARCH/REPLACE block
// content. If the content does not contain SEARCH/REPLACE markers, it is
// returned as-is (the patch engine will handle it).
func extractSEARCH(content string) string {
	lines := strings.Split(content, "\n")
	var searchLines []string
	inSearch := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<<") {
			inSearch = true
			continue
		}
		if strings.HasPrefix(trimmed, "=======") {
			break
		}
		if strings.HasPrefix(trimmed, ">>>>>>>") {
			break
		}
		if inSearch {
			searchLines = append(searchLines, line)
		}
	}
	if len(searchLines) > 0 {
		return strings.Join(searchLines, "\n")
	}
	return content
}

// extractREPLACE pulls the REPLACE portion out of a SEARCH/REPLACE block
// content.
func extractREPLACE(content string) string {
	lines := strings.Split(content, "\n")
	var replaceLines []string
	inReplace := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "=======") {
			inReplace = true
			continue
		}
		if strings.HasPrefix(trimmed, ">>>>>>>") {
			break
		}
		if inReplace {
			replaceLines = append(replaceLines, line)
		}
	}
	if len(replaceLines) > 0 {
		return strings.Join(replaceLines, "\n")
	}
	return ""
}
