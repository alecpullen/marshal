package agent

import "testing"

func TestScaledMaxToolsLeavesPlainTasksAtBase(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 0)
	if b.maxTools != 100 {
		t.Fatalf("maxTools = %d, want 100 (no known steps)", b.maxTools)
	}
}

func TestScaledMaxToolsGrantsHeadroomPerStep(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 5)
	if want := 100 + 5*planStepIterations; b.maxTools != want {
		t.Fatalf("maxTools = %d, want %d", b.maxTools, want)
	}
}

func TestScaledMaxToolsCeilingsAtMultiplier(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 500)
	if want := 100 * maxBudgetMultiplier; b.maxTools != want {
		t.Fatalf("maxTools = %d, want %d (ceiling)", b.maxTools, want)
	}
}

// Questions stay at the base: a deep read-only investigation needs the full
// budget, so shrinking it would trade one premature cutoff for another.
func TestScaledMaxToolsLeavesQuestionsAtBase(t *testing.T) {
	b := newTurnBudget(100, ClassQuestion, 12)
	if b.maxTools != 100 {
		t.Fatalf("maxTools = %d, want 100 (questions are not scaled)", b.maxTools)
	}
}

func TestGrantStepsRaisesCeilingOnlyUpward(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 0)
	b.grantSteps(4)
	raised := b.maxTools
	if want := 100 + 4*planStepIterations; raised != want {
		t.Fatalf("maxTools after grant = %d, want %d", raised, want)
	}
	b.grantSteps(1) // todo list shrank as items completed
	if b.maxTools != raised {
		t.Fatalf("maxTools = %d, want it to stay at %d (ceiling never shrinks)", b.maxTools, raised)
	}
}

// The regression this whole change exists for: overhead turns must not eat
// the work budget.
func TestOverheadDoesNotConsumeToolBudget(t *testing.T) {
	b := newTurnBudget(4, ClassEdit, 0)
	for range 3 {
		b.overhead++
	}
	if b.exhausted() {
		t.Fatal("exhausted() = true after overhead only, want false")
	}
	for range 4 {
		b.tools++
	}
	if !b.exhausted() {
		t.Fatal("exhausted() = false after the tool budget ran out, want true")
	}
}

// Overhead still needs its own ceiling, or ask→decline→ask loops forever.
func TestOverheadHasItsOwnCeiling(t *testing.T) {
	b := newTurnBudget(1000, ClassEdit, 0)
	for range maxOverheadTurns {
		b.overhead++
	}
	if !b.exhausted() {
		t.Fatalf("exhausted() = false after %d overhead turns, want true", maxOverheadTurns)
	}
}

func TestZeroBaseIsUnbounded(t *testing.T) {
	b := newTurnBudget(0, ClassEdit, 0)
	b.tools = 10000
	if b.exhausted() {
		t.Fatal("exhausted() = true for a zero (unbounded) base, want false")
	}
	if b.remainingTools() <= finalizePressureThreshold {
		t.Fatal("an unbounded budget must never apply finalize pressure")
	}
}

func TestTotalCountsBothKinds(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 0)
	b.tools, b.overhead = 3, 2
	if b.total() != 5 {
		t.Fatalf("total() = %d, want 5", b.total())
	}
}

func TestReclassifyAsOverheadMovesOneCharge(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 0)
	b.tools = 3
	b.overhead = 1
	b.reclassifyAsOverhead()
	if b.tools != 2 || b.overhead != 2 {
		t.Fatalf("tools=%d overhead=%d, want 2 and 2", b.tools, b.overhead)
	}
	if b.total() != 4 {
		t.Fatalf("total() = %d, want 4 (reclassifying must not change the total)", b.total())
	}
}

func TestReclassifyAsOverheadIsSafeAtZero(t *testing.T) {
	b := newTurnBudget(100, ClassEdit, 0)
	b.reclassifyAsOverhead()
	if b.tools != 0 || b.overhead != 0 {
		t.Fatalf("tools=%d overhead=%d, want both 0", b.tools, b.overhead)
	}
}
