package sdd

import "testing"

func TestDetectLoopsClean(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	p.Append(ws, "T1", "DISPATCH_IMPL", "branch", "sdd/T1")
	p.Append(ws, "T1", "MERGED", "branch", "sdd/T1")
	p.Append(ws, "T2", "DISPATCH_IMPL", "branch", "sdd/T2")
	alert, err := DetectLoops(&p, ws, 10)
	if err != nil {
		t.Fatal(err)
	}
	if alert != nil {
		t.Fatalf("expected no alert, got %+v", alert)
	}
}

func TestDetectLoopsTriggers(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	// Same dispatch signature 3x without a MERGED.
	for i := 0; i < 3; i++ {
		p.Append(ws, "T1", "DISPATCH_IMPL", "branch", "sdd/T1")
	}
	alert, err := DetectLoops(&p, ws, 10)
	if err != nil {
		t.Fatal(err)
	}
	if alert == nil || alert.Kind != "LOOP" {
		t.Fatalf("expected LOOP alert, got %+v", alert)
	}
}

func TestDetectStagnationClean(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	p.Append(ws, "T1", "MERGED", "branch", "sdd/T1")
	alert, err := DetectStagnation(&p, ws, 5)
	if err != nil {
		t.Fatal(err)
	}
	if alert != nil {
		t.Fatalf("expected no alert, got %+v", alert)
	}
}

func TestDetectStagnationTriggers(t *testing.T) {
	ws, _ := NewWorkspace(t.TempDir())
	ws.Ensure()
	var p Progress
	// 5 lines, none of which are MERGED/INTEGRATED/FINALIZE.
	for i := 0; i < 5; i++ {
		p.Append(ws, "T1", "DISPATCH_IMPL", "branch", "sdd/T1")
	}
	alert, err := DetectStagnation(&p, ws, 5)
	if err != nil {
		t.Fatal(err)
	}
	if alert == nil || alert.Kind != "STAGNATION" {
		t.Fatalf("expected STAGNATION alert, got %+v", alert)
	}
}
