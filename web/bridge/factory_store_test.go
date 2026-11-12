package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func factoryReq() FactoryCreate {
	return FactoryCreate{RequestID: "create", Title: "title", Plan: " plan\n", Spec: "spec", Config: FactoryConfig{RepoID: "repo", Native: true, NoRequiredCIAcknowledged: true}}
}
func TestFactoryStoreReplayAndCommandIdentity(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenFactoryStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	req := factoryReq()
	a, e := s.Create(req, now)
	if e != nil {
		t.Fatal(e)
	}
	cmd := json.RawMessage(`{"kind":"approve","x":1}`)
	b, e := s.Update(a.ID, "approve", 1, cmd, func(i *FactoryItem) error {
		i.Approvals = append(i.Approvals, FactoryApproval{Revision: 1, Plan: i.Draft.Plan, Spec: i.Draft.Spec, PlanHash: FactoryPlanHash(i.Draft.Plan, i.Draft.Spec), Config: i.Draft.Config, ApprovedAt: now})
		i.State = "queued"
		return nil
	}, now)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.Update(a.ID, "approve", 1, json.RawMessage(`{ "x":1, "kind":"approve" }`), nil, now)
	if e != nil || replay.Version != b.Version {
		t.Fatalf("replay: %v %+v", e, replay)
	}
	if _, e = s.Update(a.ID, "approve", 1, json.RawMessage(`{"kind":"stop"}`), nil, now); !errors.Is(e, ErrFactoryConflict) {
		t.Fatal(e)
	}
	if _, e = s.Update(a.ID, "stale", 1, cmd, nil, now); !errors.Is(e, ErrFactoryConflict) {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenFactoryStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, _ := s.Get(a.ID)
	x, _ := json.Marshal(got)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("snapshot changed")
	}
	c, e := s.Create(req, now)
	if e != nil || c.Version != 1 {
		t.Fatal(e)
	}
	got.Approvals[0].Plan = "changed"
	got.Draft.Config.ImplementerRouting = json.RawMessage(`{}`)
	again, _ := s.Get(a.ID)
	if again.Approvals[0].Plan != req.Plan {
		t.Fatal("aliased")
	}
}
func TestFactoryStoreInterruptedAppendAndCorruption(t *testing.T) {
	for _, tail := range []string{"partial", "{}\n", "invalid\n"} {
		t.Run(tail, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := OpenFactoryStore(dir)
			s.Create(factoryReq(), time.Now())
			s.Close()
			p := filepath.Join(dir, "journal.jsonl")
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
			f.WriteString(tail)
			f.Close()
			s, e := OpenFactoryStore(dir)
			if tail == "partial" {
				if e != nil {
					t.Fatal(e)
				}
				s.Close()
			} else if !errors.Is(e, ErrFactoryCorrupt) {
				t.Fatal(e)
			}
		})
	}
	for _, failure := range []string{"write", "sync"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := OpenFactoryStore(t.TempDir())
			defer s.Close()
			a, _ := s.Create(factoryReq(), time.Now())
			if failure == "write" {
				s.write = func([]byte) (int, error) { return 0, os.ErrPermission }
			} else {
				s.sync = func() error { return os.ErrPermission }
			}
			_, e := s.Update(a.ID, "fail", 1, json.RawMessage(`{}`), func(i *FactoryItem) error { i.Title = "bad"; return nil }, time.Now())
			if !errors.Is(e, ErrFactoryUnavailable) {
				t.Fatal(e)
			}
			b, _ := s.Get(a.ID)
			if b.Title != a.Title || b.Version != 1 {
				t.Fatal("published failure")
			}
			if _, e = s.Create(FactoryCreate{}, time.Now()); !errors.Is(e, ErrFactoryUnavailable) {
				t.Fatal(e)
			}
		})
	}
}
func TestFactoryStoreExclusiveOwner(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenFactoryStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenFactoryStore(dir); !errors.Is(e, ErrFactoryLocked) {
		t.Fatal(e)
	}
	s.Close()
	os.WriteFile(filepath.Join(dir, "owner.lock"), []byte("stale"), 0600)
	if _, e = OpenFactoryStore(dir); !errors.Is(e, ErrFactoryLocked) {
		t.Fatal(e)
	}
	dir = t.TempDir()
	s, _ = OpenFactoryStore(dir)
	os.Remove(filepath.Join(dir, "owner.lock"))
	os.WriteFile(filepath.Join(dir, "owner.lock"), []byte("replacement"), 0600)
	s.Close()
	if _, e = os.Stat(filepath.Join(dir, "owner.lock")); e != nil {
		t.Fatal("removed foreign lock")
	}
}
func TestFactoryStoreValidation(t *testing.T) {
	s, _ := OpenFactoryStore(t.TempDir())
	defer s.Close()
	now := time.Now()
	for _, n := range []int{-1, -100} {
		r := factoryReq()
		r.Config.MaxRounds = n
		if _, e := s.Create(r, now); e == nil {
			t.Fatal("accepted negative")
		}
	}
	r := factoryReq()
	r.Plan = strings.Repeat("x", factoryImportLimit+1)
	if _, e := s.Create(r, now); e == nil {
		t.Fatal("accepted oversized plan")
	}
	if len(s.List()) != 0 {
		t.Fatal("invalid write")
	}
	a, e := s.Create(factoryReq(), now)
	if e != nil || a.Draft.Config.MaxRounds != 5 || a.Draft.Config.CIPollSeconds != 15 || a.Draft.Config.CIWaitSeconds != 1800 {
		t.Fatal(e, a)
	}
	_, e = s.Update(a.ID, "huge", 1, json.RawMessage(`"`+strings.Repeat("x", factoryRequestLimit)+`"`), nil, now)
	if e == nil {
		t.Fatal("oversized request")
	}
	_, e = s.Update(a.ID, "record", 1, json.RawMessage(`{}`), func(i *FactoryItem) error {
		i.History = []FactoryEvent{{Message: strings.Repeat("x", factoryRecordLimit)}}
		return nil
	}, now)
	if e == nil {
		t.Fatal("oversized record")
	}
	b, _ := s.Get(a.ID)
	if b.Version != 1 {
		t.Fatal("invalid published")
	}
}

func TestFactoryStoreRejectsCompleteCorruption(t *testing.T) {
	for _, kind := range []string{"checksum", "format", "sequence", "version", "middle"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			s, _ := OpenFactoryStore(dir)
			s.Create(factoryReq(), time.Now())
			s.Close()
			path := filepath.Join(dir, "journal.jsonl")
			line, _ := os.ReadFile(path)
			var r factoryRecord
			json.Unmarshal(line, &r)
			var p factoryPayload
			json.Unmarshal(r.Payload, &p)
			switch kind {
			case "checksum":
				r.Checksum = "wrong"
			case "format":
				p.Format = 99
			case "sequence":
				p.Sequence = 4
			case "version":
				p.Item.Version = 2
			case "middle":
				os.WriteFile(path, append([]byte("partial\n"), line...), 0600)
			}
			if kind != "middle" {
				if kind != "checksum" {
					r.Payload, _ = json.Marshal(p)
					r.Checksum = factoryDigest(r.Payload)
				}
				b, _ := json.Marshal(r)
				os.WriteFile(path, append(b, '\n'), 0600)
			}
			if _, e := OpenFactoryStore(dir); !errors.Is(e, ErrFactoryCorrupt) {
				t.Fatal(e)
			}
		})
	}
}
func TestFactoryStoreInvalidMutationAndCopy(t *testing.T) {
	s, _ := OpenFactoryStore(t.TempDir())
	defer s.Close()
	r := factoryReq()
	r.Config.ImplementerRouting = json.RawMessage(`{"model":"chosen"}`)
	a, _ := s.Create(r, time.Now())
	a.Draft.Config.ImplementerRouting[2] = 'X'
	list := s.List()
	list[0].Draft.Config.ImplementerRouting[2] = 'Y'
	b, _ := s.Get(a.ID)
	if string(b.Draft.Config.ImplementerRouting) != `{"model":"chosen"}` {
		t.Fatal("raw message aliased")
	}
	for _, kind := range []string{"mutator", "identity", "state", "effects"} {
		_, e := s.Update(a.ID, kind, 1, json.RawMessage(`{}`), func(i *FactoryItem) error {
			switch kind {
			case "mutator":
				i.Title = "changed"
				return errors.New("no")
			case "identity":
				i.ID = "other"
			case "state":
				i.State = "unknown"
			case "effects":
				i.Effects = []FactoryEffect{{Status: "prepared"}, {Status: "uncertain"}}
			}
			return nil
		}, time.Now())
		if e == nil {
			t.Fatal("accepted", kind)
		}
	}
	b, _ = s.Get(a.ID)
	if b.Version != 1 || b.Title != r.Title {
		t.Fatal("invalid mutation published")
	}
}
