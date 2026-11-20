package registry

import (
	"context"
	"encoding/json"
	"testing"
)

// TestCommandMetadataSurvivesRegistryViews pins that command preparation and
// path resolution travel with a tool into every view and clone, and that a view
// which wraps preparation hands the caller facts it owns: mutating one
// prepared value must not change the next one.
func TestCommandMetadataSurvivesRegistryViews(t *testing.T) {
	template := CommandFacts{
		Command:       "go test ./...",
		Dir:           "/repo",
		WorkspaceRoot: "/repo",
		HomeDir:       "/home/dev",
		ReadRoots:     []string{"/docs"},
		Env:           map[string]string{"PATH": "/usr/bin"},
		Environment:   []string{"PATH=/usr/bin"},
	}
	// The fixture deliberately returns its template as-is: any view that does
	// not copy the returned facts would expose this shared value to mutation.
	prepare := func(_ context.Context, call ToolCall) (CommandFacts, error) {
		facts := template
		var args struct {
			Command string `json:"command"`
		}
		if len(call.Args) > 0 {
			_ = json.Unmarshal(call.Args, &args)
		}
		if args.Command != "" {
			facts.Command = args.Command
		}
		return facts, nil
	}
	resolver := func(_ context.Context, path string, write bool) (string, error) {
		if write {
			return "/canonical/" + path, nil
		}
		return "/read/" + path, nil
	}
	src := New()
	if err := src.Register(Tool{
		Name: "test.run", Risk: RiskCommand, Internal: true, Handler: nopHandler,
		PrepareCommand: prepare, ResolvePath: resolver,
	}); err != nil {
		t.Fatalf("Register test.run: %v", err)
	}
	if err := src.Register(Tool{
		Name: "file.write", Risk: RiskWorkspaceWrite, Handler: nopHandler, ResolvePath: resolver,
	}); err != nil {
		t.Fatalf("Register file.write: %v", err)
	}

	// A plain lookup keeps the preparation callback.
	plain, ok := src.Lookup("test.run")
	if !ok || plain.PrepareCommand == nil {
		t.Fatal("Lookup lost PrepareCommand")
	}

	// The tester view wraps preparation and strips the override, exactly as it
	// strips the override from execution.
	tester, ok := TesterView(src).Lookup("test.run")
	if !ok {
		t.Fatal("test.run missing from the tester view")
	}
	if tester.PrepareCommand == nil {
		t.Fatal("tester view lost PrepareCommand")
	}
	override := ToolCall{Name: "test.run", Args: []byte(`{"command":"override"}`)}
	facts, err := tester.PrepareCommand(context.Background(), override)
	if err != nil {
		t.Fatalf("tester PrepareCommand: %v", err)
	}
	if facts.Command != "go test ./..." {
		t.Fatalf("tester prepared %q, want the configured command", facts.Command)
	}
	// Mutating the returned slices and map must not reach the fixture, or the
	// next caller would prepare a command policy never saw.
	facts.ReadRoots[0] = "/mutated"
	facts.Env["PATH"] = "/mutated"
	facts.Environment[0] = "PATH=/mutated"
	again, err := tester.PrepareCommand(context.Background(), override)
	if err != nil {
		t.Fatalf("second tester PrepareCommand: %v", err)
	}
	if again.ReadRoots[0] != "/docs" || again.Env["PATH"] != "/usr/bin" || again.Environment[0] != "PATH=/usr/bin" {
		t.Fatalf("prepared facts were not copied: %+v", again)
	}

	// Path resolution survives a view that narrows a handler.
	writer, ok := FallbackWriterView(src, []string{"x.txt"}).Lookup("file.write")
	if !ok || writer.ResolvePath == nil {
		t.Fatal("fallback view lost ResolvePath")
	}
	got, err := writer.ResolvePath(context.Background(), "x.txt", true)
	if err != nil || got != "/canonical/x.txt" {
		t.Fatalf("ResolvePath = %q, %v", got, err)
	}
}
