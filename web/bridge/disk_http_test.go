package bridge

import (
	"encoding/json"
	"net/http"
	"path/filepath"
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
