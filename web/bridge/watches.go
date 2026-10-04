package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"
)

// studioOwner owns watches started on the control agent, and tags their
// fleet deltas where an agent id would otherwise go.
const studioOwner = "studio"

// watchListTimeout bounds each source of GET /api/watches, so one slow
// agent cannot hold the whole page.
const watchListTimeout = 3 * time.Second

// ErrUnknownReroute is returned for a reroute id the bridge does not hold.
var ErrUnknownReroute = errors.New("bridge: unknown reroute")

// errRerouteConflict is returned when a reroute cannot be undone cleanly:
// it was already undone, or the binding changed since.
var errRerouteConflict = errors.New("bridge: reroute cannot be undone")

// Reroute records one automatic rebinding so it can be undone. From and To
// are the role's binding as the engine reports it; a nil From means the
// role had no binding before.
type Reroute struct {
	ID      string          `json:"id"`
	WatchID string          `json:"watchId"`
	Watch   string          `json:"watch"`
	Role    string          `json:"role"`
	Profile string          `json:"profile"`
	From    json.RawMessage `json:"from"`
	To      json.RawMessage `json:"to"`
	At      time.Time       `json:"at"`
	Undone  bool            `json:"undone,omitempty"`
}

// rerouteLog holds reroutes in memory: an undo after a bridge restart is
// not offered, because the binding may have been edited since.
type rerouteLog struct {
	mu   sync.Mutex
	byID map[string]*Reroute
}

func (l *rerouteLog) put(r *Reroute) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = map[string]*Reroute{}
	}
	l.byID[r.ID] = r
}

func (l *rerouteLog) get(id string) (Reroute, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.byID[id]
	if !ok {
		return Reroute{}, false
	}
	return *r, true
}

// claimUndo atomically marks a reroute undone and returns it, so two
// concurrent undos cannot both proceed. releaseUndo reverts the claim when
// the undo then fails.
func (l *rerouteLog) claimUndo(id string) (Reroute, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.byID[id]
	if !ok {
		return Reroute{}, fmt.Errorf("%w: %s", ErrUnknownReroute, id)
	}
	if r.Undone {
		return Reroute{}, fmt.Errorf("%w: already undone", errRerouteConflict)
	}
	r.Undone = true
	return *r, nil
}

func (l *rerouteLog) releaseUndo(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.byID[id]; r != nil {
		r.Undone = false
	}
}

// onControlNotification forwards the control agent's watch events to the
// fleet stream, tagged "studio", and runs a fired watch's reroute rule.
func (f *Fleet) onControlNotification(method string, params json.RawMessage) {
	d, ok := classifyNotification(method, params)
	if !ok || d.Kind != "watch" {
		return
	}
	d.SessionID, d.AgentID = studioOwner, studioOwner
	_, _ = f.fleetLog.Append(fleetStreamKey, d)
	var ev struct {
		WatchID string `json:"watchId"`
		Name    string `json:"name"`
		State   string `json:"state"`
	}
	if json.Unmarshal(d.Watch, &ev) != nil || ev.State != "fired" || ev.WatchID == "" {
		return
	}
	if rule, ok := f.ws.WatchRule(ev.WatchID); ok && rule.Reroute != nil {
		// Off the read goroutine: applying needs round trips to the same
		// control agent this callback is running on.
		go f.applyReroute(ev.WatchID, ev.Name, *rule.Reroute)
	}
}

// routingSnapshot is the slice of config/get a reroute needs. Bindings are
// kept raw so fields the bridge does not know survive the round trip.
type routingSnapshot struct {
	Profiles       map[string]map[string]json.RawMessage `json:"profiles"`
	DefaultProfile string                                `json:"defaultProfile"`
	Presets        map[string]json.RawMessage            `json:"presets"`
	Roles          []string                              `json:"roles"`
}

func (f *Fleet) routingSnapshot(ctx context.Context) (routingSnapshot, error) {
	raw, err := f.controlCall(ctx, "config/get", nil, false)
	if err != nil {
		return routingSnapshot{}, err
	}
	var snap routingSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return routingSnapshot{}, fmt.Errorf("decode config/get: %w", err)
	}
	if snap.Profiles == nil {
		snap.Profiles = map[string]map[string]json.RawMessage{}
	}
	return snap, nil
}

// setRoleBinding writes one role's binding into the active profile and
// saves the whole routing section. A nil binding removes the role. The
// engine replaces profiles wholesale, so every other binding is sent back
// unchanged.
func (f *Fleet) setRoleBinding(ctx context.Context, snap routingSnapshot, role string, binding json.RawMessage) error {
	profile := snap.Profiles[snap.DefaultProfile]
	if profile == nil {
		profile = map[string]json.RawMessage{}
	}
	if binding == nil {
		delete(profile, role)
	} else {
		profile[role] = binding
	}
	snap.Profiles[snap.DefaultProfile] = profile
	_, err := f.controlCall(ctx, "config/set_routing", map[string]any{
		"profiles": snap.Profiles, "defaultProfile": snap.DefaultProfile,
	}, false)
	return err
}

// applyReroute rebinds a role after its watch fired, records how to undo
// it, and tells the fleet. It runs at most once per watch: the rule is
// dropped first, so a repeated event cannot reroute twice.
func (f *Fleet) applyReroute(watchID, watchName string, rule RerouteRule) {
	if err := f.ws.DeleteWatchRule(watchID); err != nil {
		slog.Default().Warn("webbridge: drop watch rule failed", "watch", watchID, "err", err)
	}
	if err := f.rerouteRole(watchID, watchName, rule); err != nil {
		slog.Default().Warn("webbridge: reroute failed", "watch", watchName, "err", err)
		// The rule was spent but nothing changed: put it back so the watch
		// can still act the next time it fires, and leave a record.
		if perr := f.ws.PutWatchRule(watchID, WatchRule{Reroute: &rule}); perr != nil {
			slog.Default().Warn("webbridge: restore watch rule failed", "watch", watchID, "err", perr)
		}
		f.auditf(AuditEvent{Event: AuditRerouteFailed, OwnerID: DefaultOwnerID,
			Detail: sectionRouting + ":" + rule.Role, Reason: "watch:" + watchName})
	}
}

func (f *Fleet) rerouteRole(watchID, watchName string, rule RerouteRule) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.routingMu.Lock()
	defer f.routingMu.Unlock()
	snap, err := f.routingSnapshot(ctx)
	if err != nil {
		return err
	}
	if snap.DefaultProfile == "" || snap.Profiles[snap.DefaultProfile] == nil {
		return errors.New("no active routing profile")
	}
	from := snap.Profiles[snap.DefaultProfile][rule.Role]
	to, _ := json.Marshal(map[string]string{"preset": rule.Preset})
	if err := f.setRoleBinding(ctx, snap, rule.Role, to); err != nil {
		return err
	}
	rr := &Reroute{
		ID: newAgentID(), WatchID: watchID, Watch: watchName, Role: rule.Role,
		Profile: snap.DefaultProfile, From: from, To: to, At: f.now(),
	}
	f.reroutes.put(rr)
	f.auditf(AuditEvent{Event: AuditModelsChanged, OwnerID: DefaultOwnerID,
		Detail: sectionRouting + ":" + rule.Role, Reason: "watch:" + watchName})
	f.emitReroute(rr)
	return nil
}

// rerouteDelta is a "reroute" fleet delta. Its fields are flat, as the spec
// lists them, with from and to as display names rather than raw bindings.
type rerouteDelta struct {
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	ID        string `json:"id"`
	WatchID   string `json:"watchId"`
	Watch     string `json:"watch"`
	Role      string `json:"role"`
	From      string `json:"from"`
	To        string `json:"to"`
	At        int64  `json:"at"`
}

func (f *Fleet) emitReroute(rr *Reroute) {
	_, _ = f.fleetLog.Append(fleetStreamKey, rerouteDelta{
		Kind: "reroute", SessionID: studioOwner, AgentID: studioOwner,
		ID: rr.ID, WatchID: rr.WatchID, Watch: rr.Watch, Role: rr.Role,
		From: bindingLabel(rr.From), To: bindingLabel(rr.To), At: rr.At.UnixMilli(),
	})
}

// bindingLabel names a role binding for display: its preset, else its
// custom agent, else empty (an unbound role).
func bindingLabel(raw json.RawMessage) string {
	var b struct {
		Preset      string `json:"preset"`
		CustomAgent string `json:"customAgent"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	if b.Preset != "" {
		return b.Preset
	}
	return b.CustomAgent
}

// UndoReroute restores a role's earlier binding. It refuses when the
// binding is no longer the one the reroute set, so an undo never
// overwrites an edit made since.
func (f *Fleet) UndoReroute(ctx context.Context, id string) (Reroute, error) {
	rr, err := f.reroutes.claimUndo(id)
	if err != nil {
		return Reroute{}, err
	}
	f.routingMu.Lock()
	defer f.routingMu.Unlock()
	snap, err := f.routingSnapshot(ctx)
	if err != nil {
		f.reroutes.releaseUndo(id)
		return Reroute{}, err
	}
	if snap.DefaultProfile != rr.Profile || !sameJSON(snap.Profiles[rr.Profile][rr.Role], rr.To) {
		f.reroutes.releaseUndo(id)
		return Reroute{}, fmt.Errorf("%w: the %s binding changed since", errRerouteConflict, rr.Role)
	}
	if err := f.setRoleBinding(ctx, snap, rr.Role, rr.From); err != nil {
		f.reroutes.releaseUndo(id)
		return Reroute{}, err
	}
	f.auditf(AuditEvent{Event: AuditModelsChanged, OwnerID: DefaultOwnerID,
		Detail: sectionRouting + ":" + rr.Role, Reason: "undo-watch:" + rr.Watch})
	return rr, nil
}

// sameJSON compares two JSON values by content, not spelling.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// watchSource is one place watches live: an agent's session or the
// control session.
type watchSource struct {
	owner string // agent id, or studioOwner
	list  func(ctx context.Context) (json.RawMessage, error)
}

// hasCap reports whether the agent advertised a capability at initialize.
func (rt *agentRuntime) hasCap(name string) bool { return rt.caps[name] }

// watchSources lists every source GET /api/watches should ask: the control
// session, and each running agent that advertises watchAccess.
func (f *Fleet) watchSources() []watchSource {
	srcs := []watchSource{{owner: studioOwner, list: func(ctx context.Context) (json.RawMessage, error) {
		return f.controlCall(ctx, "session/watch_list", nil, true)
	}}}
	f.mu.Lock()
	rts := make([]*agentRuntime, 0, len(f.runtimes))
	for _, rt := range f.runtimes {
		if rt.spawnErr == nil && rt.hasCap("watchAccess") && rt.sessionID != "" {
			rts = append(rts, rt)
		}
	}
	f.mu.Unlock()
	sort.Slice(rts, func(i, j int) bool { return rts[i].id < rts[j].id })
	for _, rt := range rts {
		rt := rt
		srcs = append(srcs, watchSource{owner: rt.id, list: func(ctx context.Context) (json.RawMessage, error) {
			return rt.reg.call(ctx, f.sessionIDFor(rt), "session/watch_list", "watch_list", nil)
		}})
	}
	return srcs
}

// ListWatches merges every source's watches, each tagged with its owner.
// Sources are asked in parallel under a short timeout; one that fails or
// does not support watches contributes nothing rather than failing the
// page.
func (f *Fleet) ListWatches(ctx context.Context) []json.RawMessage {
	srcs := f.watchSources()
	results := make([][]json.RawMessage, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, watchListTimeout)
			defer cancel()
			raw, err := src.list(cctx)
			if err != nil {
				var un ErrUnsupported
				if !errors.As(err, &un) {
					slog.Default().Debug("webbridge: watch list failed", "owner", src.owner, "err", err)
				}
				return
			}
			var res struct {
				Watches []map[string]json.RawMessage `json:"watches"`
			}
			if json.Unmarshal(raw, &res) != nil {
				return
			}
			owner, _ := json.Marshal(src.owner)
			for _, w := range res.Watches {
				w["agentId"] = owner
				if b, err := json.Marshal(w); err == nil {
					results[i] = append(results[i], b)
				}
			}
		}()
	}
	wg.Wait()
	out := make([]json.RawMessage, 0)
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

func (s *Server) listWatches(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.fleet.ListWatches(r.Context()))
}

// startWatch starts a watch on an agent's session, or on the control
// session when no agent is named. A reroute rule is held by the bridge and
// is allowed only on Studio watches: it rewrites user-global config, which
// no single agent's watch should be able to do.
func (s *Server) startWatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID string          `json:"agentId"`
		Spec    json.RawMessage `json:"spec"`
		OnTrip  *struct {
			Reroute *RerouteRule `json:"reroute"`
		} `json:"onTrip"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Spec) == 0 || string(body.Spec) == "null" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "spec is required"})
		return
	}
	var rule *RerouteRule
	if body.OnTrip != nil && body.OnTrip.Reroute != nil {
		rule = body.OnTrip.Reroute
		if body.AgentID != "" && body.AgentID != studioOwner {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "onTrip.reroute is allowed only on Studio watches"})
			return
		}
		if rule.Role == "" || rule.Preset == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "onTrip.reroute needs a role and a preset"})
			return
		}
		if err := s.fleet.validateReroute(r.Context(), *rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	owner := body.AgentID
	var raw json.RawMessage
	var err error
	if owner == "" || owner == studioOwner {
		owner = studioOwner
		raw, err = s.fleet.controlCall(r.Context(), "session/watch_start", map[string]any{"spec": body.Spec}, true)
	} else {
		var rt *agentRuntime
		if rt, err = s.fleet.runtimeForAgent(owner); err == nil {
			raw, err = rt.reg.call(r.Context(), s.fleet.sessionIDFor(rt), "session/watch_start", "watch_start",
				map[string]any{"spec": body.Spec})
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(raw, &res) != nil || res == nil {
		res = map[string]json.RawMessage{}
	}
	var id, name string
	_ = json.Unmarshal(res["id"], &id)
	_ = json.Unmarshal(res["name"], &name)
	if rule != nil && id != "" {
		if err := s.fleet.ws.PutWatchRule(id, WatchRule{Reroute: rule}); err != nil {
			writeErr(w, err)
			return
		}
	}
	s.fleet.auditf(AuditEvent{Event: AuditWatchStarted, OwnerID: DefaultOwnerID,
		AgentID: agentIfOwner(owner), Detail: owner + ":" + id})
	ownerJSON, _ := json.Marshal(owner)
	res["agentId"] = ownerJSON
	writeJSON(w, http.StatusOK, res)
}

// agentIfOwner is the audit AgentID for a watch owner: empty for Studio.
func agentIfOwner(owner string) string {
	if owner == studioOwner {
		return ""
	}
	return owner
}

// validateReroute checks a rule against the stored config so a typo
// fails at creation rather than silently never rerouting. A config the
// bridge cannot read does not block creation.
func (f *Fleet) validateReroute(ctx context.Context, rule RerouteRule) error {
	snap, err := f.routingSnapshot(ctx)
	if err != nil {
		return nil
	}
	if _, ok := snap.Presets[rule.Preset]; !ok {
		return fmt.Errorf("unknown preset %q", rule.Preset)
	}
	if len(snap.Roles) > 0 {
		known := false
		for _, r := range snap.Roles {
			known = known || r == rule.Role
		}
		if !known {
			return fmt.Errorf("unknown role %q", rule.Role)
		}
	}
	return nil
}

func (s *Server) stopWatch(w http.ResponseWriter, r *http.Request) {
	owner, id := r.PathValue("owner"), r.PathValue("id")
	var err error
	if owner == studioOwner {
		_, err = s.fleet.controlCall(r.Context(), "session/watch_stop", map[string]any{"id": id}, true)
	} else {
		var rt *agentRuntime
		if rt, err = s.fleet.runtimeForAgent(owner); err == nil {
			_, err = rt.reg.call(r.Context(), s.fleet.sessionIDFor(rt), "session/watch_stop", "watch_stop",
				map[string]any{"id": id})
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if owner == studioOwner {
		_ = s.fleet.ws.DeleteWatchRule(id)
	}
	s.fleet.auditf(AuditEvent{Event: AuditWatchStopped, OwnerID: DefaultOwnerID,
		AgentID: agentIfOwner(owner), Detail: owner + ":" + id})
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) undoReroute(w http.ResponseWriter, r *http.Request) {
	rr, err := s.fleet.UndoReroute(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rr)
}

func (s *Server) watchRoutes() {
	s.mux.HandleFunc("GET /api/watches", s.listWatches)
	s.mux.HandleFunc("POST /api/watches", s.startWatch)
	s.mux.HandleFunc("DELETE /api/watches/{owner}/{id}", s.stopWatch)
	s.mux.HandleFunc("POST /api/reroutes/{id}/undo", s.undoReroute)
}
