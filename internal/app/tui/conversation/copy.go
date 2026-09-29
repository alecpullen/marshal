package conversation

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// CodeBlock is one fenced code block extracted from Markdown source.
type CodeBlock struct {
	// Language is the first word of the fence's info string, or "" when the
	// fence carries no info string.
	Language string
	// Text is the block's source content with the fence lines removed and
	// every other byte preserved: interior indentation, blank lines and
	// trailing whitespace are the author's, not the renderer's.
	Text string
	// Index is the block's 0-based position among the returned blocks.
	Index int
}

// ParseCodeFences returns the fenced code blocks in source, in source order.
// It returns nil when the source contains no fenced code blocks.
//
// The blocks are found by parsing the source as CommonMark with goldmark, so
// both backtick and tilde fences are recognised, an info string is read the
// way Markdown reads it, and an unterminated fence runs to the end of the
// input. The text of each block, however, is sliced straight out of the
// source rather than taken from the parsed node: goldmark's node value is a
// normalised rendering (it dedents by the fence's own indent and invents a
// final newline at EOF), and this function's contract is the opposite — the
// bytes the author wrote, so that "Copy code" puts real source on the
// clipboard instead of a reflowed version of it.
//
// The consequence of slicing source lines is that a fence inside a container
// (a blockquote, a list item) keeps that container's prefix, because the
// prefix is part of the source line. That is deliberate: the function returns
// source, and the caller that wants the container stripped is asking a
// different question.
func ParseCodeFences(source string) []CodeBlock {
	if source == "" {
		return nil
	}
	src := []byte(source)
	root := goldmark.DefaultParser().Parse(text.NewReader(src))

	var blocks []CodeBlock
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		fenced, ok := n.(*ast.FencedCodeBlock)
		if !ok {
			return ast.WalkContinue, nil
		}
		blocks = append(blocks, CodeBlock{
			Language: fenceLanguage(fenced, src),
			Text:     fenceText(fenced, src),
			Index:    len(blocks),
		})
		return ast.WalkContinue, nil
	})
	return blocks
}

// fenceLanguage returns the first word of a fence's info string.
//
// goldmark's Language splits on a space only, so the result is split again on
// any whitespace: "```go title=x" and "```go\ttitle=x" both name "go", and a
// fence with no info string names nothing.
func fenceLanguage(n *ast.FencedCodeBlock, src []byte) string {
	fields := strings.Fields(string(n.Language(src)))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// fenceText returns the block's content as the exact source bytes between the
// opening and closing fence lines.
//
// The parsed node's line segments locate the content: the first segment's
// start is on the first content line and the last segment's stop is the end
// of the last content line, so the span between them is the content and
// nothing else. The span is widened left to the start of the first content
// line because goldmark's segment start is already dedented by the fence's
// indent, and that indent is source the caller asked to keep.
func fenceText(n *ast.FencedCodeBlock, src []byte) string {
	lines := n.Lines()
	if lines.Len() == 0 {
		return ""
	}
	start := lineStart(src, lines.At(0).Start)
	stop := lines.At(lines.Len() - 1).Stop
	if stop > len(src) || start > stop {
		return ""
	}
	return string(src[start:stop])
}

// lineStart returns the index of the first byte of the physical line
// containing pos.
func lineStart(src []byte, pos int) int {
	if pos > len(src) {
		pos = len(src)
	}
	for pos > 0 && src[pos-1] != '\n' {
		pos--
	}
	return pos
}
