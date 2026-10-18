package progress

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeConfigCombinations(t *testing.T) {
	got, err := NormalizeConfig(Config{})
	if err != nil || got.Profile != "generic" || got.Source != "none" {
		t.Fatalf("defaults: %#v %v", got, err)
	}
	for _, profile := range []string{"generic", "tests", "download", "transfer", "custom", "bogus"} {
		for _, source := range []string{"none", "events-v1", "go-test-json", "bogus"} {
			valid := profile != "bogus" && (source == "none" ||
				source == "events-v1" && profile != "generic" ||
				source == "go-test-json" && profile == "tests")
			_, err := NormalizeConfig(Config{Profile: profile, Source: source})
			if (err == nil) != valid {
				t.Fatalf("%s/%s: %v", profile, source, err)
			}
		}
	}
	for _, title := range []string{strings.Repeat("界", 81), string([]byte{0xff})} {
		if _, err := NormalizeConfig(Config{Title: title}); err == nil {
			t.Fatal("accepted invalid title")
		}
	}
}
func TestProgressCleanRemovesControlsAndSecrets(t *testing.T) {
	secret := "AKIA" + "1234567890ABCDEF"
	got := Clean("\x1b[31mred\x1b[0m\x1b]8;;https://example.invalid\x07link\x1b]8;;\x07\x00\u0085\r\n"+secret, 0, false)
	if strings.Contains(got, secret) || strings.ContainsAny(got, "\x1b\x00\u0085\r\n") {
		t.Fatalf("unsafe display: %q", got)
	}
	if !strings.Contains(got, "redlink") {
		t.Fatalf("lost text: %q", got)
	}
	if got := Clean("界界界", 2, false); got != "界界" {
		t.Fatal(got)
	}
	if got := Clean("a\nb\tc", 0, true); got != "a\nb\tc" {
		t.Fatal(got)
	}
}
func TestProgressCloneOwnsAllNestedValues(t *testing.T) {
	total, rate := 10.0, 2.0
	original := &Snapshot{Frame: Frame{Version: 1, Phase: &Phase{ID: "a"},
		Metrics: []Metric{{ID: "bytes", Total: &total, Rate: &rate}},
		Rows:    []Row{{ID: "row", State: "running"}}}}
	copied := Clone(original)
	copied.Phase.ID = "b"
	*copied.Metrics[0].Total = 20
	*copied.Metrics[0].Rate = 4
	copied.Rows[0].State = "done"
	if original.Phase.ID != "a" || total != 10 || rate != 2 || original.Rows[0].State != "running" {
		t.Fatal("clone aliases source")
	}
	if Clone(nil) != nil {
		t.Fatal("nil clone")
	}
}

func TestProgressFramesAcrossChunksAndCRLF(t *testing.T) {
	payload := `{"version":1,"sequence":1,"primaryMetricId":"bytes","metrics":[{"id":"bytes","label":"Bytes","unit":"bytes","current":5,"total":10}],"rows":[{"id":"step","label":"Step","state":"running"}],"detail":"hello"}`
	line := Prefix + payload
	for split := 0; split <= len(line); split++ {
		obs, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		obs.Write(Stdout, []byte(line[:split]))
		obs.Write(Stdout, []byte(line[split:]))
		obs.Write(Stdout, []byte("\n"))
		snap := obs.Snapshot()
		if snap == nil || snap.Sequence != 1 || snap.Detail != "hello" ||
			len(snap.Metrics) != 1 || snap.Metrics[0].Current != 5 {
			t.Fatalf("split %d: %#v", split, snap)
		}
		// Replaying the same frame must not advance or duplicate it.
		obs.Write(Stdout, []byte(line+"\n"))
		if again := obs.Snapshot(); again.Sequence != 1 {
			t.Fatalf("split %d replay: %#v", split, again)
		}
	}
	// Byte-at-a-time framing with a CRLF terminator.
	obs, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(line); i++ {
		obs.Write(Stdout, []byte{line[i]})
	}
	obs.Write(Stdout, []byte("\r\n"))
	if snap := obs.Snapshot(); snap == nil || snap.Sequence != 1 {
		t.Fatalf("bytewise CRLF: %#v", snap)
	}
}

func TestProgressInvalidFieldsCannotSetRuntimeFacts(t *testing.T) {
	longLabel := strings.Repeat("l", 81)
	longID := strings.Repeat("i", 65)
	longUnit := strings.Repeat("u", 17)
	cases := map[string]string{
		"version0":        `{"version":0,"sequence":1}`,
		"version2":        `{"version":2,"sequence":1}`,
		"sequence0":       `{"version":1,"sequence":0}`,
		"sequenceBig":     `{"version":1,"sequence":9007199254740992}`,
		"unknownPrimary":  `{"version":1,"sequence":1,"primaryMetricId":"nope","metrics":[{"id":"a","label":"A","unit":"items","current":1}]}`,
		"duplicateMetric": `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1},{"id":"a","label":"B","unit":"items","current":2}]}`,
		"tooManyMetrics":  `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1},{"id":"b","label":"B","unit":"items","current":1},{"id":"c","label":"C","unit":"items","current":1},{"id":"d","label":"D","unit":"items","current":1},{"id":"e","label":"E","unit":"items","current":1}]}`,
		"tooManyRows":     `{"version":1,"sequence":1,"rows":[{"id":"r1","label":"","state":"done"},{"id":"r2","label":"","state":"done"},{"id":"r3","label":"","state":"done"},{"id":"r4","label":"","state":"done"},{"id":"r5","label":"","state":"done"},{"id":"r6","label":"","state":"done"},{"id":"r7","label":"","state":"done"},{"id":"r8","label":"","state":"done"},{"id":"r9","label":"","state":"done"}]}`,
		"badRowState":     `{"version":1,"sequence":1,"rows":[{"id":"r","label":"R","state":"finished"}]}`,
		"negativeCurrent": `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"bytes","current":-1}]}`,
		"currentTooLarge": `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":9007199254740992}]}`,
		"fractionalCount": `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"bytes","current":1.5}]}`,
		"labelTooLong":    `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"` + longLabel + `","unit":"items","current":1}]}`,
		"idTooLong":       `{"version":1,"sequence":1,"metrics":[{"id":"` + longID + `","label":"A","unit":"items","current":1}]}`,
		"unitTooLong":     `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"` + longUnit + `","current":1}]}`,
		"phaseOutOfRange": `{"version":1,"sequence":1,"phase":{"id":"p","label":"P","index":3,"total":2}}`,
		"negativeTotal":   `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1,"total":-5}]}`,
		"trailingData":    `{"version":1,"sequence":1} {"version":1,"sequence":2}`,
	}
	for name, payload := range cases {
		if snap, err := DecodeEvent([]byte(Prefix+payload), 0, time.Unix(0, 0)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: snap=%#v err=%v", name, snap, err)
		}
	}
	// Forged runtime facts in otherwise valid JSON are ignored, not honored.
	forged := `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1}],"rows":[{"id":"r","label":"R","state":"done"}],"executionState":"failed","actions":["kill"],"reportedError":true,"owner":"attacker","capabilities":["shell"],"warning":"forged","partial":true}`
	snap, err := DecodeEvent([]byte(Prefix+forged), 0, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("forged extras rejected: %v", err)
	}
	if snap.ReportedError || snap.Partial || snap.Warning != "" || len(snap.Rows) != 1 || snap.Rows[0].State != "done" {
		t.Fatalf("forged facts leaked: %#v", snap)
	}
	// A reported error row is the only source of ReportedError.
	errRows := `{"version":1,"sequence":1,"rows":[{"id":"r","label":"R","state":"error"}]}`
	if snap, err := DecodeEvent([]byte(Prefix+errRows), 0, time.Unix(0, 0)); err != nil || !snap.ReportedError {
		t.Fatalf("error row: %#v %v", snap, err)
	}
	// Identities that collide only after sanitization are rejected.
	collide := `{"version":1,"sequence":1,"metrics":[{"id":"a\u0001","label":"A","unit":"items","current":1},{"id":"a\u0002","label":"B","unit":"items","current":1}]}`
	if _, err := DecodeEvent([]byte(Prefix+collide), 0, time.Unix(0, 0)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("collision accepted: %v", err)
	}
	// An outdated denominator clears total but keeps the measured current.
	outdated := `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"bytes","current":50,"total":10}]}`
	if snap, err := DecodeEvent([]byte(Prefix+outdated), 0, time.Unix(0, 0)); err != nil ||
		snap.Metrics[0].Total != nil || snap.Metrics[0].Current != 50 || snap.Warning == "" {
		t.Fatalf("outdated: %#v %v", snap, err)
	}
}

func TestProgressOversizeAndMalformedRecoverWithoutGrowth(t *testing.T) {
	valid := Prefix + `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1}]}` + "\n"
	obs, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An oversize line is skipped through its newline with no valid snapshot,
	// but it surfaces one bounded diagnostic until recovery.
	obs.Write(Stdout, bytes.Repeat([]byte("x"), MaxEventBytes*2))
	obs.Write(Stdout, []byte("\n"))
	if snap := obs.Snapshot(); snap == nil || snap.Sequence != 0 || !snap.Partial || snap.Warning == "" || len(snap.Metrics) != 0 {
		t.Fatalf("oversize diagnostic: %#v", snap)
	}
	obs.Write(Stdout, []byte(valid))
	if snap := obs.Snapshot(); snap == nil || snap.Sequence != 1 || snap.Warning != "" {
		t.Fatalf("no recovery after oversize: %#v", snap)
	}
	// A malformed control record surfaces output plus one bounded diagnostic.
	mal, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mal.Write(Stdout, []byte(Prefix+"{not json}\n"))
	snap := mal.Snapshot()
	if snap == nil || snap.Sequence != 0 || !snap.Partial || snap.Warning == "" || len(snap.Metrics) != 0 {
		t.Fatalf("partial diagnostic: %#v", snap)
	}
	if !strings.Contains(mal.DisplayTail(), "{not json}") {
		t.Fatalf("malformed record missing from tail: %q", mal.DisplayTail())
	}
	mal.Write(Stdout, []byte(valid))
	snap = mal.Snapshot()
	if snap == nil || snap.Sequence != 1 || snap.Warning != "" {
		t.Fatalf("no recovery after malformed: %#v", snap)
	}
	// Buffers and tail stay bounded under sustained output.
	for i := 0; i < 2000; i++ {
		mal.Write(Stderr, []byte("ordinary output line\n"))
	}
	if got := len(mal.DisplayTail()); got > MaxDisplayBytes {
		t.Fatalf("tail grew to %d", got)
	}
	// An unterminated control record is diagnostic/output, not a snapshot.
	unterminated, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	unterminated.Write(Stdout, []byte(Prefix+`{"version":1,"sequence":1`))
	unterminated.Close()
	if snap := unterminated.Snapshot(); snap != nil && snap.Sequence != 0 {
		t.Fatalf("unterminated accepted: %#v", snap)
	}
}

func TestProgressOversizeCustomLineSetsDiagnostic(t *testing.T) {
	obs, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	obs.Write(Stdout, bytes.Repeat([]byte("x"), MaxEventBytes*2))
	obs.Write(Stdout, []byte("\n"))
	snap := obs.Snapshot()
	if snap == nil || snap.Sequence != 0 || !snap.Partial || snap.Warning == "" || len(snap.Metrics) != 0 {
		t.Fatalf("oversize produced no diagnostic: %#v", snap)
	}
	// The diagnostic is bounded to one until recovery.
	obs.Write(Stdout, bytes.Repeat([]byte("y"), MaxEventBytes*2))
	obs.Write(Stdout, []byte("\n"))
	if again := obs.Snapshot(); again == nil || again.Warning != snap.Warning {
		t.Fatalf("second oversize changed diagnostic: %#v", again)
	}
	// A valid record recovers the last valid progress and clears the warning.
	valid := Prefix + `{"version":1,"sequence":1,"metrics":[{"id":"a","label":"A","unit":"items","current":1}]}` + "\n"
	obs.Write(Stdout, []byte(valid))
	if got := obs.Snapshot(); got == nil || got.Sequence != 1 || got.Warning != "" {
		t.Fatalf("no recovery after oversize: %#v", got)
	}
}

func TestProgressSequenceAndReplacement(t *testing.T) {
	obs, err := NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := Prefix + `{"version":1,"sequence":1,"phase":{"id":"p","label":"P","index":1,"total":2},"primaryMetricId":"a","metrics":[{"id":"a","label":"A","unit":"items","current":3,"total":9}],"rows":[{"id":"r","label":"R","state":"running"}],"detail":"one"}` + "\n"
	obs.Write(Stdout, []byte(first))
	s1 := obs.Snapshot()
	if s1 == nil || s1.Sequence != 1 || s1.Detail != "one" || s1.Phase == nil || len(s1.Rows) != 1 {
		t.Fatalf("first: %#v", s1)
	}
	// A complete frame replaces the previous one; omitted fields clear.
	second := Prefix + `{"version":1,"sequence":2,"metrics":[{"id":"a","label":"A","unit":"items","current":4}]}` + "\n"
	obs.Write(Stdout, []byte(second))
	s2 := obs.Snapshot()
	if s2 == nil || s2.Sequence != 2 || s2.Phase != nil || s2.Rows != nil || s2.Detail != "" {
		t.Fatalf("partial merge: %#v", s2)
	}
	// A replay of an older sequence is ignored.
	obs.Write(Stdout, []byte(first))
	s3 := obs.Snapshot()
	if s3 == nil || s3.Sequence != 2 || s3.Metrics[0].Current != 4 {
		t.Fatalf("replay advanced: %#v", s3)
	}
	if _, err := DecodeEvent([]byte(strings.TrimSuffix(first, "\n")), 2, time.Unix(0, 0)); !errors.Is(err, ErrSequence) {
		t.Fatalf("want ErrSequence, got %v", err)
	}
	// Snapshot returns a copy.
	s3.Metrics[0].Current = 999
	if obs.Snapshot().Metrics[0].Current != 4 {
		t.Fatal("snapshot aliases internal state")
	}
}

func TestProgressEstimateUnknownChangedAndStale(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ptr := func(v float64) *float64 { return &v }
	mk := func(seq uint64, at time.Time, phase, unit string, cur float64, tot, rate *float64) *Snapshot {
		s := &Snapshot{ObservedAt: at, Frame: Frame{Version: 1, Sequence: seq,
			Metrics: []Metric{{ID: "m", Label: "M", Unit: unit, Current: cur, Total: tot, Rate: rate}}}}
		if phase != "" {
			s.Phase = &Phase{ID: phase, Label: phase, Index: 1, Total: 1}
		}
		return s
	}
	if est := EstimateFor(nil, mk(1, now, "p", "items", 5, nil, nil), "m", now, false); est.Fraction != nil || est.ETA != nil {
		t.Fatalf("no denominator: %#v", est)
	}
	if est := EstimateFor(nil, mk(1, now, "p", "items", 25, ptr(100), nil), "m", now, false); est.Fraction == nil || *est.Fraction != 0.25 || est.ETA != nil {
		t.Fatalf("fraction: %#v", est)
	}
	if est := EstimateFor(nil, mk(1, now, "p", "items", 0, ptr(100), ptr(2)), "m", now, false); est.Rate == nil || *est.Rate != 2 || est.ETA == nil || *est.ETA != 50*time.Second {
		t.Fatalf("supplied rate: %#v", est)
	}
	if est := EstimateFor(mk(1, now.Add(-2*time.Second), "p", "items", 0, ptr(100), nil), mk(2, now, "p", "items", 10, ptr(100), nil), "m", now, false); est.Rate == nil || *est.Rate != 5 || est.ETA == nil || *est.ETA != 18*time.Second {
		t.Fatalf("delta rate: %#v", est)
	}
	if est := EstimateFor(mk(1, now.Add(-time.Second), "p", "items", 80, ptr(100), nil), mk(2, now, "p", "items", 10, ptr(100), nil), "m", now, false); est.Rate != nil || est.ETA != nil || est.Fraction == nil {
		t.Fatalf("counter reset: %#v", est)
	}
	if est := EstimateFor(mk(1, now.Add(-time.Second), "p", "items", 0, ptr(100), nil), mk(2, now, "q", "items", 10, ptr(100), nil), "m", now, false); est.Rate != nil || est.ETA != nil {
		t.Fatalf("phase reset: %#v", est)
	}
	if est := EstimateFor(mk(1, now.Add(-time.Second), "p", "items", 0, ptr(100), nil), mk(2, now, "p", "bytes", 10, ptr(100), nil), "m", now, false); est.Rate != nil || est.ETA != nil {
		t.Fatalf("unit reset: %#v", est)
	}
	if est := EstimateFor(nil, mk(1, now.Add(-time.Minute), "p", "items", 5, ptr(100), ptr(1)), "m", now, false); !est.Stale || est.Rate != nil || est.ETA != nil || est.Fraction == nil {
		t.Fatalf("stale: %#v", est)
	}
	if est := EstimateFor(nil, mk(1, now, "p", "items", 5, ptr(100), ptr(1)), "m", now, true); est.Rate != nil || est.ETA != nil || est.Fraction == nil {
		t.Fatalf("stopping: %#v", est)
	}
	tiny := 1e-9
	if est := EstimateFor(nil, mk(1, now, "p", "items", 0, ptr(1e6), &tiny), "m", now, false); est.Rate == nil || est.ETA != nil {
		t.Fatalf("tiny rate: %#v", est)
	}
	if est := EstimateFor(nil, mk(1, now, "p", "items", 1, ptr(100), ptr(1)), "other", now, false); est.Fraction != nil || est.Rate != nil {
		t.Fatalf("unknown metric: %#v", est)
	}
}

func TestProgressObserverCloseAndConcurrentStreams(t *testing.T) {
	var obs *Observer
	var dirtyCalls int64
	var underLock, probed int32
	dirty := func() {
		atomic.AddInt64(&dirtyCalls, 1)
		if obs != nil && atomic.CompareAndSwapInt32(&probed, 0, 1) {
			done := make(chan struct{})
			go func() { _ = obs.Snapshot(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				atomic.StoreInt32(&underLock, 1)
			}
		}
	}
	var err error
	obs, err = NewObserver(Config{Profile: "custom", Source: "events-v1"}, nil, dirty)
	if err != nil {
		t.Fatal(err)
	}
	// A tests/go-test-json observer is supported and starts idle.
	gobs, err := NewObserver(Config{Profile: "tests", Source: "go-test-json"}, nil, nil)
	if err != nil {
		t.Fatalf("go-test-json construction rejected: %v", err)
	}
	if snap := gobs.Snapshot(); snap != nil {
		t.Fatalf("idle go observer invented a snapshot: %#v", snap)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				obs.Write(Stderr, []byte("ordinary stderr line\n"))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			line := Prefix + `{"version":1,"sequence":` + strconv.Itoa(i+1) +
				`,"metrics":[{"id":"a","label":"A","unit":"items","current":` + strconv.Itoa(i) + `}]}` + "\n"
			obs.Write(Stdout, []byte(line))
		}
	}()
	wg.Wait()
	if atomic.LoadInt32(&underLock) != 0 {
		t.Fatal("dirty ran while the observer mutex was held")
	}
	if snap := obs.Snapshot(); snap == nil || snap.Sequence != 200 {
		t.Fatalf("final snapshot: %#v", snap)
	}
	if len(obs.DisplayTail()) > MaxDisplayBytes {
		t.Fatal("tail grew")
	}
	obs.Close()
	obs.Close() // idempotent
	before := atomic.LoadInt64(&dirtyCalls)
	obs.Write(Stdout, []byte(Prefix+`{"version":1,"sequence":999,"metrics":[{"id":"a","label":"A","unit":"items","current":1}]}`+"\n"))
	if atomic.LoadInt64(&dirtyCalls) != before {
		t.Fatal("dirty fired after Close")
	}
	if snap := obs.Snapshot(); snap == nil || snap.Sequence != 200 {
		t.Fatalf("Close mutated snapshot: %#v", snap)
	}
}

// --- go test -json adapter -------------------------------------------------

func newGoObserver(t *testing.T) *Observer {
	t.Helper()
	o, err := NewObserver(Config{Profile: "tests", Source: "go-test-json"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func writeGo(t *testing.T, o *Observer, events ...string) {
	t.Helper()
	for _, e := range events {
		o.Write(Stdout, append([]byte(e), '\n'))
	}
}

func goMetricValue(t *testing.T, s *Snapshot, id string) float64 {
	t.Helper()
	if s == nil {
		t.Fatalf("nil snapshot for %s", id)
	}
	for _, m := range s.Metrics {
		if m.ID == id {
			if m.Total != nil {
				t.Fatalf("fabricated denominator on %s: %v", id, *m.Total)
			}
			return m.Current
		}
	}
	t.Fatalf("missing metric %s", id)
	return 0
}

func TestGoProgressPackagesAndTestsStayDistinct(t *testing.T) {
	o := newGoObserver(t)
	writeGo(t, o,
		`{"Time":"2024-01-01T00:00:00Z","Action":"start","Package":"a"}`,
		`{"Time":"2024-01-01T00:00:01Z","Action":"run","Package":"a"}`,
		`{"Time":"2024-01-01T00:00:02Z","Action":"run","Package":"a","Test":"TestOne"}`,
		`{"Time":"2024-01-01T00:00:03Z","Action":"pass","Package":"a","Test":"TestOne","Elapsed":0.01}`,
		`{"Time":"2024-01-01T00:00:04Z","Action":"run","Package":"a","Test":"TestTwo"}`,
		`{"Time":"2024-01-01T00:00:05Z","Action":"fail","Package":"a","Test":"TestTwo","Elapsed":0.02}`,
		`{"Time":"2024-01-01T00:00:06Z","Action":"run","Package":"a","Test":"TestThree"}`,
		`{"Time":"2024-01-01T00:00:07Z","Action":"skip","Package":"a","Test":"TestThree"}`,
		`{"Time":"2024-01-01T00:00:08Z","Action":"pass","Package":"a"}`,
	)
	s := o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 1 {
		t.Fatalf("passed=%v want 1", got)
	}
	if got := goMetricValue(t, s, "failed"); got != 1 {
		t.Fatalf("failed=%v want 1", got)
	}
	if got := goMetricValue(t, s, "skipped"); got != 1 {
		t.Fatalf("skipped=%v want 1", got)
	}
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("running=%v want 0", got)
	}
	// An empty Test is package state and never an extra case.
	if got := o.goIdentityCount(); got != 3 {
		t.Fatalf("identities=%d want 3", got)
	}
	if !s.ReportedError {
		t.Fatal("a failing test must report an error")
	}
	if len(s.Metrics) != 4 {
		t.Fatalf("metrics=%d want at most 4", len(s.Metrics))
	}
}

func TestGoProgressCachedRepeatedAndDuplicateActions(t *testing.T) {
	o := newGoObserver(t)
	writeGo(t, o,
		`{"Action":"run","Package":"b","Test":"TestCached"}`,
		`{"Action":"pass","Package":"b","Test":"TestCached"}`,
		// The same terminal arrives again: cached/replayed records must not recount.
		`{"Action":"pass","Package":"b","Test":"TestCached"}`,
		`{"Action":"pass","Package":"b","Test":"TestCached"}`,
	)
	s := o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 1 {
		t.Fatalf("cached recount: passed=%v want 1", got)
	}
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("running=%v want 0", got)
	}
	// A repeated run on a live invocation is a duplicate, not a new case.
	writeGo(t, o,
		`{"Time":"2024-01-02T00:00:00Z","Action":"run","Package":"c","Test":"TestLive"}`,
		`{"Time":"2024-01-02T00:00:00Z","Action":"run","Package":"c","Test":"TestLive"}`,
		`{"Time":"2024-01-02T00:00:00Z","Action":"pause","Package":"c","Test":"TestLive"}`,
		`{"Time":"2024-01-02T00:00:01Z","Action":"cont","Package":"c","Test":"TestLive"}`,
	)
	if got := goMetricValue(t, o.Snapshot(), "running"); got != 1 {
		t.Fatalf("running=%v want 1", got)
	}
	// An older timestamped run cannot reopen a settled invocation.
	writeGo(t, o, `{"Time":"2024-01-02T00:00:02Z","Action":"pass","Package":"c","Test":"TestLive"}`)
	writeGo(t, o, `{"Time":"2024-01-01T00:00:00Z","Action":"run","Package":"c","Test":"TestLive"}`)
	s = o.Snapshot()
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("stale run reopened: running=%v want 0", got)
	}
	if got := goMetricValue(t, s, "passed"); got != 2 {
		t.Fatalf("passed=%v want 2", got)
	}
	if !s.Partial {
		t.Fatal("an out-of-order run must mark coverage partial")
	}
	// A later run explicitly opens the next invocation and settles once.
	writeGo(t, o, `{"Time":"2024-01-02T00:00:03Z","Action":"run","Package":"c","Test":"TestLive"}`)
	if got := goMetricValue(t, o.Snapshot(), "running"); got != 1 {
		t.Fatalf("new invocation not opened: running=%v", got)
	}
	writeGo(t, o, `{"Time":"2024-01-02T00:00:04Z","Action":"pass","Package":"c","Test":"TestLive"}`)
	writeGo(t, o, `{"Time":"2024-01-02T00:00:04Z","Action":"pass","Package":"c","Test":"TestLive"}`)
	s = o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 3 {
		t.Fatalf("passed=%v want 3", got)
	}
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("running=%v want 0", got)
	}
}

func TestGoProgressPauseBenchmarkAndBuildFailure(t *testing.T) {
	o := newGoObserver(t)
	writeGo(t, o,
		`{"Time":"2024-03-01T00:00:00Z","Action":"run","Package":"p","Test":"TestBench"}`,
		`{"Time":"2024-03-01T00:00:01Z","Action":"output","Package":"p","Test":"TestBench","Output":"=== RUN   TestBench\n"}`,
		`{"Time":"2024-03-01T00:00:02Z","Action":"pause","Package":"p","Test":"TestBench"}`,
		`{"Time":"2024-03-01T00:00:03Z","Action":"cont","Package":"p","Test":"TestBench"}`,
		`{"Time":"2024-03-01T00:00:04Z","Action":"pass","Package":"p","Test":"TestBench"}`,
		`{"Time":"2024-03-01T00:00:05Z","Action":"run","Package":"p","Test":"TestBroken"}`,
		`{"Time":"2024-03-01T00:00:06Z","Action":"fail","Package":"p","Test":"TestBroken"}`,
	)
	s := o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 1 {
		t.Fatalf("passed=%v want 1", got)
	}
	if got := goMetricValue(t, s, "failed"); got != 1 {
		t.Fatalf("failed=%v want 1", got)
	}
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("running=%v want 0", got)
	}
	// The filtered tail keeps real output but drops test result markers.
	tail := o.DisplayTail()
	if !strings.Contains(tail, "=== RUN   TestBench") {
		t.Fatalf("lost output: %q", tail)
	}
	if strings.Contains(tail, "PASS") || strings.Contains(tail, "FAIL") {
		t.Fatalf("result markers leaked into tail: %q", tail)
	}
	// A package build failure fails the package and reports an error without
	// inventing any case.
	writeGo(t, o,
		`{"Time":"2024-03-01T00:00:07Z","Action":"build-output","Package":"q","Output":"# q\n"}`,
		`{"Time":"2024-03-01T00:00:08Z","Action":"build-fail","Package":"q","FailedBuild":"build"}`,
	)
	s = o.Snapshot()
	if !s.ReportedError {
		t.Fatal("build failure must report an error")
	}
	if got := goMetricValue(t, s, "passed"); got != 1 {
		t.Fatalf("build failure changed passed: %v", got)
	}
	if got := o.goIdentityCount(); got != 2 {
		t.Fatalf("build failure invented a case: %d", got)
	}
	// Output-only and pause/cont records never imply a pass.
	writeGo(t, o, `{"Time":"2024-03-01T00:00:09Z","Action":"output","Package":"p","Output":"still running\n"}`)
	if got := goMetricValue(t, o.Snapshot(), "passed"); got != 1 {
		t.Fatalf("output implied a pass: %v", got)
	}
}

func TestGoProgressInvalidAndIdentityCapIsPartial(t *testing.T) {
	o := newGoObserver(t)
	writeGo(t, o, `not json at all`)
	writeGo(t, o, `{"Time":"2024-04-01T00:00:00Z","Action":"run","Package":"p","Test":"TestOK"}`)
	writeGo(t, o, `{"Time":"2024-04-01T00:00:01Z","Action":"pass","Package":"p","Test":"TestOK"}`)
	s := o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 1 {
		t.Fatalf("passed=%v want 1", got)
	}
	if !s.Partial {
		t.Fatal("a malformed record must mark coverage partial")
	}
	if !strings.Contains(o.DisplayTail(), "not json at all") {
		t.Fatalf("malformed record missing from diagnostics: %q", o.DisplayTail())
	}
	// The identity cap is bounded: known counts survive, new identities are
	// refused and coverage stops claiming completeness.
	capO := newGoObserver(t)
	for i := 0; i < MaxCases+5; i++ {
		writeGo(t, capO, `{"Action":"run","Package":"c","Test":"Test`+strconv.Itoa(i)+`"}`)
	}
	if got := capO.goIdentityCount(); got != MaxCases {
		t.Fatalf("identities=%d want %d", got, MaxCases)
	}
	if got := goMetricValue(t, capO.Snapshot(), "running"); got != MaxCases {
		t.Fatalf("running=%v want %d", got, MaxCases)
	}
	if !capO.Snapshot().Partial {
		t.Fatal("excess identities must mark coverage partial")
	}
}

func TestGoProgressBurstCountsBeforeCoalescing(t *testing.T) {
	o := newGoObserver(t)
	for i := 0; i < 50; i++ {
		name := "Test" + strconv.Itoa(i)
		writeGo(t, o, `{"Time":"2024-05-01T00:00:00Z","Action":"run","Package":"p","Test":"`+name+`"}`)
	}
	// Every case is already counted before any caller coalesces notifications.
	s := o.Snapshot()
	if got := goMetricValue(t, s, "running"); got != 50 {
		t.Fatalf("running=%v want 50", got)
	}
	for i := 0; i < 50; i++ {
		name := "Test" + strconv.Itoa(i)
		writeGo(t, o, `{"Time":"2024-05-01T00:00:01Z","Action":"pass","Package":"p","Test":"`+name+`"}`)
	}
	s = o.Snapshot()
	if got := goMetricValue(t, s, "passed"); got != 50 {
		t.Fatalf("passed=%v want 50", got)
	}
	if got := goMetricValue(t, s, "running"); got != 0 {
		t.Fatalf("running=%v want 0", got)
	}
	if s.Partial {
		t.Fatal("a clean burst must not be partial")
	}
	if len(s.Metrics) != 4 {
		t.Fatalf("metrics=%d want 4", len(s.Metrics))
	}
	for _, m := range s.Metrics {
		if m.Total != nil {
			t.Fatalf("fabricated denominator on %s", m.ID)
		}
	}
}

func TestGoProgressObservationTimeDoesNotAdvanceOnRead(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	o, err := NewObserver(Config{Profile: "tests", Source: "go-test-json"}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeGo(t, o, `{"Action":"run","Package":"p","Test":"TestOne"}`)
	first := o.Snapshot()
	if first == nil || first.ObservedAt != now {
		t.Fatalf("observation: %#v", first)
	}
	// A read must not restamp the observation time.
	now = now.Add(FreshFor + time.Second)
	second := o.Snapshot()
	if second == nil || !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("read advanced observation: %v -> %#v", first.ObservedAt, second)
	}
	// A finished burst therefore lapses instead of staying forever fresh.
	if est := EstimateFor(nil, second, "passed", now, false); !est.Stale {
		t.Fatalf("stale measurement not reported: %#v", est)
	}
}
