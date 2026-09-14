package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// getDisk fetches /api/disk and decodes it into a map, so the test
// asserts on the exact wire keys rather than on a struct that could
// drift from the contract.
func getDisk(t *testing.T, s *Server) map[string]json.RawMessage {
	t.Helper()
	rec := doReq(t, s, http.MethodGet, "/api/disk", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/disk = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	decodeBody(t, rec, &body)
	return body
}

func jsonInt64(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var v int64
	decodeRaw(t, raw, &v)
	return v
}

func decodeRaw(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", string(raw), err)
	}
}

// TestGetDiskSumsSeededTrees covers case 1: the repos and work figures
// cover the seeded files, the total is their sum, measuredAt is present
// and RFC 3339, and budgetMB mirrors Limits.MaxDiskMB.
func TestGetDiskSumsSeededTrees(t *testing.T) {
	f := testFleetWithState(t)
	f.limits = Limits{MaxDiskMB: 8}
	writeSized(t, filepath.Join(f.stateDir, "repos", "aaa", "pack"), 4096)
	writeSized(t, filepath.Join(f.stateDir, "work", "agent1", "file"), 2048)
	s := NewServer(f, "")

	body := getDisk(t, s)
	repos := jsonInt64(t, body["repos"])
	work := jsonInt64(t, body["work"])
	if repos != 4096 {
		t.Errorf("repos = %d, want 4096", repos)
	}
	if work != 2048 {
		t.Errorf("work = %d, want 2048", work)
	}
	total := jsonInt64(t, body["total"])
	if total != repos+work {
		t.Errorf("total = %d, want repos %d + work %d", total, repos, work)
	}
	if budget := jsonInt64(t, body["budgetMB"]); budget != 8 {
		t.Errorf("budgetMB = %d, want 8", budget)
	}
	var measuredAt string
	decodeRaw(t, body["measuredAt"], &measuredAt)
	if _, err := time.Parse(time.RFC3339, measuredAt); err != nil {
		t.Errorf("measuredAt %q is not RFC 3339: %v", measuredAt, err)
	}
}

// TestGetDiskEmitsZeroBudgetWhenUnlimited covers case 2: MaxDiskMB of
// zero means no budget, and the endpoint must emit 0 rather than
// omitting the key.
func TestGetDiskEmitsZeroBudgetWhenUnlimited(t *testing.T) {
	f := testFleetWithState(t) // Limits{}: MaxDiskMB 0
	s := NewServer(f, "")

	body := getDisk(t, s)
	if _, ok := body["budgetMB"]; !ok {
		t.Fatal("budgetMB missing from the response; 0 means unlimited and must still be present")
	}
	if budget := jsonInt64(t, body["budgetMB"]); budget != 0 {
		t.Errorf("budgetMB = %d, want 0", budget)
	}
}

// TestPostPruneReclaimsOrphans covers cases 3 and 5: an orphaned
// mirror and an orphaned work dir are reclaimed, the follow-up GET
// reflects the reduction (proving invalidation), and the prune audit
// record carries the reclaimed byte count. Audit is recorded by
// Fleet.Prune, not by the handler — this pins that single record.
func TestPostPruneReclaimsOrphans(t *testing.T) {
	f := testFleetWithAuditAndState(t)
	orphanMirror := filepath.Join(f.stateDir, "repos", "deadbeefdeadbeef")
	orphanWork := workspaceDirFor(f.stateDir, "agent-that-no-longer-exists")
	writeSized(t, filepath.Join(orphanMirror, "pack"), 4096)
	writeSized(t, filepath.Join(orphanWork, "file"), 2048)
	s := NewServer(f, "")

	before := getDisk(t, s)
	beforeTotal := jsonInt64(t, before["total"])

	rec := doReq(t, s, http.MethodPost, "/api/prune", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/prune = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var pruneBody struct {
		Reclaimed int64  `json:"reclaimed"`
		Total     int64  `json:"total"`
		Warning   string `json:"warning"`
	}
	decodeBody(t, rec, &pruneBody)
	if pruneBody.Reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0", pruneBody.Reclaimed)
	}
	if pruneBody.Reclaimed != 4096+2048 {
		t.Errorf("reclaimed = %d, want 6144 (the mirror's 4096 plus the work dir's 2048)", pruneBody.Reclaimed)
	}
	if pruneBody.Warning != "" {
		t.Errorf("clean prune carried a warning: %q", pruneBody.Warning)
	}

	// The follow-up GET must see the reduction, not the pre-prune cache.
	after := getDisk(t, s)
	afterTotal := jsonInt64(t, after["total"])
	if afterTotal != beforeTotal-pruneBody.Reclaimed {
		t.Fatalf("GET after prune = %d, want %d (before %d minus reclaimed %d)",
			afterTotal, beforeTotal-pruneBody.Reclaimed, beforeTotal, pruneBody.Reclaimed)
	}

	// Case 5: the audit record, with the byte count, exists exactly once.
	var prunes int
	for _, e := range auditTail(t, f) {
		if e.Event == AuditPrune {
			prunes++
			if e.Bytes != pruneBody.Reclaimed {
				t.Errorf("audit Bytes = %d, want %d", e.Bytes, pruneBody.Reclaimed)
			}
		}
	}
	if prunes != 1 {
		t.Fatalf("prune audit records = %d, want exactly 1 (recorded by Fleet.Prune, not the handler)", prunes)
	}
}

// TestPostPruneWithNothingPrunable covers case 4: a clean state
// directory prunes nothing and reports reclaimed 0.
func TestPostPruneWithNothingPrunable(t *testing.T) {
	f := testFleetWithState(t)
	s := NewServer(f, "")

	// Warm the cache so the test would catch a missing re-measure.
	getDisk(t, s)
	rec := doReq(t, s, http.MethodPost, "/api/prune", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/prune = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var pruneBody struct {
		Reclaimed int64 `json:"reclaimed"`
		Total     int64 `json:"total"`
	}
	decodeBody(t, rec, &pruneBody)
	if pruneBody.Reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 with nothing prunable", pruneBody.Reclaimed)
	}
	if pruneBody.Total != 0 {
		t.Errorf("total = %d, want 0 on an empty state dir", pruneBody.Total)
	}
}

// TestDiskEndpointsRequireFleetMode pins the nil-fleet behavior the
// handlers document: a registry-mode server has no state directory, so
// the endpoints return 503 rather than panicking or reporting zero.
func TestDiskEndpointsRequireFleetMode(t *testing.T) {
	s, _, _, _ := newTestServer(t, "")

	for method, path := range map[string]string{
		http.MethodGet:  "/api/disk",
		http.MethodPost: "/api/prune",
	} {
		rec := doReq(t, s, method, path, nil, nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s = %d, want 503 (body %s)", method, path, rec.Code, rec.Body.String())
		}
	}
}

// TestPostPruneReportsPartialFailure covers the most destructive path:
// one orphan is reclaimed and the next cannot be walked. The response
// must report the reclaimed bytes with a warning rather than 502 — the
// removal is not undone by the later failure — and, the gap this pins,
// the audit must record the partial prune and the error path must
// invalidate the disk cache, both of which the early return used to
// skip.
func TestPostPruneReportsPartialFailure(t *testing.T) {
	// chmod-based failure injection needs permission bits to bind:
	// root ignores them, and Windows has none on directories.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-000 failure injection cannot bind on this platform")
	}
	f := testFleetWithAuditAndState(t)
	// "aaa" sorts before "zzz", so the reclaimable orphan is removed
	// first and reclaimed is non-zero when the prune hits the
	// unreadable one.
	reclaimable := workspaceDirFor(f.stateDir, "aaa")
	blocked := workspaceDirFor(f.stateDir, "zzz")
	writeSized(t, filepath.Join(reclaimable, "file"), 4096)
	writeSized(t, filepath.Join(blocked, "file"), 2048)
	s := NewServer(f, "")

	// Warm the cache while both orphans are readable; the follow-up
	// GET proves the error path dropped it.
	before := getDisk(t, s)
	beforeTotal := jsonInt64(t, before["total"])
	if beforeTotal != 4096+2048 {
		t.Fatalf("seeded total = %d, want %d", beforeTotal, 4096+2048)
	}

	// A directory with no read permission makes filepath.Walk fail to
	// list it (EACCES) while os.ReadDir on the parent still sees the
	// entry, so the prune reaches it and then fails.
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	rec := doReq(t, s, http.MethodPost, "/api/prune", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/prune = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var pruneBody struct {
		Reclaimed int64  `json:"reclaimed"`
		Warning   string `json:"warning"`
	}
	decodeBody(t, rec, &pruneBody)
	if pruneBody.Reclaimed != 4096 {
		t.Errorf("reclaimed = %d, want 4096 (the readable orphan's bytes)", pruneBody.Reclaimed)
	}
	if pruneBody.Warning == "" {
		t.Error("a partial failure must carry a warning, not a clean body")
	}

	// The partial prune is audited: exactly one AuditPrune carrying
	// the bytes that actually left.
	var prunes int
	for _, e := range auditTail(t, f) {
		if e.Event == AuditPrune {
			prunes++
			if e.Bytes != 4096 {
				t.Errorf("audit Bytes = %d, want 4096", e.Bytes)
			}
		}
	}
	if prunes != 1 {
		t.Fatalf("prune audit records = %d, want exactly 1 (the error path must not skip the record)", prunes)
	}

	// Restore readability so the survivor measures again, then prove
	// the error path invalidated the cache: the total is the seeded
	// total minus what was reclaimed, not the stale warm figure.
	if err := os.Chmod(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	after := getDisk(t, s)
	afterTotal := jsonInt64(t, after["total"])
	if afterTotal != beforeTotal-pruneBody.Reclaimed {
		t.Fatalf("GET after partial prune = %d, want %d (before %d minus reclaimed %d)",
			afterTotal, beforeTotal-pruneBody.Reclaimed, beforeTotal, pruneBody.Reclaimed)
	}
}

// TestPostPruneReturns502WhenNothingIsReclaimed covers the outright
// failure: the only orphan cannot be walked, so nothing is reclaimed
// and the handler reports 502 rather than a misleading success. The
// gap this pins: even a failed prune leaves exactly one AuditPrune
// record, with zero bytes, where the early return used to leave none.
func TestPostPruneReturns502WhenNothingIsReclaimed(t *testing.T) {
	// Same injection as the partial-failure test, and the same
	// platform constraint: permission bits must bind or the walk
	// succeeds and the prune quietly cleans up instead of failing.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-000 failure injection cannot bind on this platform")
	}
	f := testFleetWithAuditAndState(t)
	blocked := filepath.Join(f.stateDir, "repos", "deadbeefdeadbeef")
	writeSized(t, filepath.Join(blocked, "pack"), 4096)
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	s := NewServer(f, "")

	rec := doReq(t, s, http.MethodPost, "/api/prune", nil, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /api/prune = %d, want 502 via writeErr (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeBody(t, rec, &body)
	if body.Error == "" {
		t.Error("the 502 must carry the prune failure in the error field")
	}

	// The failed prune is still a prune: exactly one AuditPrune record,
	// with zero bytes rather than no record at all.
	var prunes int
	for _, e := range auditTail(t, f) {
		if e.Event == AuditPrune {
			prunes++
			if e.Bytes != 0 {
				t.Errorf("audit Bytes = %d, want 0 (nothing was reclaimed)", e.Bytes)
			}
		}
	}
	if prunes != 1 {
		t.Fatalf("prune audit records = %d, want exactly 1 (an outright failure is still a prune)", prunes)
	}
}

// TestPostPruneSerializesConcurrentRequests pins pruneMu's reason for
// existing: two simultaneous prunes walk the same trees, and without
// serialization a removeTree can race itself on a directory the other
// call already removed — surfacing as a spurious warning on an
// otherwise healthy fleet. Serialized, one call reclaims the seeded
// bytes and the other finds nothing, so the two reclaimed figures sum
// to the seed with no warning on either response.
func TestPostPruneSerializesConcurrentRequests(t *testing.T) {
	f := testFleetWithAuditAndState(t)
	writeSized(t, filepath.Join(f.stateDir, "repos", "deadbeef0001", "pack"), 4096)
	writeSized(t, filepath.Join(f.stateDir, "repos", "deadbeef0002", "pack"), 2048)
	s := NewServer(f, "")
	const seeded = 4096 + 2048

	// Each goroutine drives the handler end to end and decodes its own
	// response; doReq with a nil body never touches t, so it is safe
	// off the test goroutine. The verdicts are asserted after the
	// WaitGroup, where failures may call t.Fatalf.
	type result struct {
		code      int
		reclaimed int64
		warning   string
		decodeErr error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := doReq(t, s, http.MethodPost, "/api/prune", nil, nil)
			var pruneBody struct {
				Reclaimed int64  `json:"reclaimed"`
				Warning   string `json:"warning"`
			}
			err := json.Unmarshal(rec.Body.Bytes(), &pruneBody)
			results[i] = result{
				code:      rec.Code,
				reclaimed: pruneBody.Reclaimed,
				warning:   pruneBody.Warning,
				decodeErr: err,
			}
		}(i)
	}
	wg.Wait()

	var sum int64
	for i, res := range results {
		if res.code != http.StatusOK {
			t.Fatalf("concurrent POST /api/prune %d = %d, want 200 (body %s)", i, res.code, res.warning)
		}
		if res.decodeErr != nil {
			t.Fatalf("decode response %d: %v", i, res.decodeErr)
		}
		if res.warning != "" {
			t.Errorf("concurrent prune %d carried a warning: %q (a serialized prune has no spurious failures)", i, res.warning)
		}
		sum += res.reclaimed
	}
	if sum != seeded {
		t.Fatalf("reclaimed across both prunes = %d, want %d (each byte reclaimed exactly once)", sum, seeded)
	}
}
