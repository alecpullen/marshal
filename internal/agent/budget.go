package agent

const (
	// maxOverheadTurns caps the turns a model may waste without doing work:
	// silence, a truncated response that had to be re-issued, the grounding
	// nudge, an ask_user round-trip. Each of those can loop on its own, so
	// they still need a ceiling — just not the same one as real work.
	maxOverheadTurns = 20

	// planStepIterations is the extra tool budget granted per known step of
	// work. A step legitimately costs several rounds (read, edit, verify),
	// so a flat cap sized for a single edit cuts multi-step tasks short.
	planStepIterations = 8

	// maxBudgetMultiplier ceilings the scaled budget relative to the
	// configured base, so a model that writes a 200-item todo list cannot
	// talk itself into an unbounded turn.
	maxBudgetMultiplier = 4
)

// turnBudget separates the two things the old single iteration counter
// conflated. tools counts model turns that did real work — executed tool
// calls, or produced a parsed action. overhead counts turns the model
// wasted. Burning the work budget on overhead is what made long tasks die
// early, so each gets its own ceiling.
//
// maxTools starts at the configured MaxToolIterations and is raised by
// grantSteps as the task's real size becomes known (see scaledMaxTools).
type turnBudget struct {
	tools    int
	overhead int

	base     int // configured MaxToolIterations; 0 disables the ceiling
	class    TaskClass
	steps    int // known units of work: plan steps or todo items
	maxTools int
}

func newTurnBudget(base int, class TaskClass, steps int) *turnBudget {
	b := &turnBudget{base: base, class: class, steps: steps}
	b.maxTools = b.scaledMaxTools()
	return b
}

// scaledMaxTools derives this task's tool ceiling from the configured base.
// Questions are left at the base — a "what does X do?" turn can legitimately
// need many reads, so shrinking it would trade one premature cutoff for
// another. Everything else earns headroom per known step of work.
func (b *turnBudget) scaledMaxTools() int {
	if b.base <= 0 {
		return b.base // 0 or negative: no ceiling, honored by exhausted()
	}
	if b.class == ClassQuestion || b.steps <= 0 {
		return b.base
	}
	return min(b.base+b.steps*planStepIterations, b.base*maxBudgetMultiplier)
}

// grantSteps raises the ceiling when the task turns out to be larger than it
// looked at the start — a todo list written on the third iteration is the
// usual case, since plan_first is off by default. The ceiling only ever
// grows, so a shrinking todo list cannot strand a turn mid-flight.
func (b *turnBudget) grantSteps(steps int) {
	if steps <= b.steps {
		return
	}
	b.steps = steps
	if scaled := b.scaledMaxTools(); scaled > b.maxTools {
		b.maxTools = scaled
	}
}

// exhaustionReason reports which ceiling tripped, or "" — the overhead
// cap (maxOverheadTurns) and the tool ceiling have different remedies
// and must not share a finalize reason.
func (b *turnBudget) exhaustionReason() finalizeReason {
	if b.overhead >= maxOverheadTurns {
		return reasonOverhead
	}
	if b.maxTools > 0 && b.tools >= b.maxTools {
		return reasonExhausted
	}
	return ""
}

func (b *turnBudget) exhausted() bool {
	return b.exhaustionReason() != ""
}

// remainingTools reports how much work budget is left, for the finalize
// pressure check. An unbounded budget never applies pressure.
func (b *turnBudget) remainingTools() int {
	if b.maxTools <= 0 {
		return maxOverheadTurns // never within finalizePressureThreshold
	}
	return b.maxTools - b.tools
}

// total is every model turn consumed, which is what TurnMetrics.Iterations
// has always reported.
func (b *turnBudget) total() int { return b.tools + b.overhead }

// reclassifyAsOverhead moves one charge from the work budget to overhead.
// RunTask charges a turn to tools before the calls execute, so a turn whose
// every tool call was rejected before running has to be corrected after the
// fact — nothing ran, so it was overhead.
func (b *turnBudget) reclassifyAsOverhead() {
	if b.tools > 0 {
		b.tools--
		b.overhead++
	}
}
