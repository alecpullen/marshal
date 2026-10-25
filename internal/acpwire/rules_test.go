package acpwire

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestWireImportsOnlyTheStandardLibrary enforces the boundary in spec C1:
// acpwire carries the types both sides of the client seam exchange, so it may
// depend on nothing but the Go standard library — not the rest of marshal, and
// not a third-party module. It is parsed the same way web/bridge's
// TestWebIsStdlibOnly parses web/.
//
// If this fails, do not add an exception: either the code belongs on the agent
// side of ACP, or the data belongs in the JSON contract.
func TestWireImportsOnlyTheStandardLibrary(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range wireSourceFiles(t) {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				t.Fatalf("unquote import in %s: %v", name, uerr)
			}
			if reason, forbidden := forbiddenWireImport(imp); forbidden {
				t.Errorf("%s imports %q: %s", name, imp, reason)
			}
		}
	}
}

// forbiddenWireImport reports whether an import path breaks the boundary. A
// standard library path's first element never contains a dot; a third-party
// path's always does.
func forbiddenWireImport(path string) (reason string, forbidden bool) {
	if strings.HasPrefix(path, "marshal/") {
		return "acpwire must not depend on the agent's Go packages; put the data in the JSON contract instead", true
	}
	first, _, _ := strings.Cut(path, "/")
	if strings.Contains(first, ".") {
		return "acpwire must stay dependency-free so the wire types are standard library only", true
	}
	return "", false
}

// wireSourceFiles lists the package's non-test Go files, relative to the
// package directory (the test's working directory).
func wireSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatalf("no non-test Go files in the package directory")
	}
	return names
}

// wireJSONName is the camelCase rule for a field's json name (spec C3).
var wireJSONName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// wireScalarTypes are the scalar types a wire field may have.
var wireScalarTypes = map[string]bool{
	"bool":    true,
	"string":  true,
	"byte":    true,
	"int":     true,
	"int8":    true,
	"int16":   true,
	"int32":   true,
	"int64":   true,
	"uint":    true,
	"uint8":   true,
	"uint16":  true,
	"uint32":  true,
	"uint64":  true,
	"float32": true,
	"float64": true,
}

// wireRuleViolations applies spec C3 to the parsed package: one message per
// broken rule, formatted as "<Type>.<Field>: <reason>".
//
// A struct field must be named, exported, json-tagged with a camelCase name
// (the only permitted option is omitempty), and typed with a bool, a number, a
// string, a slice, a string-keyed map or pointer of those or of one of this
// package's types, or a json.RawMessage. A non-struct type declaration's
// underlying type must satisfy the same rule.
func wireRuleViolations(fset *token.FileSet, files []*ast.File) []string {
	declared := map[string]bool{}
	for _, f := range files {
		for _, spec := range wireTypeSpecs(f) {
			declared[spec.Name.Name] = true
		}
	}

	var out []string
	for _, f := range files {
		for _, spec := range wireTypeSpecs(f) {
			name := spec.Name.Name
			st, isStruct := spec.Type.(*ast.StructType)
			if !isStruct {
				if reason := wireTypeProblem(spec.Type, declared); reason != "" {
					out = append(out, name+": "+reason)
				}
				continue
			}
			if st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					out = append(out, name+"."+wireExprString(fset, field.Type)+": embedded fields are not allowed")
					continue
				}
				for _, ident := range field.Names {
					label := name + "." + ident.Name
					if !ast.IsExported(ident.Name) {
						out = append(out, label+": field must be exported")
					}
					if reason := wireTagProblem(field.Tag); reason != "" {
						out = append(out, label+": "+reason)
					}
					if reason := wireTypeProblem(field.Type, declared); reason != "" {
						out = append(out, label+": "+reason)
					}
				}
			}
		}
	}
	return out
}

// wireTypeSpecs returns the type declarations in one file.
func wireTypeSpecs(f *ast.File) []*ast.TypeSpec {
	var specs []*ast.TypeSpec
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				specs = append(specs, ts)
			}
		}
	}
	return specs
}

// wireTagProblem reports why a field's struct tag breaks the json rule, or ""
// when it is fine.
func wireTagProblem(tag *ast.BasicLit) string {
	if tag == nil {
		return "field must carry a json tag"
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return "struct tag is not a quoted string"
	}
	value, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return "field must carry a json tag"
	}
	name, opts, _ := strings.Cut(value, ",")
	if !wireJSONName.MatchString(name) {
		return "json name " + strconv.Quote(name) + " must be camelCase"
	}
	if opts == "" {
		return ""
	}
	for _, opt := range strings.Split(opts, ",") {
		if opt != "omitempty" {
			return "json option " + strconv.Quote(opt) + " is not allowed; only omitempty is"
		}
	}
	return ""
}

// wireTypeProblem reports why a type expression is not an allowed wire type,
// or "" when it is.
func wireTypeProblem(expr ast.Expr, declared map[string]bool) string {
	switch t := expr.(type) {
	case *ast.Ident:
		if wireScalarTypes[t.Name] || declared[t.Name] {
			return ""
		}
		return "type " + t.Name + " is not an allowed wire type"
	case *ast.SelectorExpr:
		if isWireRawMessage(t) {
			return ""
		}
		return "type " + wireSelectorString(t) + " is not an allowed wire type; json.RawMessage is the only foreign type"
	case *ast.StarExpr:
		return wireTypeProblem(t.X, declared)
	case *ast.ArrayType:
		if t.Len != nil {
			return "fixed-length arrays are not allowed; use a slice"
		}
		return wireTypeProblem(t.Elt, declared)
	case *ast.MapType:
		key, ok := t.Key.(*ast.Ident)
		if !ok || key.Name != "string" {
			return "map key must be string"
		}
		return wireTypeProblem(t.Value, declared)
	}
	return "type " + wireExprString(token.NewFileSet(), expr) + " is not an allowed wire type"
}

// isWireRawMessage reports whether expr is json.RawMessage.
func isWireRawMessage(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "json" && sel.Sel.Name == "RawMessage"
}

// wireSelectorString renders a qualified type name such as time.Time.
func wireSelectorString(sel *ast.SelectorExpr) string {
	return wireExprString(token.NewFileSet(), sel)
}

// wireExprString renders an expression for a violation message.
func wireExprString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return "expression"
	}
	return buf.String()
}

// TestWireFieldsFollowTheRules applies spec C3 to every type this package
// declares.
func TestWireFieldsFollowTheRules(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range wireSourceFiles(t) {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	for _, msg := range wireRuleViolations(fset, files) {
		t.Errorf("%s", msg)
	}
}

// TestWireRuleCatchesViolations guards the guard: each of the five bad fields
// below must be reported once and once only.
func TestWireRuleCatchesViolations(t *testing.T) {
	src := "package bad\n" +
		"\n" +
		"import \"time\"\n" +
		"\n" +
		"type Bad struct {\n" +
		"\thidden string `json:\"hidden\"`\n" +
		"\tA string\n" +
		"\tB string `json:\"snake_case\"`\n" +
		"\tC time.Time `json:\"c\"`\n" +
		"\tD map[int]string `json:\"d\"`\n" +
		"}\n"

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bad.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	got := wireRuleViolations(fset, []*ast.File{f})
	if len(got) != 5 {
		t.Fatalf("wireRuleViolations reported %d messages, want 5: %v", len(got), got)
	}
	for _, want := range []string{"Bad.hidden", "Bad.A", "Bad.B", "Bad.C", "Bad.D"} {
		found := false
		for _, msg := range got {
			if strings.HasPrefix(msg, want+": ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no violation names %s: %v", want, got)
		}
	}
}
