package watch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder captures the OnFire/OnEvent seam payloads for assertions.
type recorder struct {
	mu     sync.Mutex
	fires  []Report
	events []Event
}

func (r *recorder) deps() Deps {
	return Deps{
		OnFire: func(rep Report) {
			r.mu.Lock()
			r.fires = append(r.fires, rep)
			r.mu.Unlock()
		},
		OnEvent: func(e Event) {
			r.mu.Lock()
			r.events = append(r.events, e)
			r.mu.Unlock()
		},
	}
}

func (r *recorder) firedReports() []Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Report(nil), r.fires...)
}

func (r *recorder) watchEvents(state State) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.State == state {
			out = append(out, e)
		}
	}
	return out
}

func TestStartResumeForcesNotify(t *testing.T) {
	m := newTestManager(t, Deps{})
	off := false
	id, note, err := m.Start(Spec{Name: "x", Kind: KindCommand, Notify: &off, Resume: true, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if w := m.getWatch(id); w == nil || !w.notify {
		t.Fatalf("resume watch notify = %v, want true", w.notify)
	}
	if !strings.Contains(note, "forces notify") {
		t.Fatalf("note = %q, want it to mention forcing notify", note)
	}
}

func TestStartOwnedWatchDropsResume(t *testing.T) {
	m := newTestManager(t, Deps{})
	off := false
	id, note, err := m.Start(Spec{Name: "x", Kind: KindCommand, Notify: &off, Resume: true, Owner: "sa-1", Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	w := m.getWatch(id)
	if w == nil {
		t.Fatal("watch not found")
	}
	if w.resume {
		t.Fatal("owned watch kept resume intent")
	}
	if w.notify {
		t.Fatal("owned watch resume drop did not preserve explicit notify=false")
	}
	if !strings.Contains(note, "ignored") {
		t.Fatalf("note = %q, want it to mention the ignored resume", note)
	}
}

func TestFireReportAndEventCarryResume(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Resume: true, Condition: "change", Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a"}, {Stdout: "b"}}})
	w := m.getWatch(id)
	m.sampleOnce(w) // baseline: change needs a previous sample
	m.sampleOnce(w) // fires

	fires := rec.firedReports()
	if len(fires) != 1 {
		t.Fatalf("reports = %d, want 1", len(fires))
	}
	if !fires[0].Resume {
		t.Fatalf("report %+v does not carry resume", fires[0])
	}
	events := rec.watchEvents(StateFired)
	if len(events) != 1 {
		t.Fatalf("fired events = %d, want 1", len(events))
	}
	if !events[0].Resume || events[0].Mode != ModeOnce {
		t.Fatalf("fired event = %+v, want Resume true and Mode once", events[0])
	}
}

func TestRepeatDedupFireCarriesNoResumeEvent(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	id, _, err := m.Start(Spec{Name: "r", Kind: KindCommand, Mode: ModeRepeat, Resume: true, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	w := m.getWatch(id)
	m.fire(w, Sample{Stdout: "a"})
	m.fire(w, Sample{Stdout: "a"})

	if fires := rec.firedReports(); len(fires) != 1 {
		t.Fatalf("reports = %d, want 1 (deduped second fire)", len(fires))
	}
	events := rec.watchEvents(StateFired)
	if len(events) != 2 {
		t.Fatalf("fired events = %d, want 2", len(events))
	}
	if !events[0].Resume {
		t.Fatalf("first fired event = %+v, want resume", events[0])
	}
	if events[1].Resume {
		t.Fatalf("deduped fired event = %+v, want no resume", events[1])
	}
}

func TestStopEventCarriesNoResume(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	id, _, err := m.Start(Spec{Name: "s", Kind: KindCommand, Mode: ModeRepeat, Resume: true, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if fires := rec.firedReports(); len(fires) != 0 {
		t.Fatalf("stop pushed %d reports, want 0", len(fires))
	}
	events := rec.watchEvents(StateStopped)
	if len(events) != 1 {
		t.Fatalf("stopped events = %d, want 1", len(events))
	}
	if events[0].Resume {
		t.Fatalf("stopped event = %+v, want no resume", events[0])
	}
}

func TestAutoStopErrorReportCarriesResume(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	id, _, err := m.Start(Spec{Name: "e", Kind: KindCommand, Resume: true, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.setSampler(&fakeSampler{errs: []error{errors.New("boom"), errors.New("boom"), errors.New("boom")}})
	w := m.getWatch(id)
	for i := 0; i < MaxConsecutiveErrors; i++ {
		m.sampleOnce(w)
	}

	fires := rec.firedReports()
	if len(fires) != 1 {
		t.Fatalf("reports = %d, want 1 (auto-stop)", len(fires))
	}
	if !fires[0].IsError || !fires[0].Resume {
		t.Fatalf("auto-stop report = %+v, want IsError and Resume", fires[0])
	}
	events := rec.watchEvents(StateStopped)
	if len(events) != 1 {
		t.Fatalf("stopped events = %d, want 1", len(events))
	}
	if !events[0].Resume {
		t.Fatalf("auto-stop event = %+v, want resume", events[0])
	}
}

func TestEventOwnerModePropagate(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	id, _, err := m.Start(Spec{Name: "o", Kind: KindCommand, Owner: "sa-1", Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	watching := rec.watchEvents(StateWatching)
	if len(watching) != 1 {
		t.Fatalf("watching events = %d, want 1", len(watching))
	}
	if watching[0].Owner != "sa-1" || watching[0].Mode != ModeOnce {
		t.Fatalf("watching event = %+v, want owner sa-1 and mode once", watching[0])
	}
	m.fire(m.getWatch(id), Sample{Stdout: "a"})
	fired := rec.watchEvents(StateFired)
	if len(fired) != 1 {
		t.Fatalf("fired events = %d, want 1", len(fired))
	}
	if fired[0].Owner != "sa-1" || fired[0].Mode != ModeOnce {
		t.Fatalf("fired event = %+v, want owner sa-1 and mode once", fired[0])
	}
}

func TestResumeGoalWithQuote(t *testing.T) {
	goal := ResumeGoal("build", "I'll wait for the build to finish.", false)
	if !strings.Contains(goal, `Watch "build" fired`) {
		t.Fatalf("goal = %q, want the watch label", goal)
	}
	if !strings.Contains(goal, `You last said: "I'll wait for the build to finish.".`) {
		t.Fatalf("goal = %q, want the quoted last statement", goal)
	}
	if strings.Contains(goal, "repeat watch") {
		t.Fatalf("goal = %q, want no repeat hint for a once watch", goal)
	}
}

func TestResumeGoalWithoutQuote(t *testing.T) {
	goal := ResumeGoal("build", "   ", false)
	if strings.Contains(goal, "You last said") {
		t.Fatalf("goal = %q, want no quote sentence for an empty last statement", goal)
	}
	if !strings.Contains(goal, "pick up where you left off") {
		t.Fatalf("goal = %q, want the resume tail", goal)
	}
}

func TestResumeGoalTruncatesQuote(t *testing.T) {
	long := strings.Repeat("x", 500)
	goal := ResumeGoal("build", long, false)
	const prefix = `You last said: "`
	i := strings.Index(goal, prefix)
	if i < 0 {
		t.Fatalf("goal = %q, want the quote prefix", goal)
	}
	rest := goal[i+len(prefix):]
	j := strings.Index(rest, `".`)
	if j < 0 {
		t.Fatalf("goal = %q, want a closing quote", goal)
	}
	quoted := rest[:j]
	if n := len([]rune(quoted)); n > 402 {
		t.Fatalf("quoted region is %d runes, want <= 402", n)
	}
	if !strings.HasSuffix(quoted, "…") {
		t.Fatalf("quoted region = %q, want a truncation ellipsis", quoted)
	}
}

func TestResumeGoalRepeatSuffix(t *testing.T) {
	// The repeat hint must not name watch.stop: the model-facing watch.*
	// tools were retired, so the model cannot stop a watch itself.
	if goal := ResumeGoal("build", "", true); !strings.Contains(goal, "repeat watch") {
		t.Fatalf("repeat goal = %q, want the repeat hint", goal)
	}
	if goal := ResumeGoal("build", "", true); strings.Contains(goal, "watch.stop") {
		t.Fatalf("repeat goal = %q, must not name the retired watch.stop tool", goal)
	}
	if goal := ResumeGoal("build", "", false); strings.Contains(goal, "repeat watch") {
		t.Fatalf("once goal = %q, want no repeat hint", goal)
	}
}

// TestWatchStartResumeArgParses was removed with the model-facing watch.*
// tools in the context-diet Tier 2a cut: watch.start is no longer registered
// as a tool, so there is no tool-argument path to parse. Resume intent itself
// is still covered by TestStartResumeForcesNotify and
// TestStartOwnedWatchDropsResume above, which drive Manager.Start directly,
// and the ACP watch surface drives the same Spec fields.
