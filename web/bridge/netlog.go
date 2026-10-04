package bridge

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	netLogKeepDays    = 30
	netLogRebuildDays = 7
	netLogMaxRequests = 500
)

// NetAgg is the running total for one (scope, host) pair, where scope is
// a workspace or an agent.
type NetAgg struct {
	Workspace string `json:"workspace,omitempty"`
	Agent     string `json:"agentId,omitempty"`
	Host      string `json:"host"`
	Requests  int64  `json:"requests"`
	Blocked   int64  `json:"blocked"`
	BytesUp   int64  `json:"bytesUp"`
	BytesDown int64  `json:"bytesDown"`
	LastSeen  int64  `json:"lastSeen"` // Unix ms
	// Decision is the most recent decision: "allow" or "block".
	Decision string `json:"decision"`
	// Injected is true once any request to the host had a credential
	// injected.
	Injected bool `json:"injected,omitempty"`
}

func (a *NetAgg) add(r EgressRecord) {
	a.Requests++
	if r.Decision == "block" {
		a.Blocked++
	}
	a.BytesUp += r.BytesUp
	a.BytesDown += r.BytesDown
	if r.At >= a.LastSeen {
		a.LastSeen = r.At
		a.Decision = r.Decision
	}
	a.Injected = a.Injected || r.Injected
}

type netKey struct{ scope, host string }

// NetLog stores connection records as daily JSONL files and keeps
// aggregates for the last seven days in memory.
type NetLog struct {
	dir string
	now func() time.Time

	mu     sync.Mutex
	byWS   map[netKey]*NetAgg
	byAgnt map[netKey]*NetAgg
}

// NewNetLog opens <stateDir>/network, deletes files older than 30 days,
// and rebuilds the aggregates from the last 7 days of files.
func NewNetLog(stateDir string) *NetLog {
	n := &NetLog{
		dir: filepath.Join(stateDir, "network"), now: time.Now,
		byWS: map[netKey]*NetAgg{}, byAgnt: map[netKey]*NetAgg{},
	}
	n.cleanup()
	n.rebuild()
	return n
}

func dayName(t time.Time) string { return t.UTC().Format("2006-01-02") + ".jsonl" }

func parseDayName(name string) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", base)
	return d, err == nil
}

// cleanup removes files older than the retention window.
func (n *NetLog) cleanup() {
	entries, err := os.ReadDir(n.dir)
	if err != nil {
		return
	}
	cutoff := n.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -netLogKeepDays)
	for _, e := range entries {
		if d, ok := parseDayName(e.Name()); ok && d.Before(cutoff) {
			_ = os.Remove(filepath.Join(n.dir, e.Name()))
		}
	}
}

func (n *NetLog) rebuild() {
	entries, err := os.ReadDir(n.dir)
	if err != nil {
		return
	}
	cutoff := n.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(netLogRebuildDays - 1))
	for _, e := range entries {
		d, ok := parseDayName(e.Name())
		if !ok || d.Before(cutoff) {
			continue
		}
		forEachRecord(filepath.Join(n.dir, e.Name()), func(r EgressRecord) { n.aggregate(r) })
	}
}

// forEachRecord streams the records of one file, skipping bad lines.
func forEachRecord(path string, fn func(EgressRecord)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r EgressRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Host != "" {
			fn(r)
		}
	}
}

func (n *NetLog) aggregate(r EgressRecord) {
	r.Host = normalizeHost(r.Host)
	n.mu.Lock()
	defer n.mu.Unlock()
	if r.Workspace != "" {
		k := netKey{r.Workspace, r.Host}
		a := n.byWS[k]
		if a == nil {
			a = &NetAgg{Workspace: r.Workspace, Host: r.Host}
			n.byWS[k] = a
		}
		a.add(r)
	}
	if r.AgentID != "" {
		k := netKey{r.AgentID, r.Host}
		a := n.byAgnt[k]
		if a == nil {
			a = &NetAgg{Workspace: r.Workspace, Agent: r.AgentID, Host: r.Host}
			n.byAgnt[k] = a
		}
		a.add(r)
	}
}

// Append writes records to the day file of each record's timestamp and
// updates the aggregates.
func (n *NetLog) Append(records []EgressRecord) error {
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		return err
	}
	byFile := map[string][]byte{}
	for i := range records {
		r := &records[i]
		if r.At == 0 {
			r.At = n.now().UnixMilli()
		}
		r.Host = normalizeHost(r.Host)
		line, err := json.Marshal(r)
		if err != nil {
			continue
		}
		name := dayName(time.UnixMilli(r.At))
		byFile[name] = append(append(byFile[name], line...), '\n')
	}
	var firstErr error
	for name, data := range byFile {
		f, err := os.OpenFile(filepath.Join(n.dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.Write(data)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, r := range records {
		n.aggregate(r)
	}
	return firstErr
}

// Hosts returns aggregate rows: for one agent, for one workspace, or
// (both empty) per workspace and host across the fleet. Newest first.
func (n *NetLog) Hosts(workspace, agent string) []NetAgg {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []NetAgg
	switch {
	case agent != "":
		for _, a := range n.byAgnt {
			if a.Agent == agent {
				out = append(out, *a)
			}
		}
	default:
		for _, a := range n.byWS {
			if workspace == "" || a.Workspace == workspace {
				out = append(out, *a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// AgentTotals is one agent's totals across hosts.
type AgentTotals struct {
	Agent     string `json:"agentId"`
	Workspace string `json:"workspace,omitempty"`
	Hosts     int    `json:"hosts"`
	Requests  int64  `json:"requests"`
	Blocked   int64  `json:"blocked"`
	BytesUp   int64  `json:"bytesUp"`
	BytesDown int64  `json:"bytesDown"`
	LastSeen  int64  `json:"lastSeen"`
}

// Agents returns per-agent totals, optionally within one workspace.
func (n *NetLog) Agents(workspace string) []AgentTotals {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := map[string]*AgentTotals{}
	for _, a := range n.byAgnt {
		if workspace != "" && a.Workspace != workspace {
			continue
		}
		t := m[a.Agent]
		if t == nil {
			t = &AgentTotals{Agent: a.Agent, Workspace: a.Workspace}
			m[a.Agent] = t
		}
		t.Hosts++
		t.Requests += a.Requests
		t.Blocked += a.Blocked
		t.BytesUp += a.BytesUp
		t.BytesDown += a.BytesDown
		if a.LastSeen > t.LastSeen {
			t.LastSeen = a.LastSeen
		}
	}
	out := make([]AgentTotals, 0, len(m))
	for _, t := range m {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Agent < out[j].Agent
	})
	return out
}

// Requests returns the most recent records from today's file that match
// the filters, newest first, capped at 500.
func (n *NetLog) Requests(workspace, agent string) []EgressRecord {
	var all []EgressRecord
	forEachRecord(filepath.Join(n.dir, dayName(n.now())), func(r EgressRecord) {
		if (workspace == "" || r.Workspace == workspace) && (agent == "" || r.AgentID == agent) {
			all = append(all, r)
		}
	})
	if len(all) > netLogMaxRequests {
		all = all[len(all)-netLogMaxRequests:]
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if all == nil {
		all = []EgressRecord{}
	}
	return all
}
