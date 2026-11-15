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

// fleetIface is the interface FleetRoutes must describe.
var fleetIface = reflect.TypeOf((*Fleet)(nil)).Elem()

// TestFleetRouteTableCoversTheInterface keeps FleetRoutes in step with the
// interface: every Fleet method has exactly one row, every row names a method
// that exists, and no row repeats (P5 decision 3).
func TestFleetRouteTableCoversTheInterface(t *testing.T) {
	seen := make(map[string]bool, len(FleetRoutes))
	for _, row := range FleetRoutes {
		if seen[row.Name] {
			t.Errorf("FleetRoutes repeats %s", row.Name)
		}
		seen[row.Name] = true
	}

	want := make(map[string]bool)
	for i := 0; i < fleetIface.NumMethod(); i++ {
		name := fleetIface.Method(i).Name
		want[name] = true
		if n := len(fleetRowsFor(name)); n != 1 {
			t.Errorf("Fleet.%s has %d rows in FleetRoutes, want exactly 1", name, n)
		}
	}

	for _, row := range FleetRoutes {
		if !want[row.Name] {
			t.Errorf("FleetRoutes row %s names no method of Fleet", row.Name)
		}
	}
}

// TestFleetDocsNameTheirRoutes keeps every "Routes: " doc line in fleet.go in
// step with the corresponding row of FleetRoutes (P5 decision 3).
func TestFleetDocsNameTheirRoutes(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate the test file")
	}
	seamFile := filepath.Join(filepath.Dir(thisFile), "fleet.go")

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, seamFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", seamFile, err)
	}

	// routeLines maps a Fleet method name to its "Routes: " doc line.
	routeLines := make(map[string]string)
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != fleetIface.Name() {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					continue // an embedded interface, not a method
				}
				for _, name := range field.Names {
					routeLines[name.Name] = routesDocLine(field.Doc)
				}
			}
		}
	}

	if len(routeLines) == 0 {
		t.Fatalf("%s declares no Fleet method", seamFile)
	}

	for name, line := range routeLines {
		row, ok := fleetRowFor(name)
		if !ok {
			t.Errorf("Fleet.%s has no row in FleetRoutes", name)
			continue
		}
		if line == "" {
			t.Errorf("Fleet.%s doc has no \"Routes: \" line", name)
			continue
		}
		if want := wantRoutesDoc(row); line != want {
			t.Errorf("Fleet.%s doc reads %q, want %q", name, line, want)
		}
	}
}

// fleetRowsFor returns the rows of FleetRoutes naming one Fleet method.
func fleetRowsFor(name string) []FleetRoute {
	var rows []FleetRoute
	for _, row := range FleetRoutes {
		if row.Name == name {
			rows = append(rows, row)
		}
	}
	return rows
}

// fleetRowFor returns the single row of FleetRoutes naming name.
func fleetRowFor(name string) (FleetRoute, bool) {
	rows := fleetRowsFor(name)
	if len(rows) != 1 {
		return FleetRoute{}, false
	}
	return rows[0], true
}

// routesDocLine returns the method's "Routes: " statement, or "" when it has
// none. The statement may wrap over several lines, so its lines are joined
// into one sentence before it is compared with the row.
func routesDocLine(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, paragraph := range strings.Split(doc.Text(), "\n\n") {
		if !strings.HasPrefix(strings.TrimSpace(paragraph), "Routes: ") {
			continue
		}
		lines := strings.Split(strings.TrimSpace(paragraph), "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		return strings.Join(lines, " ")
	}
	return ""
}

// wantRoutesDoc is the whole doc line the row requires: the routes in row
// order, or "none", and always a closing full stop.
func wantRoutesDoc(row FleetRoute) string {
	if len(row.Routes) == 0 {
		return "Routes: none."
	}
	return "Routes: " + strings.Join(row.Routes, ", ") + "."
}
