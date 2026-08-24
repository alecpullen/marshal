package repo

import (
	"context"
	"strings"
	"testing"
)

const goSrc = `package p

import "fmt"

type Scanner struct {
	n int
}

func Alpha() {
	fmt.Println("a")
}

func (s *Scanner) Beta() int {
	return s.n
}

func Gamma() {
	fmt.Println("g")
}
`

func hitNames(hits []SymbolHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Name)
	}
	return out
}

func lineOf(t *testing.T, src, needle string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("needle %q not found", needle)
	return 0
}

func TestChangedSymbolsInsideFunction(t *testing.T) {
	ln := lineOf(t, goSrc, `fmt.Println("a")`)
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: ln, End: ln + 1}})
	if err != nil {
		t.Fatalf("ChangedSymbols: %v", err)
	}
	if got := hitNames(hits); len(got) != 1 || got[0] != "Alpha" {
		t.Fatalf("got %v, want [Alpha]", got)
	}
}

// A change inside a method must attribute to the method, not to a type that
// happens to enclose or precede it. Innermost-first ordering is the rule.
func TestChangedSymbolsMethodNotEnclosingType(t *testing.T) {
	ln := lineOf(t, goSrc, "return s.n")
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: ln, End: ln + 1}})
	if err != nil {
		t.Fatalf("ChangedSymbols: %v", err)
	}
	if len(hits) == 0 || hits[0].Name != "Beta" {
		t.Fatalf("first hit = %v, want Beta", hitNames(hits))
	}
	if hits[0].Kind != "method" {
		t.Fatalf("Beta kind = %q, want method", hits[0].Kind)
	}
	if hits[0].Receiver == "" {
		t.Fatal("method hit lost its receiver")
	}
}

func TestChangedSymbolsSpanningTwoFunctions(t *testing.T) {
	start := lineOf(t, goSrc, "func Alpha()")
	end := lineOf(t, goSrc, "func Gamma()") + 1
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: start, End: end}})
	if err != nil {
		t.Fatalf("ChangedSymbols: %v", err)
	}
	names := map[string]bool{}
	for _, n := range hitNames(hits) {
		names[n] = true
	}
	for _, want := range []string{"Alpha", "Beta", "Gamma"} {
		if !names[want] {
			t.Errorf("missing %q in %v", want, hitNames(hits))
		}
	}
}

func TestChangedSymbolsOutsideAnySymbol(t *testing.T) {
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: 1, End: 2}})
	if err != nil {
		t.Fatalf("ChangedSymbols: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("package clause must attribute to nothing, got %v", hitNames(hits))
	}
}

// Imports are not a meaningful attribution subject and are filtered out.
func TestChangedSymbolsSkipsImports(t *testing.T) {
	ln := lineOf(t, goSrc, `import "fmt"`)
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: ln, End: ln + 1}})
	if err != nil {
		t.Fatalf("ChangedSymbols: %v", err)
	}
	for _, h := range hits {
		if h.Kind == "import" {
			t.Fatalf("imports must be filtered out, got %v", hitNames(hits))
		}
	}
}

// THE correctness test for blast radius. Slice the source at the reported
// position and it must be the symbol's own name. An error here does not
// crash — it queries the wrong place and returns the wrong callers.
func TestChangedSymbolsNamePositionPointsAtTheName(t *testing.T) {
	lines := strings.Split(goSrc, "\n")
	for _, tc := range []struct{ needle, name string }{
		{`fmt.Println("a")`, "Alpha"},
		{"return s.n", "Beta"},
		{`fmt.Println("g")`, "Gamma"},
	} {
		ln := lineOf(t, goSrc, tc.needle)
		hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), []LineRange{{Start: ln, End: ln + 1}})
		if err != nil || len(hits) == 0 {
			t.Fatalf("%s: hits=%v err=%v", tc.name, hitNames(hits), err)
		}
		h := hits[0]
		if !h.Resolved() {
			t.Fatalf("%s: name position unresolved", tc.name)
		}
		line := lines[h.NameLine-1]
		if h.NameCol < 0 || h.NameCol+len(h.Name) > len(line) {
			t.Fatalf("%s: NameCol %d out of range on %q", tc.name, h.NameCol, line)
		}
		if got := line[h.NameCol : h.NameCol+len(h.Name)]; got != h.Name {
			t.Fatalf("%s: source at (%d,%d) = %q, want %q", tc.name, h.NameLine, h.NameCol, got, h.Name)
		}
	}
}

// A method whose name matches its receiver type is the case a naive
// "find the name on the declaration line" search gets wrong: the receiver
// appears first.
func TestChangedSymbolsMethodNameMatchingReceiver(t *testing.T) {
	src := "package p\n\ntype Symbol struct{}\n\nfunc (s *Symbol) Symbol() int {\n\treturn 1\n}\n"
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(src), []LineRange{{Start: 6, End: 7}})
	if err != nil || len(hits) == 0 {
		t.Fatalf("hits=%v err=%v", hitNames(hits), err)
	}
	h := hits[0]
	if !h.Resolved() {
		t.Fatal("unresolved name position")
	}
	line := strings.Split(src, "\n")[h.NameLine-1]
	if got := line[h.NameCol : h.NameCol+len(h.Name)]; got != "Symbol" {
		t.Fatalf("resolved to %q at col %d in %q", got, h.NameCol, line)
	}
	// It must be the method's name, not the receiver type's occurrence.
	if h.NameCol <= strings.Index(line, "*Symbol") {
		t.Fatalf("resolved to the receiver, not the method name (col %d in %q)", h.NameCol, line)
	}
}

func TestChangedSymbolsUnsupportedLanguageIsNilNotError(t *testing.T) {
	hits, err := ChangedSymbols(context.Background(), "ruby", "a.rb", []byte("def foo\nend\n"), []LineRange{{Start: 1, End: 2}})
	if err != nil {
		t.Fatalf("unsupported language must not error, got %v", err)
	}
	if hits != nil {
		t.Fatalf("want nil hits, got %v", hitNames(hits))
	}
}

func TestChangedSymbolsNoRangesIsNil(t *testing.T) {
	hits, err := ChangedSymbols(context.Background(), "go", "p.go", []byte(goSrc), nil)
	if err != nil || hits != nil {
		t.Fatalf("hits=%v err=%v; want nil, nil", hitNames(hits), err)
	}
}

func TestChangedSymbolsPythonAndTypeScript(t *testing.T) {
	py := "def alpha():\n    return 1\n\ndef beta():\n    return 2\n"
	hits, err := ChangedSymbols(context.Background(), "python", "a.py", []byte(py), []LineRange{{Start: 2, End: 3}})
	if err != nil || len(hits) == 0 || hits[0].Name != "alpha" {
		t.Fatalf("python: hits=%v err=%v", hitNames(hits), err)
	}
	ts := "export function alpha(): number {\n  return 1;\n}\n"
	hits, err = ChangedSymbols(context.Background(), "typescript", "a.ts", []byte(ts), []LineRange{{Start: 2, End: 3}})
	if err != nil || len(hits) == 0 || hits[0].Name != "alpha" {
		t.Fatalf("typescript: hits=%v err=%v", hitNames(hits), err)
	}
}
