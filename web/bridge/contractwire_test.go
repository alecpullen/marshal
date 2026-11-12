package bridge

// This file records the bridge's current JSON wire, byte for byte, before the
// moved types are aliased onto the contract package. Each recording is one
// subtest; its bytes live in testdata/contract/<name>.json and are compared
// exactly. Run with -update to re-record.
//
// Left out of the recordings because today's fixtures cannot drive it to a
// deterministic result:
//   - the "warning" key POST /api/agents adds when Spawn returns a non-fatal
//     error after minting an agent id. No fixture makes a spawn warn, so there
//     is nothing stable to record; the recording is the clean answer only.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

// updateContract rewrites the recorded wire files instead of comparing them.
var updateContract = flag.Bool("update", false, "rewrite testdata/contract recordings")

// contractDir holds one file per recording, named after its subtest.
const contractDir = "testdata/contract"

// wireRecording is one recorded message: a stable name and the bytes today's
// code produces for it.
type wireRecording struct {
	name   string
	record func(t *testing.T) []byte
}

// TestTheWireIsUnchanged pins the bridge wire: every recording must still
// encode to exactly the bytes in testdata/contract.
func TestTheWireIsUnchanged(t *testing.T) {
	for _, rec := range wireRecordings() {
		t.Run(rec.name, func(t *testing.T) {
			got := rec.record(t)
			path := filepath.Join(contractDir, rec.name+".json")
			if *updateContract {
				if err := os.MkdirAll(contractDir, 0o755); err != nil {
					t.Fatalf("create %s: %v", contractDir, err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read recording: %v (run with -update to record it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s changed the wire\nrecorded: %s\nnow:      %s", rec.name, want, got)
			}
		})
	}
}

func wireRecordings() []wireRecording {
	recs := []wireRecording{}
	for _, group := range [][]wireRecording{
		typeRecordings[AgentStatus]("AgentStatus"),
		typeRecordings[PendingRequest]("PendingRequest"),
		typeRecordings[AccessState]("AccessState"),
		typeRecordings[AgentWorkspace]("AgentWorkspace"),
		typeRecordings[GateOverride]("GateOverride"),
		typeRecordings[ProjectStatus]("ProjectStatus"),
		typeRecordings[gateRecord]("gateRecord"),
		typeRecordings[ExitResult]("ExitResult"),
		typeRecordings[gateResult]("gateResult"),
		typeRecordings[Budgets]("Budgets"),
		typeRecordings[budgetDelta]("budgetDelta"),
		typeRecordings[budgetAgentSpend]("budgetAgentSpend"),
		typeRecordings[budgetReport]("budgetReport"),
		typeRecordings[DaemonInfo]("DaemonInfo"),
		typeRecordings[DaemonRefresh]("DaemonRefresh"),
		typeRecordings[DaemonChild]("DaemonChild"),
		typeRecordings[DaemonSession]("DaemonSession"),
		typeRecordings[PinState]("PinState"),
		fleetDeltaRecordings(),
		answerRecordings(),
		deltaRecordings(),
	} {
		recs = append(recs, group...)
	}
	return recs
}

// typeRecordings records a struct filled and zero, through the same encoder
// the handlers use.
func typeRecordings[T any](name string) []wireRecording {
	return []wireRecording{
		{name: name, record: func(*testing.T) []byte { return encodeWire(fillValue(*new(T))) }},
		{name: name + ".zero", record: func(*testing.T) []byte { return encodeWire(*new(T)) }},
	}
}

// fleetDeltaRecordings pins the fleet stream's delta envelope, including the
// proof that its two off-wire fields never reach the encoder.
func fleetDeltaRecordings() []wireRecording {
	return []wireRecording{
		{name: "fleetDelta", record: func(t *testing.T) []byte {
			d := fillValue(fleetDelta{})
			// Usage is exported (json:"-"), so fillValue fills it; clear it
			// for the "off-wire fields unpopulated" recording.
			d.Usage = nil
			return encodeWire(d)
		}},
		{name: "fleetDelta.offwire", record: func(t *testing.T) []byte {
			d := fillValue(fleetDelta{})
			d.accessSession = "off-wire-session"
			d.Usage = json.RawMessage(`"off-wire-usage"`)
			got := encodeWire(d)
			for _, banned := range []string{"off-wire-session", "off-wire-usage", "accessSession", "Usage"} {
				if bytes.Contains(got, []byte(banned)) {
					t.Fatalf("fleetDelta put its off-wire field %q on the wire: %s", banned, got)
				}
			}
			return got
		}},
		{name: "fleetDelta.zero", record: func(t *testing.T) []byte {
			got := encodeWire(fleetDelta{})
			for _, banned := range []string{"accessSession", "Usage"} {
				if bytes.Contains(got, []byte(banned)) {
					t.Fatalf("a zero fleetDelta put %q on the wire: %s", banned, got)
				}
			}
			return got
		}},
	}
}

// answerRecordings pins the map-shaped handler answers a client reads.
func answerRecordings() []wireRecording {
	return []wireRecording{
		{name: "pending_list", record: pendingListRecording},
		{name: "pending_deny", record: pendingDenyRecording},
		{name: "agents_spawn", record: agentSpawnRecording},
		{name: "agent_discard", record: agentDiscardRecording},
		{name: "pending_approve", record: pendingApproveRecording},
	}
}

// pendingListRecording lists two submissions: one with every field set and an
// explicit Auto false, one with only what PutPending needs.
func pendingListRecording(t *testing.T) []byte {
	t.Helper()
	f := testFleet(t)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := f.ws.PutPending(PendingSpawn{
		ID: "p-full", Origin: OriginMCP, ClientID: "c1", Title: "Every field",
		RepoID: "r1", Ref: "main", Prompt: "do the thing", Plan: "## Plan",
		Mode: "act", Auto: boolPtr(false),
		CreatedAt: fixed, ExpiresAt: fixed.Add(24 * time.Hour),
		IssueNumber: 7, IssueURL: "https://example.com/issues/7",
	}); err != nil {
		t.Fatalf("PutPending: %v", err)
	}
	if err := f.ws.PutPending(PendingSpawn{ID: "p-minimal"}); err != nil {
		t.Fatalf("PutPending: %v", err)
	}
	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/pending", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/pending = %d %s", rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

func pendingDenyRecording(t *testing.T) []byte {
	t.Helper()
	f := testFleet(t)
	if err := f.ws.PutPending(PendingSpawn{ID: "p-1", Title: "x", RepoID: "r1"}); err != nil {
		t.Fatalf("PutPending: %v", err)
	}
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/pending/p-1/deny", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("deny = %d %s", rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

// agentSpawnRecording spawns on a project and replaces the minted agent id,
// which cannot be deterministic, with <agent>.
func agentSpawnRecording(t *testing.T) []byte {
	t.Helper()
	_, s, root := projectEnv(t)
	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{"project": root, "name": "wire"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn = %d %s", rec.Code, rec.Body)
	}
	return replaceAgentID(t, rec.Body.Bytes())
}

func agentDiscardRecording(t *testing.T) []byte {
	t.Helper()
	_, s, root := projectEnv(t)
	rec := doReq(t, s, http.MethodPost, "/api/agents", map[string]any{"project": root}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("spawn = %d %s", rec.Code, rec.Body)
	}
	var spawned struct {
		AgentID string `json:"agentId"`
	}
	decodeBody(t, rec, &spawned)
	rec = doReq(t, s, http.MethodPost, "/api/agents/"+spawned.AgentID+"/discard", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", rec.Code, rec.Body)
	}
	return rec.Body.Bytes()
}

// pendingApproveRecording approves the submission
// TestPendingSpawnRetainsCanonicalTuplePresence builds, over a real bare repo
// so the spawn itself can succeed, and replaces the minted agent id.
func pendingApproveRecording(t *testing.T) []byte {
	t.Helper()
	f := testFleetWithClient(t, MCPClient{ID: "c1", OwnerID: DefaultOwnerID})
	registerGitRepo(t, f, "r1")
	res, err := f.Submit(ctlContext(t), SpawnRequest{
		Origin: OriginMCP, ClientID: "c1", RepoID: "r1", Title: "x", Prompt: "y",
		Mode: "act", Auto: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/pending/"+res.PendingID+"/approve", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body)
	}
	return replaceAgentID(t, rec.Body.Bytes())
}

// replaceAgentID substitutes the one minted agent id in an answer. Both
// answers name exactly one agent.
func replaceAgentID(t *testing.T, body []byte) []byte {
	t.Helper()
	var out struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AgentID == "" {
		t.Fatalf("answer carries no agent id: %s (%v)", body, err)
	}
	return bytes.Replace(body, []byte(out.AgentID), []byte("<agent>"), 1)
}

// deltaRecordings pins the fleet stream's deltas.
func deltaRecordings() []wireRecording {
	return []wireRecording{
		{name: "delta_activity", record: func(t *testing.T) []byte {
			return emitSampleNotification(t, `{"sessionId":"s-1","update":{"kind":"tool_call","toolName":"file.read"}}`, "")
		}},
		{name: "delta_mode", record: func(t *testing.T) []byte {
			return emitSampleNotification(t,
				`{"sessionId":"s-1","update":{"kind":"mode_changed","mode":"act","auto":true,"system_access":true,"access_revision":3}}`,
				"")
		}},
		{name: "delta_telemetry", record: func(t *testing.T) []byte {
			return emitSampleNotification(t,
				`{"sessionId":"s-1","update":{"kind":"session_telemetry","changedFiles":[{"path":"a.go"},{"path":"b.go"}],"toolStats":[{"name":"file.read","count":2}],"rules":["r1"],"usage":[{"id":1}],"context":{"usedPct":40}}}`,
				"")
		}},
		{name: "delta_watch", record: func(t *testing.T) []byte {
			return emitSampleNotification(t,
				`{"sessionId":"s-1","update":{"kind":"watch","event":{"type":"file_changed","path":"a.go"}}}`, "")
		}},
		{name: "delta_run", record: func(t *testing.T) []byte {
			return emitSampleNotification(t,
				`{"sessionId":"s-1","update":{"kind":"run_progress","run":{"runId":"r-1","status":"running"}}}`, "")
		}},
		{name: "delta_pending", record: func(t *testing.T) []byte {
			f, _, reg := classifierFixture(t)
			reg.emitEvent("s-1", map[string]any{
				"type": "permission_request", "toolCallId": "tc-1",
				"params": json.RawMessage(`{"toolCallId":"tc-1"}`),
			})
			return normalizeDelta(lastDelta(t, f), "")
		}},
		{name: "delta_gate", record: func(t *testing.T) []byte {
			f := testFleet(t)
			f.storeGate("a1", &gateResult{OK: false, FailedCommand: "go test", Output: "boom"})
			return normalizeDelta(lastDelta(t, f), "")
		}},
		{name: "delta_hold", record: func(t *testing.T) []byte {
			f := testFleet(t)
			f.holdCall = func(context.Context, string, bool) error { return nil }
			ok, unsupported := f.callHold(&terminal{agentID: "a1"}, true)
			if !ok || unsupported {
				t.Fatalf("callHold = %v %v, want a hold delta", ok, unsupported)
			}
			return normalizeDelta(lastDelta(t, f), "")
		}},
		{name: "delta_budget_daily", record: func(t *testing.T) []byte {
			return budgetDeltaRecording(t, "daily")
		}},
		{name: "delta_budget_agent", record: func(t *testing.T) []byte {
			return budgetDeltaRecording(t, "agent")
		}},
		{name: "delta_project_removed", record: func(t *testing.T) []byte {
			e, s, root := projectEnv(t)
			rec := doReq(t, s, http.MethodDelete, "/api/projects", map[string]string{"root": root}, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("delete project = %d %s", rec.Code, rec.Body)
			}
			return normalizeDelta(lastDelta(t, e.f), root)
		}},
	}
}

// emitSampleNotification runs one sample notification through the classifier
// and the fleet's emit, as the child's notification hook does.
func emitSampleNotification(t *testing.T, params, root string) []byte {
	t.Helper()
	f := testFleet(t)
	d, ok := classifyNotification("session/update", json.RawMessage(params))
	if !ok {
		t.Fatalf("classifyNotification rejected %s", params)
	}
	f.emit(d)
	return normalizeDelta(lastDelta(t, f), root)
}

// budgetDeltaRecording produces one budget delta of the given scope through
// checkBudgets, with a fixed agent id so the recording is stable.
func budgetDeltaRecording(t *testing.T, scope string) []byte {
	t.Helper()
	h := newBudgetHarness(t, Budgets{})
	h.f.budgets.applyBudgets(Budgets{
		DailyUSD: 1, OnDailyCap: budgetBlock, PerAgentUSD: 1, OnAgentCap: budgetWarn,
	}, h.now)
	h.f.checkBudgets(usageRow("agent-a", 1, h.now, 1.5))
	tail := h.f.fleetLog.Tail(fleetStreamKey)
	for _, ev := range tail {
		var d budgetDelta
		if json.Unmarshal(ev.Data, &d) == nil && d.Kind == "budget" && d.Scope == scope {
			return normalizeDelta(ev.Data, "")
		}
	}
	t.Fatalf("no budget delta of scope %q on the fleet stream", scope)
	return nil
}

// lastDelta is the most recent delta on the fleet stream.
func lastDelta(t *testing.T, f *Fleet) []byte {
	t.Helper()
	tail := f.fleetLog.Tail(fleetStreamKey)
	if len(tail) == 0 {
		t.Fatal("no delta on the fleet stream")
	}
	return tail[len(tail)-1].Data
}

var wireTimeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)

// normalizeDelta replaces the two values a recording cannot fix: the times a
// delta stamps and the project root it names.
func normalizeDelta(data []byte, root string) []byte {
	if root != "" {
		data = bytes.ReplaceAll(data, []byte(root), []byte("<root>"))
	}
	return wireTimeRE.ReplaceAll(data, []byte("<time>"))
}

// encodeWire encodes v exactly as the handlers do.
func encodeWire(v any) []byte {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, v)
	return rec.Body.Bytes()
}

// fillValue returns v with every exported field set deterministically: strings
// are their Go field name, numbers are 1, bools are true, times are fixed, a
// RawMessage is {"k":"v"}, a pointer points at a filled value, a slice holds
// one filled element, a map holds one entry keyed "k".
func fillValue[T any](v T) T {
	fillReflect(reflect.ValueOf(&v).Elem(), reflect.TypeOf(v).Name())
	return v
}

var (
	wireTimeType = reflect.TypeOf(time.Time{})
	wireRawType  = reflect.TypeOf(json.RawMessage(nil))
)

// wireTime is the one timestamp fillValue stamps.
var wireTime = time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)

func fillReflect(v reflect.Value, name string) {
	if !v.CanSet() {
		return
	}
	t := v.Type()
	switch {
	case t == wireTimeType:
		v.Set(reflect.ValueOf(wireTime))
		return
	case t == wireRawType:
		v.Set(reflect.ValueOf(json.RawMessage(`{"k":"v"}`)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		fillReflect(p.Elem(), name)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 1, 1)
		fillReflect(s.Index(0), name)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(t)
		k := reflect.New(t.Key()).Elem()
		if t.Key().Kind() == reflect.String {
			k.SetString("k")
		} else {
			fillReflect(k, "k")
		}
		val := reflect.New(t.Elem()).Elem()
		fillReflect(val, name)
		m.SetMapIndex(k, val)
		v.Set(m)
	case reflect.Struct:
		// An embedded struct is filled the same way as any other.
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported
			}
			fillReflect(v.Field(i), f.Name)
		}
	}
}
