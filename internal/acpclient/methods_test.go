package acpclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// seamInterfaces are the interfaces Methods must describe.
var seamInterfaces = []reflect.Type{
	reflect.TypeOf((*Host)(nil)).Elem(),
	reflect.TypeOf((*Session)(nil)).Elem(),
}

// TestMethodTableCoversTheInterfaces keeps Methods in step with the interfaces:
// every interface method has exactly one row, every row names a method that
// exists, and no row repeats (C2, T4).
func TestMethodTableCoversTheInterfaces(t *testing.T) {
	seen := make(map[string]bool, len(Methods))
	for _, row := range Methods {
		key := row.Iface + "." + row.Name
		if seen[key] {
			t.Errorf("Methods repeats %s", key)
		}
		seen[key] = true
	}

	want := make(map[string]bool)
	for _, iface := range seamInterfaces {
		for i := 0; i < iface.NumMethod(); i++ {
			key := iface.Name() + "." + iface.Method(i).Name
			want[key] = true
			if n := len(rowsFor(iface.Name(), iface.Method(i).Name)); n != 1 {
				t.Errorf("%s has %d rows in Methods, want exactly 1", key, n)
			}
		}
	}

	for _, row := range Methods {
		key := row.Iface + "." + row.Name
		if !want[key] {
			t.Errorf("Methods row %s names no method of %s", key, row.Iface)
		}
	}
}

// TestMethodDocsNameTheirACPMethod keeps every "ACP:" doc line in acpclient.go
// in step with the corresponding row of Methods (C2).
func TestMethodDocsNameTheirACPMethod(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate the test file")
	}
	seamFile := filepath.Join(filepath.Dir(thisFile), "acpclient.go")

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, seamFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", seamFile, err)
	}

	// acpLines maps "Iface.Name" to the method's "ACP: " doc line.
	acpLines := make(map[string]string)
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok || !isSeamInterface(typeSpec.Name.Name) {
				continue
			}
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					continue // an embedded interface, not a method
				}
				for _, name := range field.Names {
					acpLines[typeSpec.Name.Name+"."+name.Name] = acpDocLine(field.Doc)
				}
			}
		}
	}

	if len(acpLines) == 0 {
		t.Fatalf("%s declares no Host or Session method", seamFile)
	}

	for key, line := range acpLines {
		row, ok := rowFor(key)
		if !ok {
			t.Errorf("%s has no row in Methods", key)
			continue
		}
		if line == "" {
			t.Errorf("%s doc has no \"ACP: \" line", key)
			continue
		}
		want := wantACPDoc(row)
		if !strings.HasPrefix(line, want) {
			t.Errorf("%s doc reads %q, want a line starting %q", key, line, want)
		}
	}
}

// isSeamInterface reports whether name is one of the interfaces Methods maps.
func isSeamInterface(name string) bool {
	for _, iface := range seamInterfaces {
		if iface.Name() == name {
			return true
		}
	}
	return false
}

// rowsFor returns the rows of Methods for one interface method.
func rowsFor(iface, name string) []Method {
	var rows []Method
	for _, row := range Methods {
		if row.Iface == iface && row.Name == name {
			rows = append(rows, row)
		}
	}
	return rows
}

// rowFor returns the single row of Methods naming key, "Iface.Name".
func rowFor(key string) (Method, bool) {
	rows := rowsFor(strings.SplitN(key, ".", 2)[0], strings.SplitN(key, ".", 2)[1])
	if len(rows) != 1 {
		return Method{}, false
	}
	return rows[0], true
}

// acpDocLine returns the first "ACP: " line of a method's doc comment, or "".
func acpDocLine(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, line := range strings.Split(doc.Text(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ACP: ") {
			return line
		}
	}
	return ""
}

// wantACPDoc is the prefix the row's doc line must start with.
func wantACPDoc(row Method) string {
	switch {
	case row.ACP == "":
		return "ACP: none"
	case !row.Exists:
		return "ACP: " + row.ACP + " (new in P3)"
	default:
		return "ACP: " + row.ACP + " (exists)"
	}
}
