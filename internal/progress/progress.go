// Package progress holds bounded display data. It never owns execution.
package progress

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"marshal/internal/redact"
)

const (
	Version         = 1
	Prefix          = "@@marshal-progress/v1 "
	MaxNumber       = float64(1<<53 - 1)
	MaxEventBytes   = 16 << 10
	MaxGoLineBytes  = 64 << 10
	MaxDisplayBytes = 8 << 10
	MaxCases        = 20000
	FreshFor        = 30 * time.Second
)

var ErrInvalid = errors.New("invalid progress data")
var ErrSequence = errors.New("stale progress sequence")

type Config struct {
	Profile string `json:"profile,omitempty"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
}
type Phase struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Index int    `json:"index,omitempty"`
	Total int    `json:"total,omitempty"`
}
type Metric struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Unit    string   `json:"unit"`
	Current float64  `json:"current"`
	Total   *float64 `json:"total,omitempty"`
	Rate    *float64 `json:"rate,omitempty"`
}
type Row struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	State string `json:"state"`
}

// Frame is the producer's complete snapshot. Runtime facts are excluded.
type Frame struct {
	Version         int      `json:"version"`
	Sequence        uint64   `json:"sequence"`
	Phase           *Phase   `json:"phase,omitempty"`
	PrimaryMetricID string   `json:"primaryMetricId,omitempty"`
	Metrics         []Metric `json:"metrics,omitempty"`
	Rows            []Row    `json:"rows,omitempty"`
	Detail          string   `json:"detail,omitempty"`
}
type Snapshot struct {
	Frame
	ObservedAt    time.Time `json:"observedAt"`
	MeasuredAt    time.Time `json:"measuredAt,omitempty"`
	Partial       bool      `json:"partial,omitempty"`
	Warning       string    `json:"warning,omitempty"`
	ReportedError bool      `json:"reportedError,omitempty"`
}
type Estimate struct {
	Fraction *float64
	Rate     *float64
	ETA      *time.Duration
	Stale    bool
}

// Clean is the common display/persistence boundary; callers keep raw identity.
func Clean(s string, limit int, multiline bool) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			if multiline {
				return r
			}
			return ' '
		}
		if r == '\r' {
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = redact.Secrets(s)
	if limit > 0 && utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}
func NormalizeConfig(c Config) (Config, error) {
	if c.Profile == "" {
		c.Profile = "generic"
	}
	if c.Source == "" {
		c.Source = "none"
	}
	switch c.Profile {
	case "generic", "tests", "download", "transfer", "custom":
	default:
		return Config{}, fmt.Errorf("%w: profile", ErrInvalid)
	}
	switch c.Source {
	case "none":
	case "events-v1":
		if c.Profile == "generic" {
			return Config{}, fmt.Errorf("%w: events profile", ErrInvalid)
		}
	case "go-test-json":
		if c.Profile != "tests" {
			return Config{}, fmt.Errorf("%w: test source/profile", ErrInvalid)
		}
	default:
		return Config{}, fmt.Errorf("%w: source", ErrInvalid)
	}
	if !utf8.ValidString(c.Title) || utf8.RuneCountInString(c.Title) > 80 {
		return Config{}, fmt.Errorf("%w: title", ErrInvalid)
	}
	c.Title = Clean(c.Title, 80, false)
	return c, nil
}
func Clone(s *Snapshot) *Snapshot {
	if s == nil {
		return nil
	}
	out := *s
	if s.Phase != nil {
		p := *s.Phase
		out.Phase = &p
	}
	out.Metrics = append([]Metric(nil), s.Metrics...)
	for i := range out.Metrics {
		if p := s.Metrics[i].Total; p != nil {
			v := *p
			out.Metrics[i].Total = &v
		}
		if p := s.Metrics[i].Rate; p != nil {
			v := *p
			out.Metrics[i].Rate = &v
		}
	}
	out.Rows = append([]Row(nil), s.Rows...)
	return &out
}
func WorkKey(conversation, epoch, kind, sourceID string) string {
	b, _ := json.Marshal([4]string{conversation, epoch, kind, sourceID})
	return "work:" + base64.RawURLEncoding.EncodeToString(b)
}
func ResultRevision(key, state, outcome string, exitCode *int, endedAt time.Time) string {
	b, _ := json.Marshal([]any{key, state, outcome, exitCode, endedAt.UTC().Format(time.RFC3339Nano)})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Stream identifies which child output stream a Write belongs to.
type Stream uint8

const (
	// Stdout carries ordinary output plus events-v1 control records.
	Stdout Stream = iota
	// Stderr carries ordinary text only.
	Stderr
)

const (
	maxMetricIDRunes = 64
	maxLabelRunes    = 80
	maxUnitRunes     = 16
	maxDetailBytes   = 1024
	maxMetrics       = 4
	maxRows          = 8
	maxETA           = 365 * 24 * time.Hour

	diagInvalid   = "invalid progress record"
	diagDenominat = "outdated denominator"
)

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// countUnit reports whether unit carries integer current/total values.
func countUnit(unit string) bool {
	switch unit {
	case "bytes", "files", "tests", "packages", "items":
		return true
	}
	return false
}

// DecodeEvent parses one complete events-v1 record. Only Frame fields are
// decoded; runtime identity and lifecycle are never read from the producer.
func DecodeEvent(line []byte, previous uint64, observed time.Time) (*Snapshot, error) {
	if !bytes.HasPrefix(line, []byte(Prefix)) {
		return nil, fmt.Errorf("%w: prefix", ErrInvalid)
	}
	payload := line[len(Prefix):]
	dec := json.NewDecoder(bytes.NewReader(payload))
	var f Frame
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: json", ErrInvalid)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	if f.Sequence < 1 || f.Sequence > uint64(MaxNumber) {
		return nil, fmt.Errorf("%w: sequence", ErrInvalid)
	}
	if f.Sequence <= previous {
		return nil, ErrSequence
	}
	if err := validateFrame(&f); err != nil {
		return nil, err
	}
	outdated := false
	for i := range f.Metrics {
		if t := f.Metrics[i].Total; t != nil && f.Metrics[i].Current > *t {
			f.Metrics[i].Total = nil
			outdated = true
		}
	}
	out := &Frame{
		Version:         f.Version,
		Sequence:        f.Sequence,
		PrimaryMetricID: Clean(f.PrimaryMetricID, maxMetricIDRunes, false),
		Detail:          cleanDetail(f.Detail),
	}
	if f.Phase != nil {
		out.Phase = &Phase{
			ID:    Clean(f.Phase.ID, maxMetricIDRunes, false),
			Label: Clean(f.Phase.Label, maxLabelRunes, false),
			Index: f.Phase.Index,
			Total: f.Phase.Total,
		}
	}
	if f.Metrics != nil {
		out.Metrics = make([]Metric, len(f.Metrics))
		for i, m := range f.Metrics {
			out.Metrics[i] = Metric{
				ID:      Clean(m.ID, maxMetricIDRunes, false),
				Label:   Clean(m.Label, maxLabelRunes, false),
				Unit:    Clean(m.Unit, maxUnitRunes, false),
				Current: m.Current,
			}
			if m.Total != nil {
				v := *m.Total
				out.Metrics[i].Total = &v
			}
			if m.Rate != nil {
				v := *m.Rate
				out.Metrics[i].Rate = &v
			}
		}
	}
	if f.Rows != nil {
		out.Rows = make([]Row, len(f.Rows))
		for i, r := range f.Rows {
			out.Rows[i] = Row{
				ID:    Clean(r.ID, maxMetricIDRunes, false),
				Label: Clean(r.Label, maxLabelRunes, false),
				State: r.State,
			}
		}
	}
	// Sanitization can collapse distinct producer IDs (redaction, escapes).
	// Reject a rendered snapshot that would carry colliding or empty identity.
	seen := make(map[string]bool, len(out.Metrics))
	for _, m := range out.Metrics {
		if m.ID == "" || m.Unit == "" || seen[m.ID] {
			return nil, fmt.Errorf("%w: metric identity", ErrInvalid)
		}
		seen[m.ID] = true
	}
	if out.PrimaryMetricID != "" && !seen[out.PrimaryMetricID] {
		return nil, fmt.Errorf("%w: primary metric", ErrInvalid)
	}
	rowIDs := make(map[string]bool, len(out.Rows))
	for _, r := range out.Rows {
		if r.ID == "" || rowIDs[r.ID] {
			return nil, fmt.Errorf("%w: row identity", ErrInvalid)
		}
		rowIDs[r.ID] = true
	}
	snap := &Snapshot{Frame: *out, ObservedAt: observed, MeasuredAt: observed}
	if outdated {
		snap.Warning = diagDenominat
	}
	for _, r := range snap.Rows {
		if r.State == "error" {
			snap.ReportedError = true
			break
		}
	}
	return snap, nil
}

// validateFrame enforces every events-v1 structural and numeric limit before
// any text is sanitized. Unknown JSON fields have already been dropped.
func validateFrame(f *Frame) error {
	if f.Version != Version {
		return fmt.Errorf("%w: version", ErrInvalid)
	}
	if len(f.Metrics) > maxMetrics {
		return fmt.Errorf("%w: metrics", ErrInvalid)
	}
	ids := make(map[string]bool, len(f.Metrics))
	for i := range f.Metrics {
		m := &f.Metrics[i]
		if m.ID == "" || utf8.RuneCountInString(m.ID) > maxMetricIDRunes {
			return fmt.Errorf("%w: metric id", ErrInvalid)
		}
		if ids[m.ID] {
			return fmt.Errorf("%w: duplicate metric", ErrInvalid)
		}
		ids[m.ID] = true
		if utf8.RuneCountInString(m.Label) > maxLabelRunes {
			return fmt.Errorf("%w: metric label", ErrInvalid)
		}
		if u := utf8.RuneCountInString(m.Unit); u == 0 || u > maxUnitRunes {
			return fmt.Errorf("%w: unit", ErrInvalid)
		}
		if !finite(m.Current) || m.Current < 0 || m.Current > MaxNumber {
			return fmt.Errorf("%w: current", ErrInvalid)
		}
		if countUnit(m.Unit) && m.Current != math.Trunc(m.Current) {
			return fmt.Errorf("%w: non-integer count", ErrInvalid)
		}
		if m.Total != nil {
			if !finite(*m.Total) || *m.Total <= 0 || *m.Total > MaxNumber {
				return fmt.Errorf("%w: total", ErrInvalid)
			}
			if countUnit(m.Unit) && *m.Total != math.Trunc(*m.Total) {
				return fmt.Errorf("%w: non-integer total", ErrInvalid)
			}
		}
		if m.Rate != nil {
			if !finite(*m.Rate) || *m.Rate < 0 || *m.Rate > MaxNumber {
				return fmt.Errorf("%w: rate", ErrInvalid)
			}
		}
	}
	if f.PrimaryMetricID != "" && !ids[f.PrimaryMetricID] {
		return fmt.Errorf("%w: primary metric", ErrInvalid)
	}
	if len(f.Rows) > maxRows {
		return fmt.Errorf("%w: rows", ErrInvalid)
	}
	rowIDs := make(map[string]bool, len(f.Rows))
	for _, r := range f.Rows {
		if r.ID == "" || utf8.RuneCountInString(r.ID) > maxMetricIDRunes {
			return fmt.Errorf("%w: row id", ErrInvalid)
		}
		if rowIDs[r.ID] {
			return fmt.Errorf("%w: duplicate row", ErrInvalid)
		}
		rowIDs[r.ID] = true
		if utf8.RuneCountInString(r.Label) > maxLabelRunes {
			return fmt.Errorf("%w: row label", ErrInvalid)
		}
		switch r.State {
		case "pending", "running", "done", "error":
		default:
			return fmt.Errorf("%w: row state", ErrInvalid)
		}
	}
	if p := f.Phase; p != nil {
		if p.ID == "" || utf8.RuneCountInString(p.ID) > maxMetricIDRunes {
			return fmt.Errorf("%w: phase id", ErrInvalid)
		}
		if utf8.RuneCountInString(p.Label) > maxLabelRunes {
			return fmt.Errorf("%w: phase label", ErrInvalid)
		}
		if p.Index < 0 || p.Total < 0 {
			return fmt.Errorf("%w: phase bounds", ErrInvalid)
		}
		if p.Total > 0 && (p.Index < 1 || p.Index > p.Total) {
			return fmt.Errorf("%w: phase bounds", ErrInvalid)
		}
	}
	return nil
}

// cleanDetail sanitizes redaction-sensitive text then caps it on a UTF-8 byte
// boundary at maxDetailBytes.
func cleanDetail(s string) string {
	s = Clean(s, 0, false)
	if len(s) <= maxDetailBytes {
		return s
	}
	s = s[:maxDetailBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// EstimateFor derives fraction/rate/ETA for a metric by its stable ID. It uses
// only observation times stamped by Marshal and never producer timestamps.
func EstimateFor(previous, current *Snapshot, metricID string, now time.Time, stopping bool) Estimate {
	var est Estimate
	if current == nil {
		return est
	}
	cur, ok := findMetric(current, metricID)
	if !ok {
		return est
	}
	if cur.Total != nil && *cur.Total > 0 && finite(cur.Current) && finite(*cur.Total) {
		frac := cur.Current / *cur.Total
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		est.Fraction = &frac
	}
	est.Stale = current.ObservedAt.IsZero() || now.Sub(current.ObservedAt) > FreshFor
	if stopping || est.Stale {
		return est
	}
	rate, ok := estimateRate(previous, current, cur)
	if !ok || !finite(rate) || rate < 0 {
		return est
	}
	est.Rate = &rate
	if cur.Total != nil && *cur.Total > 0 && rate > 0 {
		remaining := *cur.Total - cur.Current
		if remaining > 0 {
			seconds := remaining / rate
			if finite(seconds) && seconds >= 0 && seconds <= maxETA.Seconds() {
				d := time.Duration(seconds * float64(time.Second))
				if d >= 0 {
					est.ETA = &d
				}
			}
		}
	}
	return est
}

// estimateRate prefers a producer-supplied rate and otherwise derives one from
// the positive delta between consecutive observations of the same phase, unit
// and metric. A counter reset, phase change or unit change yields no delta rate.
func estimateRate(previous, current *Snapshot, cur Metric) (float64, bool) {
	if cur.Rate != nil && finite(*cur.Rate) && *cur.Rate >= 0 {
		return *cur.Rate, true
	}
	if previous == nil {
		return 0, false
	}
	prev, ok := findMetric(previous, cur.ID)
	if !ok || prev.Unit != cur.Unit || phaseID(previous) != phaseID(current) {
		return 0, false
	}
	if !previous.ObservedAt.Before(current.ObservedAt) {
		return 0, false
	}
	delta := cur.Current - prev.Current
	if !(delta > 0) {
		return 0, false
	}
	dt := current.ObservedAt.Sub(previous.ObservedAt).Seconds()
	if dt <= 0 {
		return 0, false
	}
	rate := delta / dt
	if !finite(rate) {
		return 0, false
	}
	return rate, true
}

func findMetric(s *Snapshot, id string) (Metric, bool) {
	if s == nil {
		return Metric{}, false
	}
	if id == "" {
		id = s.PrimaryMetricID
	}
	if id == "" {
		if len(s.Metrics) == 0 {
			return Metric{}, false
		}
		return s.Metrics[0], true
	}
	for _, m := range s.Metrics {
		if m.ID == id {
			return m, true
		}
	}
	return Metric{}, false
}

func phaseID(s *Snapshot) string {
	if s != nil && s.Phase != nil {
		return s.Phase.ID
	}
	return ""
}

// Observer consumes one command's stdout/stderr, keeps a bounded display tail
// and exposes the newest valid events-v1 snapshot. It owns no execution.
type Observer struct {
	mu       sync.Mutex
	config   Config
	now      func() time.Time
	dirty    func()
	stdout   *framer
	stderr   *framer
	latest   *Snapshot
	previous *Snapshot
	tail     []byte
	lastSeq  uint64
	warning  string
	changed  bool
	closed   bool
}

// NewObserver normalizes config once and builds an idle observer. The dirty
// callback must be a nonblocking wake; it runs outside the observer mutex and
// only when semantic data changed.
func NewObserver(config Config, now func() time.Time, dirty func()) (*Observer, error) {
	cfg, err := NormalizeConfig(config)
	if err != nil {
		return nil, err
	}
	// Task 3 supplies the go-test-json adapter; until then it is unsupported.
	if cfg.Source == "go-test-json" {
		return nil, fmt.Errorf("%w: source unsupported", ErrInvalid)
	}
	if now == nil {
		now = time.Now
	}
	return &Observer{
		config: cfg,
		now:    now,
		dirty:  dirty,
		stdout: &framer{limit: MaxEventBytes},
		stderr: &framer{limit: MaxEventBytes},
	}, nil
}

// Write feeds raw child output to the observer. It serializes framing,
// decoding and display updates, then wakes the caller outside the lock.
func (o *Observer) Write(stream Stream, p []byte) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.changed = false
	if stream == Stderr {
		o.stderr.write(p, func(line []byte, oversized bool) { o.feedLine(Stderr, line, oversized) })
	} else {
		o.stdout.write(p, func(line []byte, oversized bool) { o.feedLine(Stdout, line, oversized) })
	}
	changed, dirty := o.changed, o.dirty
	o.mu.Unlock()
	if changed && dirty != nil {
		dirty()
	}
}

// feedLine handles one complete output line. It runs with the mutex held.
func (o *Observer) feedLine(stream Stream, line []byte, oversized bool) {
	if oversized {
		// Bytes were discarded at the framer; skip through the newline.
		return
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	if o.config.Source == "events-v1" && stream == Stdout && bytes.HasPrefix(line, []byte(Prefix)) {
		snap, err := DecodeEvent(line, o.lastSeq, o.now())
		switch {
		case err == nil:
			o.previous = o.latest
			o.latest = snap
			o.lastSeq = snap.Sequence
			o.warning = ""
			o.changed = true
		case errors.Is(err, ErrSequence):
			// A replay is ignored: no tail entry, no observation, no warning.
		default:
			o.setWarning(diagInvalid)
			o.appendTail(line)
		}
		return
	}
	o.appendTail(line)
}

func (o *Observer) setWarning(msg string) {
	if o.warning != "" {
		return
	}
	o.warning = msg
	o.changed = true
}

func (o *Observer) appendTail(line []byte) {
	text := Clean(string(line), 0, true)
	if text == "" {
		return
	}
	o.tail = append(o.tail, text...)
	o.tail = append(o.tail, '\n')
	if len(o.tail) > MaxDisplayBytes {
		o.tail = o.tail[len(o.tail)-MaxDisplayBytes:]
		for len(o.tail) > 0 && !utf8.RuneStart(o.tail[0]) {
			o.tail = o.tail[1:]
		}
	}
	o.changed = true
}

// Snapshot returns a copy of the newest valid snapshot, tagged with any pending
// diagnostic. With no valid snapshot it returns a bounded partial snapshot with
// Sequence=0 and no invented measurements.
func (o *Observer) Snapshot() *Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.latest != nil {
		s := Clone(o.latest)
		if o.warning != "" {
			s.Warning = o.warning
		}
		return s
	}
	if o.warning != "" {
		return &Snapshot{
			Frame:      Frame{Version: Version},
			ObservedAt: o.now(),
			Partial:    true,
			Warning:    o.warning,
		}
	}
	return nil
}

// DisplayTail returns the bounded ordinary-output tail. Valid control records
// are excluded; malformed candidates remain as output.
func (o *Observer) DisplayTail() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.tail)
}

// Close is idempotent. It flushes buffered text/diagnostics and refuses further
// updates. It never invokes process control.
func (o *Observer) Close() {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	o.changed = false
	o.stdout.flush(func(line []byte, oversized bool) { o.feedLine(Stdout, line, oversized) })
	o.stderr.flush(func(line []byte, oversized bool) { o.feedLine(Stderr, line, oversized) })
	changed, dirty := o.changed, o.dirty
	o.mu.Unlock()
	if changed && dirty != nil {
		dirty()
	}
}

// framer splits a byte stream into LF-terminated lines with a hard per-line
// cap. Oversized content is discarded through its newline without unbounded
// buffering; a multi-byte rune split across writes is assembled before use.
type framer struct {
	buf       []byte
	limit     int
	oversized bool
}

func (f *framer) write(p []byte, emit func(line []byte, oversized bool)) {
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.append(p)
			return
		}
		f.append(p[:i])
		if f.oversized {
			emit(nil, true)
		} else {
			emit(f.buf, false)
		}
		f.reset()
		p = p[i+1:]
	}
}

func (f *framer) append(p []byte) {
	if f.oversized || len(p) == 0 {
		return
	}
	if len(f.buf)+len(p) > f.limit {
		f.oversized = true
		f.buf = nil
		return
	}
	f.buf = append(f.buf, p...)
}

func (f *framer) reset() {
	f.buf = nil
	f.oversized = false
}

func (f *framer) flush(emit func(line []byte, oversized bool)) {
	if f.oversized {
		emit(nil, true)
	} else if len(f.buf) > 0 {
		emit(f.buf, false)
	}
	f.reset()
}
