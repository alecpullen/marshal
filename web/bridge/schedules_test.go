package bridge

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func scheduleRecipe(t *testing.T, f *Fleet) {
	t.Helper()
	if err := f.recipes.Put(Recipe{Name: "nightly", Kind: RecipePrompt, Prompt: "Summarize since {{since}}.",
		Inputs: []RecipeInput{{Name: "since", Required: true}}}); err != nil {
		t.Fatal(err)
	}
}

func putSchedule(t *testing.T, f *Fleet, s Schedule) {
	t.Helper()
	if s.Recipe == "" {
		s.Recipe = "nightly"
	}
	if s.Inputs == nil {
		s.Inputs = map[string]string{"since": "yesterday"}
	}
	s.OwnerID, s.Name = DefaultOwnerID, "n-"+s.ID
	if err := f.ws.PutSchedule(s); err != nil {
		t.Fatal(err)
	}
}

func scheduleAgents(f *Fleet) []Agent {
	var out []Agent
	for _, a := range f.ws.Agents() {
		if a.Origin == OriginSchedule {
			out = append(out, a)
		}
	}
	return out
}

func TestSchedulerFiresAtTheRightMinute(t *testing.T) {
	f, _ := recipeFleet(t, nil)
	scheduleRecipe(t, f)
	putSchedule(t, f, Schedule{ID: "s1", Project: t.TempDir(), Cron: "*/5 * * * *", Enabled: true})
	putSchedule(t, f, Schedule{ID: "off", Project: t.TempDir(), Cron: "* * * * *", Enabled: false})

	f.sched.lastTick = utc("2026-03-02 12:03")
	f.schedulerTick(utc("2026-03-02 12:04"))
	if n := len(scheduleAgents(f)); n != 0 {
		t.Fatalf("%d runs before the due minute", n)
	}
	f.schedulerTick(utc("2026-03-02 12:05"))
	waitFor(t, 5*time.Second, "the run to finish", func() bool {
		s, _ := f.ws.Schedule("s1")
		return s.LastResult == "ok"
	})
	agents := scheduleAgents(f)
	if len(agents) != 1 || agents[0].Recipe != "nightly" {
		t.Fatalf("agents = %+v", agents)
	}
	s, _ := f.ws.Schedule("s1")
	if !s.LastRun.Equal(utc("2026-03-02 12:05")) || s.LastRunAgent != agents[0].ID {
		t.Errorf("schedule = %+v", s)
	}
	// Not due again until 12:10, and a disabled schedule never fires.
	f.schedulerTick(utc("2026-03-02 12:06"))
	f.schedulerTick(utc("2026-03-02 12:09"))
	if n := len(scheduleAgents(f)); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
	// A tick that spans several minutes still fires a schedule due inside it.
	f.schedulerTick(utc("2026-03-02 12:21"))
	waitFor(t, 5*time.Second, "the catch-up run", func() bool { return len(scheduleAgents(f)) == 2 })
}

func TestSchedulerSkipsWhileThePreviousRunIsActive(t *testing.T) {
	release := make(chan struct{})
	f, _ := recipeFleet(t, blockPrompt(release))
	scheduleRecipe(t, f)
	putSchedule(t, f, Schedule{ID: "s1", Project: t.TempDir(), Cron: "* * * * *", Enabled: true})

	f.sched.lastTick = utc("2026-03-02 12:00")
	f.schedulerTick(utc("2026-03-02 12:01"))
	waitFor(t, 5*time.Second, "the first run to start", func() bool { return len(scheduleAgents(f)) == 1 })
	f.schedulerTick(utc("2026-03-02 12:02"))
	if n := len(scheduleAgents(f)); n != 1 {
		t.Fatalf("overlapping run started: %d agents", n)
	}
	if s, _ := f.ws.Schedule("s1"); s.LastResult != "skipped: previous run active" {
		t.Errorf("lastResult = %q", s.LastResult)
	}
	close(release)
	waitFor(t, 5*time.Second, "the first run to end", func() bool {
		f.sched.mu.Lock()
		defer f.sched.mu.Unlock()
		return len(f.sched.inflight) == 0
	})
	waitFor(t, 5*time.Second, "its result", func() bool { s, _ := f.ws.Schedule("s1"); return s.LastResult == "ok" })
	f.schedulerTick(utc("2026-03-02 12:03"))
	waitFor(t, 5*time.Second, "the next run", func() bool { return len(scheduleAgents(f)) == 2 })
}

func TestSchedulerRecordsAFailureToStart(t *testing.T) {
	f, _ := recipeFleet(t, nil)
	scheduleRecipe(t, f)
	putSchedule(t, f, Schedule{ID: "s1", Project: t.TempDir(), Cron: "* * * * *", Enabled: true, Recipe: "gone"})
	f.sched.lastTick = utc("2026-03-02 12:00")
	f.schedulerTick(utc("2026-03-02 12:01"))
	s, _ := f.ws.Schedule("s1")
	if !strings.HasPrefix(s.LastResult, "failed to start:") || !s.LastRun.Equal(utc("2026-03-02 12:01")) {
		t.Errorf("schedule = %+v", s)
	}
	// The slot is free again: the next minute tries again rather than skipping.
	f.schedulerTick(utc("2026-03-02 12:02"))
	if s, _ := f.ws.Schedule("s1"); strings.HasPrefix(s.LastResult, "skipped") {
		t.Errorf("a failed start blocked the next run: %q", s.LastResult)
	}
}

func TestScheduleRunNowRoute(t *testing.T) {
	release := make(chan struct{})
	f, _ := recipeFleet(t, blockPrompt(release))
	scheduleRecipe(t, f)
	putSchedule(t, f, Schedule{ID: "s1", Project: t.TempDir(), Cron: "@daily", Enabled: false})
	s := NewServer(f, "")

	rec := doReq(t, s, http.MethodPost, "/api/schedules/s1/run", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run now = %d %s", rec.Code, rec.Body.String())
	}
	if a := scheduleAgents(f); len(a) != 1 {
		t.Fatalf("agents = %+v", a)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/schedules/s1/run", nil, nil); rec.Code != http.StatusConflict {
		t.Errorf("run now while active = %d", rec.Code)
	}
	close(release)
	if rec := doReq(t, s, http.MethodPost, "/api/schedules/zzz/run", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("run now unknown = %d", rec.Code)
	}
}

func TestScheduleRoutesAndValidation(t *testing.T) {
	f, _ := recipeFleet(t, nil)
	scheduleRecipe(t, f)
	s := NewServer(f, "")
	root := t.TempDir()
	good := map[string]any{"name": "Nightly", "recipe": "nightly", "project": root, "cron": "0 3 * * *",
		"inputs": map[string]string{"since": "1d"}, "enabled": true}

	rec := doReq(t, s, http.MethodPost, "/api/schedules", good, nil)
	var created Schedule
	decodeBody(t, rec, &created)
	if rec.Code != http.StatusCreated || created.ID == "" || created.OwnerID != DefaultOwnerID {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if findEvent(auditTail(t, f), AuditScheduleSaved) == nil {
		t.Error("save not audited")
	}
	var list []Schedule
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/schedules", nil, nil), &list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list = %+v", list)
	}

	mutate := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"bad cron":       {mutate("cron", "61 * * * *"), http.StatusBadRequest},
		"seconds field":  {mutate("cron", "0 0 3 * * *"), http.StatusBadRequest},
		"unknown recipe": {mutate("recipe", "nope"), http.StatusNotFound},
		"missing input":  {mutate("inputs", map[string]string{}), http.StatusBadRequest},
		"relative path":  {mutate("project", "rel"), http.StatusBadRequest},
		"unknown repo":   {mutate("repoId", "ghost"), http.StatusBadRequest},
		"no name":        {mutate("name", ""), http.StatusBadRequest},
	} {
		if rec := doReq(t, s, http.MethodPost, "/api/schedules", tc.body, nil); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), tc.want)
		}
	}

	// A PUT cannot rewrite run history.
	_ = f.ws.PutSchedule(func() Schedule { c := created; c.LastResult = "ok"; c.LastRunAgent = "a9"; return c }())
	put := mutate("cron", "@hourly")
	put["lastResult"], put["lastRunAgent"] = "forged", "forged"
	rec = doReq(t, s, http.MethodPut, "/api/schedules/"+created.ID, put, nil)
	var updated Schedule
	decodeBody(t, rec, &updated)
	if rec.Code != http.StatusOK || updated.Cron != "@hourly" || updated.LastResult != "ok" || updated.LastRunAgent != "a9" || updated.ID != created.ID {
		t.Fatalf("put = %d %+v", rec.Code, updated)
	}
	if rec := doReq(t, s, http.MethodPut, "/api/schedules/zzz", good, nil); rec.Code != http.StatusNotFound {
		t.Errorf("put unknown = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/schedules/"+created.ID, nil, nil); rec.Code != http.StatusOK {
		t.Errorf("delete = %d", rec.Code)
	}
	if findEvent(auditTail(t, f), AuditScheduleDeleted) == nil {
		t.Error("delete not audited")
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/schedules/"+created.ID, nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d", rec.Code)
	}
}
