package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// usageSeenCap bounds the dedup set. A telemetry row is resent only when
// an agent restarts, so a short memory is enough.
const usageSeenCap = 10000

// UsageRow is one turn's spend: the engine's telemetry row plus the agent
// it came from.
type UsageRow struct {
	ID               int64   `json:"id"`
	StartedAt        int64   `json:"startedAt"` // Unix milliseconds
	DurationMs       int64   `json:"durationMs"`
	Role             string  `json:"role"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	ReasoningTokens  int     `json:"reasoningTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CostUSD          float64 `json:"costUsd"`
	// CostMicroUSD is the cost in millionths of a dollar. CostUSD is whole
	// cents, so a cheap turn reads 0 there; sum this field instead.
	CostMicroUSD int64  `json:"costMicroUsd"`
	AgentID      string `json:"agentId"`
	Project      string `json:"project"`
	Origin       string `json:"origin"`
}

// micro is the row's cost in millionths of a dollar. An older agent sends
// only CostUSD, which converts at its own (whole-cent) precision.
func (r UsageRow) micro() int64 {
	if r.CostMicroUSD != 0 {
		return r.CostMicroUSD
	}
	return usdToMicro(r.CostUSD)
}

func usdToMicro(usd float64) int64 { return int64(math.Round(usd * 1e6)) }
func microToUSD(m int64) float64   { return float64(m) / 1e6 }

func (r UsageRow) key() string { return fmt.Sprintf("%s:%d", r.AgentID, r.ID) }

func (r UsageRow) started() time.Time { return time.UnixMilli(r.StartedAt).UTC() }

// UsageLog is an append-only ledger of usage rows, one JSONL file per
// month, modelled on AuditLog.
type UsageLog struct {
	dir string
	mu  sync.Mutex
	// seen and order dedupe rows by agent and id, oldest evicted first.
	seen   map[string]struct{}
	order  []string
	warmed bool
}

func NewUsageLog(stateDir string) *UsageLog {
	return &UsageLog{dir: filepath.Join(stateDir, "usage"), seen: make(map[string]struct{})}
}

func usageFileName(t time.Time) string { return t.UTC().Format("2006-01") + ".jsonl" }

func (u *UsageLog) remember(key string) {
	u.seen[key] = struct{}{}
	u.order = append(u.order, key)
	if len(u.order) > usageSeenCap {
		delete(u.seen, u.order[0])
		u.order = u.order[1:]
	}
}

// warm seeds the dedup set from the last two months' files, so a bridge
// restart followed by an agent resending its backlog does not double
// count. Called with u.mu held, once.
func (u *UsageLog) warm(now time.Time) {
	if u.warmed {
		return
	}
	u.warmed = true
	var rows []UsageRow
	for _, month := range []time.Time{now.AddDate(0, -1, 0), now} {
		rows = append(rows, u.readFile(filepath.Join(u.dir, usageFileName(month)))...)
	}
	if len(rows) > usageSeenCap {
		rows = rows[len(rows)-usageSeenCap:]
	}
	for _, r := range rows {
		u.remember(r.key())
	}
}

// Append writes rows it has not seen before and returns those rows. Each
// row lands in the file for the month of its own start time.
func (u *UsageLog) Append(rows []UsageRow) ([]UsageRow, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.warm(time.Now().UTC())
	var fresh []UsageRow
	for _, r := range rows {
		if _, dup := u.seen[r.key()]; dup {
			continue
		}
		fresh = append(fresh, r)
	}
	if len(fresh) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return nil, fmt.Errorf("bridge: create usage dir: %w", err)
	}
	for _, r := range fresh {
		line, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("bridge: encode usage row: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(u.dir, usageFileName(r.started())),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("bridge: open usage ledger: %w", err)
		}
		_, werr := f.Write(append(line, '\n'))
		cerr := f.Close()
		if werr != nil {
			return nil, werr
		}
		if cerr != nil {
			return nil, cerr
		}
		u.remember(r.key())
	}
	return fresh, nil
}

// Range returns the rows that started in [from, to), reading only the
// months the window touches.
func (u *UsageLog) Range(from, to time.Time) ([]UsageRow, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []UsageRow
	first := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for m := first; m.Before(to); m = m.AddDate(0, 1, 0) {
		for _, r := range u.readFile(filepath.Join(u.dir, usageFileName(m))) {
			if t := r.started(); !t.Before(from) && t.Before(to) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// readFile decodes one ledger file. A missing file is empty, and a torn
// line is skipped so one bad write cannot hide the rest.
func (u *UsageLog) readFile(path string) []UsageRow {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []UsageRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r UsageRow
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// recordUsage ingests the usage section of a session_telemetry update for
// an agent, writes the new rows to the ledger, and runs the budget check
// on each.
func (f *Fleet) recordUsage(agentID string, raw json.RawMessage) {
	if f.usage == nil || len(raw) == 0 {
		return
	}
	var rows []UsageRow
	if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		return
	}
	// Load the caps first so the ledger seed cannot include these rows.
	f.budgets.ensureLoaded()
	var project, origin string
	if a, ok := f.ws.Agent(agentID); ok {
		project, origin = a.Project, a.Origin
	}
	for i := range rows {
		rows[i].AgentID, rows[i].Project, rows[i].Origin = agentID, project, origin
	}
	fresh, err := f.usage.Append(rows)
	if err != nil {
		slog.Default().Warn("webbridge: usage append failed", "agent", agentID, "err", err)
		return
	}
	for _, r := range fresh {
		f.checkBudgets(r)
	}
}

// usageRange parses ?range=: 7d or 30d.
func usageRange(s string) (time.Duration, string, bool) {
	switch s {
	case "", "7d":
		return 7 * 24 * time.Hour, "7d", true
	case "30d":
		return 30 * 24 * time.Hour, "30d", true
	}
	return 0, "", false
}

type usageTotals struct {
	CostUSD          float64 `json:"costUsd"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	AgentHours       float64 `json:"agentHours"`
	PRsShipped       int     `json:"prsShipped"`
}

type usagePoint struct {
	Key     string  `json:"key"`
	CostUSD float64 `json:"costUsd"`
	Tokens  int     `json:"tokens"`
}

type usageReport struct {
	Range  string       `json:"range"`
	Totals usageTotals  `json:"totals"`
	Series []usagePoint `json:"series"`
}

// usageReport aggregates the ledger over the trailing window ending at
// now, grouped by day, project, role or model.
func (f *Fleet) usageReport(window time.Duration, label, by string, now time.Time) (usageReport, error) {
	from := now.Add(-window)
	rows, err := f.usage.Range(from, now.Add(time.Nanosecond))
	if err != nil {
		return usageReport{}, err
	}
	rep := usageReport{Range: label, Series: []usagePoint{}}
	groups := map[string]*usagePoint{}
	var durationMs, totalMicro int64
	micros := map[string]int64{}
	for _, r := range rows {
		totalMicro += r.micro()
		rep.Totals.PromptTokens += r.PromptTokens
		rep.Totals.CompletionTokens += r.CompletionTokens
		durationMs += r.DurationMs
		key := usageKey(r, by)
		g := groups[key]
		if g == nil {
			g = &usagePoint{Key: key}
			groups[key] = g
		}
		micros[key] += r.micro()
		g.Tokens += r.PromptTokens + r.CompletionTokens
	}
	rep.Totals.CostUSD = microToUSD(totalMicro)
	rep.Totals.AgentHours = float64(durationMs) / 3.6e6
	rep.Totals.PRsShipped = f.prsShipped(from, now)

	if by == "day" {
		// Every day in the window appears, so a chart has no gaps.
		for d := from.UTC().Truncate(24 * time.Hour); !d.After(now); d = d.AddDate(0, 0, 1) {
			key := d.Format("2006-01-02")
			if groups[key] == nil {
				groups[key] = &usagePoint{Key: key}
			}
		}
	}
	for key, g := range groups {
		g.CostUSD = microToUSD(micros[key])
		rep.Series = append(rep.Series, *g)
	}
	sort.Slice(rep.Series, func(i, j int) bool {
		if by == "day" {
			return rep.Series[i].Key < rep.Series[j].Key
		}
		if rep.Series[i].CostUSD != rep.Series[j].CostUSD {
			return rep.Series[i].CostUSD > rep.Series[j].CostUSD
		}
		return rep.Series[i].Key < rep.Series[j].Key
	})
	return rep, nil
}

func usageKey(r UsageRow, by string) string {
	var k string
	switch by {
	case "project":
		k = r.Project
	case "role":
		k = r.Role
	case "model":
		k = r.Model
	default:
		return r.started().Format("2006-01-02")
	}
	if k == "" {
		return "unknown"
	}
	return k
}

// prsShipped counts agents that pushed a pull request in [from, to].
//
// Two sources are merged by agent id. The audit log's push events give
// the time of each push, but their Detail names a branch, not a pull
// request URL, and Tail reads only the recent active file. The agent
// record carries the validated PRUrl and the last push time. A push counts
// when either shows a pull request.
func (f *Fleet) prsShipped(from, to time.Time) int {
	shipped := map[string]struct{}{}
	for _, a := range f.ws.Agents() {
		if a.PRUrl != "" && !a.PushedAt.IsZero() && !a.PushedAt.Before(from) && !a.PushedAt.After(to) {
			shipped[a.ID] = struct{}{}
		}
	}
	if f.audit != nil {
		events, _ := f.audit.Tail(maxAuditTail)
		for _, e := range events {
			if e.Event != AuditPush || e.TS.Before(from) || e.TS.After(to) {
				continue
			}
			if a, ok := f.ws.Agent(e.AgentID); ok {
				if a.PRUrl != "" {
					shipped[a.ID] = struct{}{}
				}
			} else if strings.Contains(e.Detail, "://") {
				// An agent since removed: its record is gone, but the
				// event itself named a pull request.
				shipped[e.AgentID+"|"+e.Detail] = struct{}{}
			}
		}
	}
	return len(shipped)
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) {
	window, label, ok := usageRange(r.URL.Query().Get("range"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "range must be 7d or 30d"})
		return
	}
	by := r.URL.Query().Get("by")
	switch by {
	case "":
		by = "day"
	case "day", "project", "role", "model":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "by must be day, project, role or model"})
		return
	}
	rep, err := s.fleet.usageReport(window, label, by, s.fleet.now())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
