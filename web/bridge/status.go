package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	statusDefaultTTL = 168 * time.Hour
	statusMaxTTL     = 720 * time.Hour
	statusPrefix     = "/s/"
	// statusRateBurst and statusRateRefill bound /s/ requests per IP: a
	// bucket of 60 refilling one a second.
	statusRateBurst   = 60
	statusPruneEvery  = 10 * time.Minute
	statusHeadlineMax = 200
)

//go:embed status_page.html
var statusPageHTML string

var statusPageTmpl = template.Must(template.New("status").Parse(statusPageHTML))

// StatusLink is a revocable public link to one agent's progress. Only the
// hash of the token is stored.
type StatusLink struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agentId"`
	TokenHash string    `json:"tokenHash"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
}

// StatusLinks returns every link, sorted by id.
func (w *Workspace) StatusLinks() []StatusLink {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]StatusLink, 0, len(w.statusLinks))
	for _, l := range w.statusLinks {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PutStatusLink stores or replaces a link.
func (w *Workspace) PutStatusLink(l StatusLink) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statusLinks[l.ID] = l
	return w.save()
}

// StatusLinkByID returns one link.
func (w *Workspace) StatusLinkByID(id string) (StatusLink, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, ok := w.statusLinks[id]
	return l, ok
}

// statusLimiter is a per-IP token bucket.
type statusLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*statusBucket
	lastPrune time.Time
}

type statusBucket struct {
	tokens float64
	at     time.Time
}

func (l *statusLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*statusBucket{}
	}
	if now.Sub(l.lastPrune) >= statusPruneEvery {
		for k, b := range l.buckets {
			if now.Sub(b.at) >= statusPruneEvery {
				delete(l.buckets, k)
			}
		}
		l.lastPrune = now
	}
	b := l.buckets[ip]
	if b == nil {
		b = &statusBucket{tokens: statusRateBurst, at: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.at).Seconds() // one token a second
	if b.tokens > statusRateBurst {
		b.tokens = statusRateBurst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func newStatusToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("bridge: generate status token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Server) statusLinkCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID  string `json:"agentId"`
		TTLHours int    `json:"ttlHours"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, ok := s.fleet.ws.Agent(body.AgentID); !ok {
		writeErr(w, fmt.Errorf("%w: agent %s", ErrUnknownAgent, body.AgentID))
		return
	}
	ttl := statusDefaultTTL
	if body.TTLHours != 0 {
		ttl = time.Duration(body.TTLHours) * time.Hour
	}
	if ttl <= 0 || ttl > statusMaxTTL {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ttlHours must be between 1 and 720"})
		return
	}
	token, err := newStatusToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	now := s.fleet.now()
	link := StatusLink{ID: newAgentID(), AgentID: body.AgentID, TokenHash: HashToken(token),
		CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.fleet.ws.PutStatusLink(link); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditStatusLinkCreated, OwnerID: DefaultOwnerID, AgentID: body.AgentID, Detail: link.ID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": link.ID, "url": statusPrefix + token, "expiresAt": link.ExpiresAt})
}

// statusLinkView is a link as listed: never the token or its hash.
type statusLinkView struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agentId"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

func (s *Server) statusLinkList(w http.ResponseWriter, r *http.Request) {
	out := []statusLinkView{}
	for _, l := range s.fleet.ws.StatusLinks() {
		v := statusLinkView{ID: l.ID, AgentID: l.AgentID, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt}
		if !l.RevokedAt.IsZero() {
			t := l.RevokedAt
			v.RevokedAt = &t
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) statusLinkRevoke(w http.ResponseWriter, r *http.Request) {
	l, ok := s.fleet.ws.StatusLinkByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown status link"})
		return
	}
	if l.RevokedAt.IsZero() {
		l.RevokedAt = s.fleet.now()
		if err := s.fleet.ws.PutStatusLink(l); err != nil {
			writeErr(w, err)
			return
		}
		s.fleet.auditf(AuditEvent{Event: AuditStatusLinkRevoked, OwnerID: DefaultOwnerID, AgentID: l.AgentID, Detail: l.ID})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (s *Server) statusLinkRoutes() {
	s.mux.HandleFunc("POST /api/status-links", s.statusLinkCreate)
	s.mux.HandleFunc("GET /api/status-links", s.statusLinkList)
	s.mux.HandleFunc("DELETE /api/status-links/{id}", s.statusLinkRevoke)
}

// lookupStatusLink finds the live link a presented token belongs to. Every
// stored hash is compared in constant time, so neither timing nor position
// shows whether, or which, link matched.
func (f *Fleet) lookupStatusLink(token string, now time.Time) (StatusLink, bool) {
	want := []byte(HashToken(token))
	var found StatusLink
	matched := false
	for _, l := range f.ws.StatusLinks() {
		if subtle.ConstantTimeCompare([]byte(l.TokenHash), want) == 1 {
			found, matched = l, true
		}
	}
	if !matched || !found.RevokedAt.IsZero() || !now.Before(found.ExpiresAt) {
		return StatusLink{}, false
	}
	return found, true
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// statusPublic serves /s/<token> and /s/<token>/data. It is outside /api
// and takes no bearer token: the link is the credential.
func (s *Server) statusPublic(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	notFound := func() { http.Error(w, "not found", http.StatusNotFound) }
	if s.fleet == nil || r.Method != http.MethodGet {
		notFound()
		return
	}
	now := s.fleet.now()
	if !s.fleet.statusRate.allow(remoteIP(r), now) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, statusPrefix)
	token, sub, _ := strings.Cut(rest, "/")
	if token == "" || (sub != "" && sub != "data") {
		notFound()
		return
	}
	link, ok := s.fleet.lookupStatusLink(token, now)
	if !ok {
		notFound()
		return
	}
	a, ok := s.fleet.ws.Agent(link.AgentID)
	if !ok {
		notFound()
		return
	}
	if sub == "data" {
		writeJSON(w, http.StatusOK, s.fleet.statusSnapshot(r.Context(), a, now))
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'")
	_ = statusPageTmpl.Execute(w, struct{ Name string }{statusName(a)})
}

func statusName(a Agent) string {
	if a.Name != "" {
		return a.Name
	}
	return "Agent"
}

// statusData is the whole of what a status link reveals. Adding a field
// here is a decision to publish it: the test pins the key set.
type statusData struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	Status    string `json:"status"`
	ElapsedMs int64  `json:"elapsedMs"`
	Progress  struct {
		Done  int `json:"done"`
		Total int `json:"total"`
	} `json:"progress"`
	Tasks    []statusTask `json:"tasks"`
	Headline string       `json:"headline"`
}

type statusTask struct {
	Index   int    `json:"index"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// statusSnapshot builds the whitelisted view of an agent from its stack
// and its run. Nothing is copied wholesale: each field is picked.
func (f *Fleet) statusSnapshot(ctx context.Context, a Agent, now time.Time) statusData {
	d := statusData{Name: statusName(a), Project: filepath.Base(a.Project), Status: "idle", Tasks: []statusTask{}}
	if d.Project == "." || d.Project == string(filepath.Separator) {
		d.Project = ""
	}
	d.ElapsedMs = now.Sub(a.CreatedAt).Milliseconds()
	if d.ElapsedMs < 0 {
		d.ElapsedMs = 0
	}
	rt, err := f.runtimeForAgent(a.ID)
	if err != nil {
		return d
	}
	sid := f.sessionIDFor(rt)
	switch rt.reg.Pending(sid) {
	case "approval", "question":
		d.Status = "waiting"
	default:
		if info, ok := rt.reg.lookup(sid); ok && info.Busy {
			d.Status = "working"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if raw, err := rt.reg.Stack(ctx, sid, 0); err == nil {
		var tree struct {
			Nodes []struct {
				Kind string `json:"kind"`
				Live bool   `json:"live"`
				Step *struct {
					Headline string `json:"headline"`
				} `json:"step"`
				Task *struct {
					Content string `json:"content"`
					Status  string `json:"status"`
					Index   int    `json:"index"`
				} `json:"task"`
			} `json:"nodes"`
		}
		if json.Unmarshal(raw, &tree) == nil {
			var live, last string
			for _, n := range tree.Nodes {
				switch {
				case n.Kind == "step" && n.Step != nil:
					last = n.Step.Headline
					if n.Live {
						live = n.Step.Headline
					}
				case n.Kind == "task" && n.Task != nil:
					st := "pending"
					switch n.Task.Status {
					case "completed":
						st = "done"
						d.Progress.Done++
					case "in_progress":
						st = "active"
					}
					d.Tasks = append(d.Tasks, statusTask{Index: n.Task.Index, Content: n.Task.Content, Status: st})
				}
			}
			d.Progress.Total = len(d.Tasks)
			d.Headline = live
			if d.Headline == "" {
				d.Headline = last
			}
			if r := []rune(d.Headline); len(r) > statusHeadlineMax {
				d.Headline = string(r[:statusHeadlineMax]) + "…"
			}
		}
	}
	// A plan run's own task list is the better progress source.
	if raw, err := rt.reg.Run(ctx, sid); err == nil {
		var run struct {
			Kind string `json:"kind"`
			SDD  *struct {
				Total     int  `json:"totalTasks"`
				Done      int  `json:"doneTasks"`
				Finished  bool `json:"finished"`
				Succeeded bool `json:"succeeded"`
				Tasks     []struct {
					N      int    `json:"n"`
					Title  string `json:"title"`
					Status string `json:"status"`
				} `json:"tasks"`
			} `json:"sdd"`
		}
		if json.Unmarshal(raw, &run) == nil && run.Kind == "sdd" && run.SDD != nil && run.SDD.Total > 0 {
			d.Progress.Done, d.Progress.Total = run.SDD.Done, run.SDD.Total
			d.Tasks = d.Tasks[:0]
			for _, t := range run.SDD.Tasks {
				d.Tasks = append(d.Tasks, statusTask{Index: t.N, Content: t.Title, Status: t.Status})
			}
			if run.SDD.Finished {
				d.Status = "failed"
				if run.SDD.Succeeded {
					d.Status = "finished"
				}
			}
		}
	}
	return d
}
