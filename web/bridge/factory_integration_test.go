package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type deliveryHarness struct {
	t                                         *testing.T
	store                                     *FactoryStore
	fleet                                     *Fleet
	effects                                   *factoryEffects
	controller                                *FactoryController
	bare, base, id, firstHead                 string
	seedHead                                  string
	server                                    *httptest.Server
	created, draft                            bool
	description                               string
	creates, implements, reviews, mutations   int
	loseKind                                  string
	cancelTurn, readyRace                     bool
	forcePRHead, loseCompensation, loseCancel bool
	cancelCalls                               int
	order                                     []string
	prompts                                   []string
	mu                                        sync.Mutex
	activePrompts, maxActivePrompts           int
}

type deliveryAdapter struct{ h *deliveryHarness }

type deliveryForgeState struct {
	creates, mutations int
	draft              bool
	description        string
}

func (h *deliveryHarness) forgeState() deliveryForgeState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return deliveryForgeState{h.creates, h.mutations, h.draft, h.description}
}

func (d deliveryAdapter) Cancel(ctx context.Context, i FactoryItem) error {
	err := d.h.effects.Cancel(ctx, i)
	if err == nil {
		d.h.cancelCalls++
		if d.h.loseCancel {
			return fmt.Errorf("%w: cancel acknowledgement lost", ErrFactoryUncertain)
		}
	}
	return err
}

func (d deliveryAdapter) Observe(ctx context.Context, i FactoryItem) (FactoryObservation, error) {
	return d.h.effects.Observe(ctx, i)
}
func (d deliveryAdapter) Execute(ctx context.Context, i FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	h := d.h
	var out FactoryOutcome
	var err error
	switch e.Kind {
	case "spawn":
		w := h.spawn(e)
		out = FactoryOutcome{EffectID: e.ID, Confirmed: true, Worker: &w}
	case "park", "resume":
		a, _ := h.fleet.ws.Agent(e.Worker.AgentID)
		a.FactoryParked = e.Kind == "park"
		if err := h.fleet.ws.PutAgent(a); err != nil {
			return FactoryOutcome{}, err
		}
		out = FactoryOutcome{EffectID: e.ID, Confirmed: true}
	default:
		out, err = h.effects.Execute(ctx, i, e)
		if e.Kind == "publish" && err == nil && len(i.PRs) == 0 {
			h.seedHead = out.HeadSHA
		}
		if e.Kind == "publish" && err == nil && len(i.PRs) > 0 && h.firstHead == "" {
			h.firstHead = out.HeadSHA
		}
	}
	if err == nil {
		h.order = append(h.order, e.Kind)
	}
	if err == nil && h.loseKind == e.Kind {
		return out, fmt.Errorf("%w: simulated lost %s response", ErrFactoryUncertain, e.Kind)
	}
	return out, err
}
func (h *deliveryHarness) github(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.Contains(r.URL.Path, "/check-runs/") {
		fmt.Fprint(w, `{"output":{"text":"fixture failure"}}`)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/check-runs") {
		parts := strings.Split(r.URL.Path, "/")
		sha := parts[len(parts)-2]
		conclusion := "success"
		if sha == h.firstHead {
			conclusion = "failure"
		}
		fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"id":9,"name":"build","head_sha":%q,"status":"completed","conclusion":%q,"app":{"id":7}}]}`, sha, conclusion)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/statuses") {
		fmt.Fprint(w, "[]")
		return
	}
	if r.URL.Path == "/graphql" {
		var q struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		h.draft = strings.Contains(q.Query, "convertPullRequestToDraft")
		h.mutations++
		if h.loseCompensation && h.draft {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		fmt.Fprintf(w, `{"data":{"mutation":{"pullRequest":{"id":"node","isDraft":%t}}}}`, h.draft)
		return
	}
	if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/pulls/") {
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.description = body.Body
		head := gitText(h.t, h.bare, "rev-parse", "refs/heads/marshal/factory/"+h.id)
		fmt.Fprintf(w, `{"number":1,"html_url":"https://github.com/o/r/pull/1","node_id":"node","state":"open","draft":%t,"head":{"ref":%q,"sha":%q},"base":{"ref":"main","sha":%q}}`, h.draft, "marshal/factory/"+h.id, head, h.base)
		return
	}
	if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls") {
		var body struct {
			Draft bool `json:"draft"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Draft {
			h.t.Error("PR create was not draft")
		}
		h.creates++
		h.created = true
		fmt.Fprint(w, `{"number":1,"html_url":"https://github.com/o/r/pull/1"}`)
		return
	}
	if strings.Contains(r.URL.Path, "/pulls") {
		if !h.created {
			fmt.Fprint(w, "[]")
			return
		}
		head := gitText(h.t, h.bare, "rev-parse", "refs/heads/marshal/factory/"+h.id)
		if h.readyRace && !h.draft || h.forcePRHead {
			head = "external-head"
		}
		p := fmt.Sprintf(`{"number":1,"html_url":"https://github.com/o/r/pull/1","node_id":"node","state":"open","draft":%t,"head":{"ref":%q,"sha":%q},"base":{"ref":"main","sha":%q}}`, h.draft, "marshal/factory/"+h.id, head, h.base)
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			fmt.Fprint(w, "["+p+"]")
		} else {
			fmt.Fprint(w, p)
		}
		return
	}
	http.NotFound(w, r)
}
func newDeliveryHarness(t *testing.T) *deliveryHarness {
	h := &deliveryHarness{t: t, bare: newBareRepoFixture(t), draft: true}
	h.base = gitText(t, h.bare, "rev-parse", "refs/heads/main")
	ws := NewWorkspace(filepath.Join(t.TempDir(), "fleet.json"))
	if err := ws.PutRepo(Repo{ID: "repo", URL: h.bare, Branch: "main", OwnerID: DefaultOwnerID}); err != nil {
		t.Fatal(err)
	}
	h.fleet = NewFleet(ws, "", nil, t.TempDir(), Limits{}, "", nil, "")
	t.Cleanup(func() { h.fleet.Close() })
	var err error
	h.store, err = OpenFactoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.store.Close() })
	h.server = httptest.NewServer(http.HandlerFunc(h.github))
	t.Cleanup(h.server.Close)
	h.effects = NewFactoryEffects(h.fleet).(*factoryEffects)
	h.effects.openForge = func(context.Context, FactoryItem) (Repo, Forge, FactoryForge, Credential, error) {
		g := &githubForge{client: h.server.Client()}
		return Repo{URL: "https://github.com/o/r", APIBase: h.server.URL, Branch: "main"}, g, g, Credential{}, nil
	}
	h.controller = NewFactoryController(h.store, deliveryAdapter{h}, time.Now)
	req := factoryReq()
	req.RequestID = "integration-create"
	req.Plan = "exact plan\n"
	req.Spec = "exact spec\n"
	req.Config.TargetBranch = "main"
	req.Config.ExpectedChecks = []FactoryCheckKey{{Kind: "check_run", Name: "build", AppID: 7}}
	req.Config.NoRequiredCIAcknowledged = false
	i, err := h.store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.id = i.ID
	i, err = h.controller.Command(context.Background(), i.ID, FactoryCommand{RequestID: "approve", Kind: "approve", ExpectedVersion: i.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.store.Update(i.ID, "fixture-base", i.Version, json.RawMessage(`{"kind":"observe_base"}`), func(dst *FactoryItem) error { dst.BaseSHA = h.base; return nil }, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.controller.admitAfterRecovery()
	return h
}
func (h *deliveryHarness) spawn(e FactoryEffect) FactoryWorker {
	t := h.t
	role := e.WorkerRole
	h.assertOneActiveWorker()
	work := filepath.Join(t.TempDir(), role)
	gitText(t, "", "clone", h.bare, work)
	gitText(t, work, "config", "user.email", "fixture@example.com")
	gitText(t, work, "config", "user.name", "Fixture")
	if role == "reviewer" {
		gitText(t, work, "fetch", "origin", "marshal/factory/"+h.id)
		gitText(t, work, "checkout", "--detach", e.ExpectedHead)
	}
	worker := FactoryWorker{AgentID: e.ID, SessionID: "session-" + e.ID, Role: role, SpawnEffectID: e.ID}
	a := Agent{ID: worker.AgentID, SessionID: worker.SessionID, Project: work, SourceKind: "git", SourceRef: "repo", FactoryItemID: h.id, FactoryRole: role, FactorySpawnEffectID: e.ID}
	if err := h.fleet.ws.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	h.assertOneActiveWorker()
	tr := &captureTransport{stackResult: json.RawMessage(`{"nodes":[]}`), results: map[string]json.RawMessage{"session/prompt": json.RawMessage(`{"stopReason":"end_turn"}`)}}
	tr.onRequest = func(method string) error {
		switch method {
		case "session/commit":
			gitText(t, work, "add", "-A")
			gitText(t, work, "commit", "-m", "fixture agent commit")
		case "session/prompt":
			h.assertOneActiveWorker()
			h.mu.Lock()
			h.activePrompts++
			if h.activePrompts > h.maxActivePrompts {
				h.maxActivePrompts = h.activePrompts
			}
			h.mu.Unlock()
			defer func() { h.mu.Lock(); h.activePrompts--; h.mu.Unlock() }()
			tr.mu.Lock()
			params := tr.seen[len(tr.seen)-1].params
			tr.mu.Unlock()
			h.prompts = append(h.prompts, role+":"+params)
			if h.cancelTurn && role == "implementer" {
				tr.results["session/prompt"] = json.RawMessage(`{"stopReason":"cancelled"}`)
			}
			i, _ := h.store.Get(h.id)
			fx := i.Effects[len(i.Effects)-1]
			report := ""
			if role == "implementer" {
				h.implements++
				if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte(fmt.Sprintf("revision %d\n", h.implements)), 0644); err != nil {
					return err
				}
				report = fmt.Sprintf(`{"operationId":%q,"outcome":"complete","summary":"implemented","narration":{"issue":"fixture issue","approach":"fixture approach","verification":"fixture verification"}}`, fx.ID)
			} else {
				h.reviews++
				findings := "[]"
				if h.reviews == 1 {
					findings = `[{"title":"repair feature","severity":"blocking"}]`
				}
				report = fmt.Sprintf(`{"operationId":%q,"outcome":"complete","summary":"reviewed","inputsAccessible":true,"baseSha":%q,"headSha":%q,"findings":%s}`, fx.ID, fx.ExpectedBase, fx.ExpectedHead, findings)
			}
			raw, _ := json.Marshal(map[string]any{"nodes": []any{map[string]any{"id": fx.ID, "kind": "final", "message": map[string]any{"content": "```json\n" + report + "\n```"}}}})
			tr.stackResult = raw
		}
		return nil
	}
	child := &Child{Transport: tr}
	reg := NewRegistry(child)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(child.Stop)
	reg.track(worker.SessionID, AgentPath(work))
	rt := &agentRuntime{id: worker.AgentID, root: work, sessionID: worker.SessionID, child: child, reg: reg, sourceKind: "git"}
	h.fleet.runtimes[worker.AgentID] = rt
	h.fleet.sessionAgent[worker.SessionID] = worker.AgentID
	return worker
}

func (h *deliveryHarness) assertOneActiveWorker() {
	h.t.Helper()
	active := 0
	for _, a := range h.fleet.ws.Agents() {
		if a.FactoryItemID == h.id && !a.FactoryParked {
			active++
		}
	}
	if active > 1 {
		h.t.Fatalf("%d factory model workers active together", active)
	}
}
func (h *deliveryHarness) tick() FactoryItem {
	h.t.Helper()
	if err := h.controller.tick(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	i, err := h.store.Get(h.id)
	if err != nil {
		h.t.Fatal(err)
	}
	return i
}
func TestFactoryEndToEnd(t *testing.T) {
	h := newDeliveryHarness(t)
	var i FactoryItem
	for n := 0; n < 80; n++ {
		i = h.tick()
		if i.State == "ready" || i.Decision != nil {
			break
		}
		if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if i.State != "ready" {
		t.Fatalf("state=%s decision=%+v last=%+v", i.State, i.Decision, factoryLastEffect(i))
	}
	forge := h.forgeState()
	if forge.creates != 1 || h.implements != 2 || h.reviews != 2 || forge.mutations != 1 || len(i.PRs) != 1 || len(i.Rounds) != 2 {
		t.Fatalf("creates=%d implement=%d review=%d mutations=%d PRs=%d rounds=%d", forge.creates, h.implements, h.reviews, forge.mutations, len(i.PRs), len(i.Rounds))
	}
	for name, want := range map[string]string{"plan.md": i.Approvals[0].Plan, "spec.md": i.Approvals[0].Spec} {
		cmd := exec.Command("git", "show", h.seedHead+":docs/marshal-factory/"+i.ID+"/"+name)
		cmd.Dir = h.bare
		got, err := cmd.Output()
		if err != nil || string(got) != want {
			t.Fatalf("approved seed %s changed: %q, %v", name, got, err)
		}
	}
	seed, draft, implement := -1, -1, -1
	for n, kind := range h.order {
		if kind == "publish" && seed < 0 {
			seed = n
		}
		if kind == "create_pr" {
			draft = n
		}
		if kind == "implement" && implement < 0 {
			implement = n
		}
	}
	if seed < 0 || draft <= seed || implement <= draft {
		t.Fatalf("seed/draft/implementation order: %v", h.order)
	}
	if h.maxActivePrompts != 1 {
		t.Fatalf("model prompt concurrency = %d", h.maxActivePrompts)
	}
	var repairPrompt string
	for _, prompt := range h.prompts {
		if strings.HasPrefix(prompt, "implementer:") {
			repairPrompt = prompt
		}
	}
	if !strings.Contains(repairPrompt, "fixture failure") || !strings.Contains(repairPrompt, "repair feature") {
		t.Fatalf("repair omitted CI or review evidence: %s", repairPrompt)
	}
	if i.Rounds[0].CI == nil || i.Rounds[0].CI.Checks[0].Conclusion != "failure" || i.Rounds[1].CI == nil || i.Rounds[1].CI.Checks[0].Conclusion != "success" || i.Rounds[0].HeadSHA != h.firstHead || i.Rounds[1].HeadSHA != gitText(t, h.bare, "rev-parse", "refs/heads/"+i.Branch) {
		t.Fatal("CI and round SHAs do not match published commits")
	}
	if i.Rounds[0].HeadSHA == i.Rounds[1].HeadSHA {
		t.Fatal("repair did not produce a distinct published head")
	}
	for _, round := range i.Rounds {
		if round.CI.HeadSHA != round.HeadSHA || len(round.CI.Checks) == 0 {
			t.Fatalf("round CI revision mismatch: %+v", round)
		}
		for _, check := range round.CI.Checks {
			if check.SHA != round.HeadSHA {
				t.Fatalf("check revision %q != published %q", check.SHA, round.HeadSHA)
			}
		}
	}
	if i.PRs[0].State != "open" || i.PRs[0].Draft {
		t.Fatal("PR not left ready for human merge")
	}
	if !strings.Contains(forge.description, "## CI") || !strings.Contains(forge.description, "fixture issue") || !strings.Contains(forge.description, "fixture approach") {
		t.Fatalf("published description does not carry the newest narration: %q", forge.description)
	}
	for _, e := range i.Effects {
		if e.Status != "completed" {
			t.Fatalf("effect unsettled: %+v", e)
		}
	}
	for _, r := range i.Rounds {
		if r.Review == nil || r.Review.HeadSHA != r.HeadSHA || r.Review.BaseSHA != r.BaseSHA || r.Implementation == nil || r.ImplementEffectID != r.Implementation.OperationID {
			t.Fatalf("round evidence mismatch: %+v", r)
		}
	}
	if len(i.Rounds[0].Review.Findings) != 1 || i.Rounds[0].Review.Findings[0].Title != "repair feature" || len(i.Rounds[1].Review.Findings) != 0 {
		t.Fatal("review finding did not drive a clean repair review")
	}
	h.fleet.SetFactoryController(h.controller)
	if _, err := h.fleet.Merge(context.Background(), i.Workers[0].AgentID, ""); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("independent merge claimed factory worker: %v", err)
	}
	if err := h.fleet.StartRun(context.Background(), i.Workers[0].AgentID, RunRequest{}); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("independent run claimed factory worker: %v", err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFactoryStore(filepath.Dir(h.store.lockPath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Get(i.ID)
	if err != nil || len(persisted.Rounds) != 2 || len(persisted.History) == 0 || persisted.State != "ready" {
		t.Fatalf("journal lost delivery history: %+v %v", persisted, err)
	}
}
func TestFactoryEndToEndInterrupted(t *testing.T) {
	// Each scenario owns its repositories, server, store and fleet. Run them
	// concurrently while preserving the real Git and recovery assertions.
	t.Run("lost cancellation acknowledgement is not replayed", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		h.loseCancel = true
		i := h.tick()
		if len(i.Workers) != 1 {
			t.Fatalf("spawn did not complete: %+v", i.Workers)
		}
		cmd := FactoryCommand{RequestID: "stop-lost", Kind: "stop", ExpectedVersion: i.Version}
		if _, err := h.controller.Command(context.Background(), i.ID, cmd); err != nil {
			t.Fatal(err)
		}
		if _, err := h.controller.Command(context.Background(), i.ID, cmd); err != nil {
			t.Fatal(err)
		}
		latest, _ := h.store.Get(i.ID)
		if h.cancelCalls != 1 {
			t.Fatalf("cancel dispatched %d times", h.cancelCalls)
		}
		found := false
		for _, event := range latest.History {
			if event.Kind == "cancel_uncertain" {
				found = true
			}
		}
		if !found {
			t.Fatal("lost cancellation acknowledgement not journaled")
		}
	})
	t.Run("lost set draft response is not replayed", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		var i FactoryItem
		for n := 0; n < 80; n++ {
			i = h.tick()
			if i.State == "ready" || i.Decision != nil {
				break
			}
			if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if i.State != "ready" {
			t.Fatalf("fixture never ready: %+v", i.Decision)
		}
		h.mu.Lock()
		h.forcePRHead = true
		h.mu.Unlock()
		h.loseKind = "set_draft"
		h.controller.monitorReadyOnce(context.Background())
		i = h.tick()
		last := factoryLastEffect(i)
		forge := h.forgeState()
		if last == nil || last.Kind != "set_draft" || last.Status != "uncertain" || forge.mutations != 2 || !forge.draft {
			t.Fatalf("lost set draft: state=%s effect=%+v mutations=%d draft=%t", i.State, last, forge.mutations, forge.draft)
		}
		for n := 0; n < 3; n++ {
			h.tick()
		}
		if h.forgeState().mutations != 2 {
			t.Fatal("set draft mutation replayed")
		}
	})
	t.Run("lost ready compensation response remains uncertain", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		h.mu.Lock()
		h.readyRace = true
		h.loseCompensation = true
		h.mu.Unlock()
		var i FactoryItem
		for n := 0; n < 80; n++ {
			i = h.tick()
			if i.Decision != nil || i.State == "ready" {
				break
			}
			if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
				time.Sleep(5 * time.Millisecond)
			}
		}
		last := factoryLastEffect(i)
		forge := h.forgeState()
		if i.State == "ready" || last == nil || last.Kind != "set_ready" || last.Status != "uncertain" || forge.mutations != 2 || !forge.draft {
			t.Fatalf("lost compensation: state=%s effect=%+v mutations=%d draft=%t", i.State, last, forge.mutations, forge.draft)
		}
		for n := 0; n < 3; n++ {
			h.tick()
		}
		if h.forgeState().mutations != 2 {
			t.Fatal("ready or compensation replayed")
		}
	})
	t.Run("journal failure stops dispatch", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		h.store.write = func([]byte) (int, error) { return 0, os.ErrPermission }
		if err := h.controller.tick(context.Background()); err == nil {
			t.Fatal("journal failure accepted")
		}
		if h.forgeState().creates != 0 || h.implements != 0 || len(h.store.List()[0].Effects) != 0 {
			t.Fatal("effect ran after failed journal append")
		}
	})
	t.Run("cancelled nil-error ACP reply cannot publish", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		h.cancelTurn = true
		var i FactoryItem
		for n := 0; n < 25; n++ {
			i = h.tick()
			if i.Decision != nil {
				break
			}
			if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if i.Decision == nil || h.implements != 1 || h.firstHead != "" {
			t.Fatalf("cancelled turn published: decision=%+v prompts=%d head=%q", i.Decision, h.implements, h.firstHead)
		}
	})
	t.Run("ready race returns PR to draft", func(t *testing.T) {
		t.Parallel()
		h := newDeliveryHarness(t)
		h.mu.Lock()
		h.readyRace = true
		h.mu.Unlock()
		var i FactoryItem
		for n := 0; n < 80; n++ {
			i = h.tick()
			if i.Decision != nil || i.State == "ready" {
				break
			}
			if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
				time.Sleep(5 * time.Millisecond)
			}
		}
		forge := h.forgeState()
		if i.State == "ready" || i.Decision == nil || forge.mutations != 2 || !forge.draft {
			t.Fatalf("ready race state=%s decision=%+v mutations=%d draft=%t", i.State, i.Decision, forge.mutations, forge.draft)
		}
	})
	for _, kind := range []string{"spawn", "seed", "publish", "create_pr", "implement", "park", "review", "resume", "set_ready"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			h := newDeliveryHarness(t)
			h.loseKind = kind
			var i FactoryItem
			for n := 0; n < 80; n++ {
				i = h.tick()
				if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "uncertain" {
					break
				}
				if i.Decision != nil && i.Decision.Kind != "recovery" {
					t.Fatalf("before interruption: %+v", i.Decision)
				}
				if len(i.Effects) > 0 && i.Effects[len(i.Effects)-1].Status == "dispatched" {
					time.Sleep(5 * time.Millisecond)
				}
			}
			last := factoryLastEffect(i)
			if last == nil || last.Kind != kind || last.Status != "uncertain" {
				t.Fatalf("did not interrupt %s: state=%s decision=%+v last=%+v", kind, i.State, i.Decision, last)
			}
			forge := h.forgeState()
			before := []int{forge.creates, h.implements, h.reviews, forge.mutations, len(i.Effects)}
			for n := 0; n < 3; n++ {
				h.tick()
			}
			after, _ := h.store.Get(h.id)
			forge = h.forgeState()
			if forge.creates != before[0] || h.implements != before[1] || h.reviews != before[2] || forge.mutations != before[3] || len(after.Effects) != before[4] {
				t.Fatalf("blind replay of %s: before=%v after=%v", kind, before, []int{forge.creates, h.implements, h.reviews, forge.mutations, len(after.Effects)})
			}
		})
	}
}
