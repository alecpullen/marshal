package sandbox

import "testing"

func TestDetectRuntimeRejectsUnknownName(t *testing.T) {
	name, path, ok := DetectRuntime("definitely-not-a-runtime")
	if ok {
		t.Fatalf("DetectRuntime(unknown) = (%q, %q, true), want ok=false", name, path)
	}
	if name != "" || path != "" {
		t.Fatalf("DetectRuntime(unknown) = (%q, %q), want empty strings", name, path)
	}
}

func TestDetectRuntimeMatchesUnexported(t *testing.T) {
	wantName, wantPath, wantOK := detectRuntime("auto")
	name, path, ok := DetectRuntime("auto")
	if name != wantName || path != wantPath || ok != wantOK {
		t.Fatalf("DetectRuntime(auto) = (%q, %q, %v), want (%q, %q, %v)",
			name, path, ok, wantName, wantPath, wantOK)
	}
}
