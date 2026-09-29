package conversation

import (
	"reflect"
	"testing"
)

// ParseCodeFences reports "nothing to copy" as nil, not as an empty slice, so
// a caller can tell a source with no fences from one holding an empty block.
func TestParseCodeFencesEmptySourceIsNil(t *testing.T) {
	if got := ParseCodeFences(""); got != nil {
		t.Fatalf("ParseCodeFences(\"\") = %#v, want nil", got)
	}
}

// Ordinary Markdown is not code: prose, headings, inline code spans and links
// must not be mistaken for a fenced block.
func TestParseCodeFencesWithoutFencesIsNil(t *testing.T) {
	src := "# Heading\n\nSome *prose* with `inline code` and a [link](https://example.com).\n"
	if got := ParseCodeFences(src); got != nil {
		t.Fatalf("ParseCodeFences(prose) = %#v, want nil", got)
	}
}

// The fence syntax is not part of the block: the text is the code alone, and
// the info string's first word is the language.
func TestParseCodeFencesSingleBlock(t *testing.T) {
	src := "```go\nfunc main() {}\n```\n"
	want := []CodeBlock{{Language: "go", Text: "func main() {}\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Blocks come back in source order and are numbered by that order, so a
// caller can address "the second block" without re-deriving it.
func TestParseCodeFencesMultipleBlocksInOrder(t *testing.T) {
	src := "intro\n\n```go\none\n```\n\nmiddle\n\n```python\ntwo\n```\n\noutro\n"
	want := []CodeBlock{
		{Language: "go", Text: "one\n", Index: 0},
		{Language: "python", Text: "two\n", Index: 1},
	}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// A blank line before the closing fence is part of the code, not padding to
// be trimmed: the reader asked for the source, and the source has it.
func TestParseCodeFencesPreservesTrailingBlankLine(t *testing.T) {
	src := "```\nline\n\n```\n"
	want := []CodeBlock{{Language: "", Text: "line\n\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Interior indentation is the code's meaning. Stripping it would turn a
// nested block into a flat one, so every leading space survives.
func TestParseCodeFencesPreservesInteriorIndentation(t *testing.T) {
	src := "```go\nfunc main() {\n    if x {\n        y()\n    }\n}\n```\n"
	want := []CodeBlock{{
		Language: "go",
		Text:     "func main() {\n    if x {\n        y()\n    }\n}\n",
		Index:    0,
	}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Indented lines that look like a nested list are still code, and keep their
// indentation: the parser must not re-read them as Markdown structure.
func TestParseCodeFencesPreservesListLikeIndentation(t *testing.T) {
	src := "```\n- item\n  - nested\n    - deeper\n```\n"
	want := []CodeBlock{{
		Language: "",
		Text:     "- item\n  - nested\n    - deeper\n",
		Index:    0,
	}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// DECISION: the fence's own indentation is NOT subtracted from the code.
// CommonMark would dedent content lines by the fence's indent, but this
// function's contract is "the bytes the author wrote", and a reader who
// copies a block wants the source, not a normalised version of it. The
// column-zero case (every fence in an ordinary answer) is unaffected.
func TestParseCodeFencesIndentedFenceKeepsSourceIndentation(t *testing.T) {
	src := "  ```go\n    x := 1\n  ```\n"
	want := []CodeBlock{{Language: "go", Text: "    x := 1\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Only the first word of the info string is the language; the rest is
// metadata (a title, a filename) that must not leak into the language.
func TestParseCodeFencesLanguageIsFirstInfoWord(t *testing.T) {
	src := "```go title=main.go\nx := 1\n```\n"
	want := []CodeBlock{{Language: "go", Text: "x := 1\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Tilde fences are the other CommonMark fence form and behave identically.
func TestParseCodeFencesTildeFence(t *testing.T) {
	src := "~~~go\nx := 1\n~~~\n"
	want := []CodeBlock{{Language: "go", Text: "x := 1\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// A fence with no info string is still a block; its language is empty rather
// than guessed from the content.
func TestParseCodeFencesWithoutLanguage(t *testing.T) {
	src := "```\nplain\n```\n"
	want := []CodeBlock{{Language: "", Text: "plain\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// Markdown around a fence is not part of it: "Copy code" must not pick up the
// prose that happened to sit next to the block.
func TestParseCodeFencesExcludesSurroundingMarkdown(t *testing.T) {
	src := "# Title\n\nbefore\n\n```go\ncode\n```\n\nafter\n"
	want := []CodeBlock{{Language: "go", Text: "code\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// DECISION: an unterminated fence is a code block that runs to the end of the
// input, which is what CommonMark says an unclosed fence means and what the
// reader sees on screen. Its text is the exact remaining source bytes: no
// trailing newline is invented, because a byte that was never in the source
// must not appear on the clipboard.
func TestParseCodeFencesUnterminatedFenceRunsToEOF(t *testing.T) {
	src := "```go\nx := 1"
	want := []CodeBlock{{Language: "go", Text: "x := 1", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}

// The same rule with a trailing newline: the newline is source, so it stays.
func TestParseCodeFencesUnterminatedFenceKeepsFinalNewline(t *testing.T) {
	src := "```go\nx := 1\n"
	want := []CodeBlock{{Language: "go", Text: "x := 1\n", Index: 0}}
	if got := ParseCodeFences(src); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCodeFences() = %#v, want %#v", got, want)
	}
}
