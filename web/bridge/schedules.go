package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

var (
	// ErrUnknownSchedule is returned when no schedule has the id.
	ErrUnknownSchedule = errors.New("bridge: unknown schedule")
	errInvalidSchedule = errors.New("bridge: invalid schedule")
	// errScheduleActive is returned by run-now while the previous run is
	// still going.
	errScheduleActive = errors.New("bridge: the previous run of this schedule is still active")
)

// Schedule runs a recipe on a UTC cron.
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Recipe  string `json:"recipe"`
	Project string `json:"project,omitempty"`
	// RepoID and Ref run the recipe on a fresh checkout of a registered
	// repo instead of a local project.
	RepoID       string            `json:"repoId,omitempty"`
	Ref          string            `json:"ref,omitempty"`
	Inputs       map[string]string `json:"inputs,omitempty"`
	Cron         string            `json:"cron"`
	Enabled      bool              `json:"enabled"`
	LastRun      time.Time         `json:"lastRun,omitzero"`
	LastRunAgent string            `json:"lastRunAgent,omitempty"`
	LastResult   string            `json:"lastResult,omitempty"`
	OwnerID      string            `json:"ownerId"`
}

// Schedules returns every schedule, sorted by id.
func (w *Workspace) Schedules() []Schedule {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Schedule, 0, len(w.schedules))
	for _, s := range w.schedules {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Schedule returns one schedule by id.
func (w *Workspace) Schedule(id string) (Schedule, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.schedules[id]
	return s, ok
}

// PutSchedule stores a schedule, replacing any with the same id.
func (w *Workspace) PutSchedule(s Schedule) error {
	if s.ID == "" {
		return errors.New("schedule id is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.schedules[s.ID] = s
	return w.save()
}

// RemoveSchedule deletes a schedule.
func (w *Workspace) RemoveSchedule(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.schedules[id]; !ok {
		return ErrUnknownSchedule
	}
	delete(w.schedules, id)
	return w.save()
}

// schedulerState tracks runs in flight, by schedule id.
type schedulerState struct {
	mu       sync.Mutex
	inflight map[string]string // schedule id -> agent id
	lastTick time.Time
}

// validateSchedule checks everything a schedule needs to run later, so a
// bad one is refused when saved rather than failing silently at 3am.
func (f *Fleet) validateSchedule(s Schedule) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{errInvalidSchedule}, a...)...)
	}
	if s.Name == "" {
		return bad("name is required")
	}
	if _, err := ParseCron(s.Cron); err != nil {
		return bad("%v", err)
	}
	rec, err := f.recipes.Get(s.Recipe)
	if err != nil {
		return err
	}
	if _, err := render(rec.Prompt, s.Inputs, rec.Inputs); err != nil {
		return bad("%v", err)
	}
	if s.RepoID != "" {
		if _, ok := f.ws.Repo(s.RepoID); !ok {
			return bad("repo %q is not registered", s.RepoID)
		}
	} else if err := ValidateProjectRoot(s.Project); err != nil {
		return bad("%v", err)
	}
	return nil
}

// StartScheduler runs due schedules until the fleet closes. It ticks every
// minute; clock is the time source (nil means time.Now). Missed ticks, for
// instance while the bridge was down, are not made up.
func (f *Fleet) StartScheduler(clock func() time.Time) {
	if clock == nil {
		clock = time.Now
	}
	f.sched.mu.Lock()
	f.sched.lastTick = clock().UTC().Truncate(time.Minute)
	f.sched.mu.Unlock()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-t.C:
				f.schedulerTick(clock().UTC())
			}
		}
	}()
}

// schedulerTick runs every enabled schedule whose next fire time falls in
// (lastTick, now].
func (f *Fleet) schedulerTick(now time.Time) {
	f.sched.mu.Lock()
	last := f.sched.lastTick
	f.sched.lastTick = now.Truncate(time.Minute)
	f.sched.mu.Unlock()
	if last.IsZero() {
		return
	}
	for _, s := range f.ws.Schedules() {
		if !s.Enabled {
			continue
		}
		c, err := ParseCron(s.Cron)
		if err != nil {
			continue
		}
		if next := c.Next(last); next.IsZero() || next.After(now) {
			continue
		}
		if _, err := f.runSchedule(context.Background(), s.ID, now); err != nil {
			slog.Default().Warn("webbridge: scheduled run failed", "schedule", s.ID, "err", err)
		}
	}
}

func (f *Fleet) updateSchedule(id string, mutate func(*Schedule)) {
	cur, ok := f.ws.Schedule(id)
	if !ok {
		return // deleted while it ran
	}
	mutate(&cur)
	if err := f.ws.PutSchedule(cur); err != nil {
		slog.Default().Warn("webbridge: record schedule result failed", "schedule", id, "err", err)
	}
}

// runSchedule runs one schedule's recipe now. A run that is still active
// is skipped and recorded as such.
func (f *Fleet) runSchedule(ctx context.Context, id string, now time.Time) (string, error) {
	s, ok := f.ws.Schedule(id)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownSchedule, id)
	}
	f.sched.mu.Lock()
	if f.sched.inflight == nil {
		f.sched.inflight = map[string]string{}
	}
	if _, running := f.sched.inflight[id]; running {
		f.sched.mu.Unlock()
		f.updateSchedule(id, func(s *Schedule) { s.LastResult = "skipped: previous run active" })
		return "", errScheduleActive
	}
	f.sched.inflight[id] = "starting"
	f.sched.mu.Unlock()
	release := func() {
		f.sched.mu.Lock()
		delete(f.sched.inflight, id)
		f.sched.mu.Unlock()
	}

	agentID, err := f.RunRecipe(ctx, s.Recipe, RecipeRunRequest{
		Project: s.Project, RepoID: s.RepoID, Ref: s.Ref, Inputs: s.Inputs, Origin: OriginSchedule,
		OnDone: func(res RecipeResult) {
			release()
			f.updateSchedule(id, func(s *Schedule) {
				if res.Err != "" {
					s.LastResult = "error: " + res.Err
				} else {
					s.LastResult = "ok"
				}
			})
		},
	})
	if err != nil {
		release()
		f.updateSchedule(id, func(s *Schedule) { s.LastRun, s.LastResult = now, "failed to start: "+err.Error() })
		return "", err
	}
	f.updateSchedule(id, func(s *Schedule) {
		s.LastRun, s.LastRunAgent = now, agentID
		// OnDone releases the slot before it records its result, so a slot
		// still held means the result has not been written yet. A very
		// short run that already finished keeps its own result.
		f.sched.mu.Lock()
		defer f.sched.mu.Unlock()
		if _, running := f.sched.inflight[id]; running {
			f.sched.inflight[id] = agentID
			s.LastResult = "running"
		}
	})
	return agentID, nil
}

func (s *Server) scheduleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.ws.Schedules())
}

func (s *Server) scheduleSave(create bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body Schedule
		if !decodeJSON(w, r, &body) {
			return
		}
		if create {
			body.ID = newAgentID()
		} else {
			id := r.PathValue("id")
			prev, ok := s.fleet.ws.Schedule(id)
			if !ok {
				writeErr(w, fmt.Errorf("%w: %s", ErrUnknownSchedule, id))
				return
			}
			// Run history belongs to the server, not the client.
			body.ID, body.LastRun, body.LastRunAgent, body.LastResult = id, prev.LastRun, prev.LastRunAgent, prev.LastResult
		}
		body.OwnerID = DefaultOwnerID
		if err := s.fleet.validateSchedule(body); err != nil {
			writeErr(w, err)
			return
		}
		if err := s.fleet.ws.PutSchedule(body); err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.auditf(AuditEvent{Event: AuditScheduleSaved, OwnerID: DefaultOwnerID, Detail: body.ID + " " + body.Recipe})
		status := http.StatusOK
		if create {
			status = http.StatusCreated
		}
		writeJSON(w, status, body)
	}
}

func (s *Server) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.fleet.ws.RemoveSchedule(id); err != nil {
		writeErr(w, fmt.Errorf("%w: %s", err, id))
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditScheduleDeleted, OwnerID: DefaultOwnerID, Detail: id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) scheduleRunNow(w http.ResponseWriter, r *http.Request) {
	agentID, err := s.fleet.runSchedule(r.Context(), r.PathValue("id"), s.fleet.now())
	if errors.Is(err, errScheduleActive) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"agentId": agentID})
}

func (s *Server) scheduleRoutes() {
	s.mux.HandleFunc("GET /api/schedules", s.scheduleList)
	s.mux.HandleFunc("POST /api/schedules", s.scheduleSave(true))
	s.mux.HandleFunc("PUT /api/schedules/{id}", s.scheduleSave(false))
	s.mux.HandleFunc("DELETE /api/schedules/{id}", s.scheduleDelete)
	s.mux.HandleFunc("POST /api/schedules/{id}/run", s.scheduleRunNow)
}
