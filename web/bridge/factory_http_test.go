package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFactoryHTTP(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fleet := NewFleet(NewWorkspace(t.TempDir()), "", nil, t.TempDir(), Limits{}, "", nil, "")
	defer fleet.Close()
	srv := NewServer(fleet, "secret")
	srv.factoryRoutes(NewFactoryAPI(store, nil, fleet))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	if got := request("GET", "/api/factory/items", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("auth %d", got.Code)
	}
	if got := request("POST", "/api/factory/items", `{"title":"x"}`, "secret"); got.Code != http.StatusBadRequest {
		t.Fatalf("identity %d: %s", got.Code, got.Body.String())
	}
	if got := request("POST", "/api/factory/items", `{"requestId":"x","title":"x","plan":"p","config":{"repoId":"missing"}}`, "secret"); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repo %d: %s", got.Code, got.Body.String())
	}
	if got := request("POST", "/api/factory/items", strings.Repeat("x", factoryRequestLimit+1), "secret"); got.Code != http.StatusBadRequest {
		t.Fatalf("body limit %d", got.Code)
	}
	var list struct {
		Items []FactoryItem `json:"items"`
	}
	got := request("GET", "/api/factory/items", "", "secret")
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &list) != nil || len(list.Items) != 0 {
		t.Fatalf("list %d: %s", got.Code, got.Body.String())
	}
}

func TestFactoryHTTPCreateApprovalAndReplay(t *testing.T) {
	agent := newFakeAgent()
	fleet := ctlFleet(t, agent)
	root := t.TempDir()
	if err := fleet.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	if err := fleet.ws.PutRepo(Repo{ID: "repo", URL: "https://example.invalid/repo.git", Branch: "main", OwnerID: DefaultOwnerID, Forge: "github", CredRef: "test-pat"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_TEST_PAT", "fixture-only")
	fleet.creds = NewCredentialStore([]Credential{{ID: "test-pat", Kind: "pat", OwnerID: DefaultOwnerID, EnvVar: "FACTORY_TEST_PAT"}})
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"test": map[string]any{"type": "openai_compatible", "hasKey": true}}, "presets": map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}, "defaultProfile": "chosen"}
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	controller := NewFactoryController(store, nil, nil)
	srv := NewServer(fleet, "secret", "", NewFactoryAPI(store, controller, fleet))
	req := FactoryCreate{RequestID: "create-1", Title: "Task", Plan: "build", Config: FactoryConfig{RepoID: "repo", ProjectRoot: root, TargetBranch: "main", Native: true, ImplementerRouting: json.RawMessage(`{"profile":"chosen"}`), ReviewerRouting: json.RawMessage(`{"profile":"chosen"}`)}}
	post := func(v any, path string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	created := post(req, "/api/factory/items")
	if created.Code != 201 {
		t.Fatalf("create %d: %s", created.Code, created.Body.String())
	}
	var result struct {
		Item FactoryItem `json:"item"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Item.State != "draft" || result.Item.Draft.Config.ImplementerAuto {
		t.Fatalf("preview started or auto selected: %+v", result.Item)
	}
	if replay := post(req, "/api/factory/items"); replay.Code != 200 {
		t.Fatalf("replay %d: %s", replay.Code, replay.Body.String())
	}
	changed := req
	changed.Title = "different"
	if conflict := post(changed, "/api/factory/items"); conflict.Code != 409 {
		t.Fatalf("request identity conflict %d: %s", conflict.Code, conflict.Body.String())
	}
	stale := post(FactoryCommand{RequestID: "stale", Kind: "stop", ExpectedVersion: 2}, "/api/factory/items/"+result.Item.ID+"/commands")
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), `"currentVersion":1`) {
		t.Fatalf("version conflict %d: %s", stale.Code, stale.Body.String())
	}
	if bad := post(FactoryCommand{RequestID: "approve-1", Kind: "approve", ExpectedVersion: 1}, "/api/factory/items/"+result.Item.ID+"/commands"); bad.Code != 422 {
		t.Fatalf("CI acknowledgement %d: %s", bad.Code, bad.Body.String())
	}
	if latest, err := store.Get(result.Item.ID); err != nil || latest.Version != 1 {
		t.Fatalf("invalid approval changed journal: %v %+v", err, latest)
	}
	approved := req
	approved.RequestID = "create-2"
	approved.Config.NoRequiredCIAcknowledged = true
	approved.Config.ImplementerAuto = true
	second := post(approved, "/api/factory/items")
	if second.Code != 201 || json.Unmarshal(second.Body.Bytes(), &result) != nil {
		t.Fatalf("second create %d: %s", second.Code, second.Body.String())
	}
	command := FactoryCommand{RequestID: "approve-2", Kind: "approve", ExpectedVersion: 1}
	commandBody, _ := json.Marshal(command)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	acceptedRequest := httptest.NewRequest("POST", "/api/factory/items/"+result.Item.ID+"/commands", bytes.NewReader(commandBody)).WithContext(requestCtx)
	acceptedRequest.Header.Set("Authorization", "Bearer secret")
	accepted := httptest.NewRecorder()
	srv.ServeHTTP(accepted, acceptedRequest)
	cancelRequest()
	if accepted.Code != 200 || json.Unmarshal(accepted.Body.Bytes(), &result) != nil || result.Item.State != "queued" || !result.Item.Approvals[0].Config.ImplementerAuto {
		t.Fatalf("approve %d: %s", accepted.Code, accepted.Body.String())
	}
	if replay := post(command, "/api/factory/items/"+result.Item.ID+"/commands"); replay.Code != 200 {
		t.Fatalf("command replay %d: %s", replay.Code, replay.Body.String())
	}
	invalidModel := req
	invalidModel.RequestID = "bad-model"
	invalidModel.Config.ImplementerRouting = json.RawMessage(`{"profile":"missing"}`)
	if bad := post(invalidModel, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("model %d: %s", bad.Code, bad.Body.String())
	}
	invalidEnvironment := req
	invalidEnvironment.RequestID = "bad-environment"
	invalidEnvironment.Config.Native = false
	invalidEnvironment.Config.Workspace = "unbuilt"
	if bad := post(invalidEnvironment, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("environment %d: %s", bad.Code, bad.Body.String())
	}
	if _, err := fleet.templates.Create("pending", []byte("fixture"), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := fleet.templates.Publish("pending", "test"); err != nil {
		t.Fatal(err)
	}
	agent.results["workspace/parse"] = map[string]any{"doc": map[string]any{"workspace": map[string]any{"name": "pending"}}}
	invalidEnvironment.RequestID = "pending-image"
	invalidEnvironment.Config.Workspace = "pending"
	if bad := post(invalidEnvironment, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("pending build %d: %s", bad.Code, bad.Body.String())
	}
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"test": map[string]any{"type": "anthropic", "hasKey": false}}, "presets": map[string]any{"test/model": map[string]any{"provider": "test", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}}
	missingKey := req
	missingKey.RequestID = "missing-key"
	if bad := post(missingKey, "/api/factory/items"); bad.Code != 422 {
		t.Fatalf("missing provider key %d: %s", bad.Code, bad.Body.String())
	}
	agent.results["config/get"] = map[string]any{"providers": map[string]any{"oauth": map[string]any{"type": "openai_codex", "auth": "oauth", "apiKeyEnv": "FACTORY_STALE_OPTIONAL_KEY", "hasKey": false}}, "presets": map[string]any{"test/model": map[string]any{"provider": "oauth", "model": "model"}}, "profiles": map[string]any{"chosen": map[string]any{"implementer": map[string]any{"preset": "test/model"}, "reviewer": map[string]any{"preset": "test/model"}}}}
	oauth := req
	oauth.RequestID = "oauth-with-stale-env"
	if accepted := post(oauth, "/api/factory/items"); accepted.Code != 201 {
		t.Fatalf("OAuth route with optional stale key %d: %s", accepted.Code, accepted.Body.String())
	}
	if strings.Contains(created.Body.String(), "fixture-only") || strings.Contains(accepted.Body.String(), "fixture-only") {
		t.Fatal("credential value leaked in response")
	}
	// The accepted approval belongs to the process controller. Closing the
	// request above must not cancel its later dispatch.
	effects := &httpContinuationEffects{started: make(chan context.Context, 1), release: make(chan struct{})}
	processController := NewFactoryController(store, effects, nil)
	if err := processController.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	processCtx, cancelProcess := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- processController.Run(processCtx) }()
	select {
	case effectCtx := <-effects.started:
		select {
		case <-effectCtx.Done():
			t.Fatal("request cancellation stopped accepted work")
		default:
		}
	case <-time.After(time.Second):
		t.Fatal("accepted work was not dispatched")
	}
	if err := processController.Quiesce(); err != nil {
		t.Fatal(err)
	}
	close(effects.release)
	cancelProcess()
	<-done
}

type httpContinuationEffects struct {
	started chan context.Context
	release chan struct{}
}

func (f *httpContinuationEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return FactoryObservation{BaseSHA: "base", RuntimeKnown: true, TurnIdle: true, ActiveWorkKnown: true, WorkIdle: true}, nil
}
func (f *httpContinuationEffects) Execute(ctx context.Context, _ FactoryItem, e FactoryEffect) (FactoryOutcome, error) {
	select {
	case f.started <- ctx:
	default:
	}
	<-f.release
	return FactoryOutcome{EffectID: e.ID, Confirmed: true}, nil
}

func TestFactoryStartupQuiescePersistsUncertainty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "factory")
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := factoryReq()
	item, err := store.Create(req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item, err = store.Update(item.ID, "effect", item.Version, json.RawMessage(`{"effect":true}`), func(i *FactoryItem) error {
		i.State = "implementing"
		i.Effects = []FactoryEffect{{ID: "effect-1", Kind: "implement", Status: "dispatched"}}
		return nil
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	controller := NewFactoryController(store, nil, nil)
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if controller.isReady() {
		t.Fatal("dispatch admitted after shutdown")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recovered, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Effects) != 1 || recovered.Effects[0].Status != "uncertain" || recovered.Decision == nil || recovered.Decision.Kind != "recovery" {
		t.Fatalf("shutdown evidence lost: %+v", recovered)
	}
	if _, err = store.Create(FactoryCreate{RequestID: "effect"}, time.Now()); !errors.Is(err, ErrFactoryConflict) {
		t.Fatalf("journal request identity lost: %v", err)
	}
}

func TestFactoryStartupQuiesceConcurrentFinish(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "factory")
	store, err := OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	effects := &blockedLoopEffects{started: make(chan struct{}, 2), release: make(chan struct{}), cancelled: make(chan struct{}, 1)}
	controller := NewFactoryController(store, effects, nil)
	for _, id := range []string{"first", "second"} {
		item := blockedFactoryItem(t, store, id)
		if err := controller.dispatch(context.Background(), item, item.Effects[len(item.Effects)-1]); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 2; n++ {
		select {
		case <-effects.started:
		case <-time.After(time.Second):
			t.Fatal("effect did not start")
		}
	}
	close(effects.release)
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	controller.active.Wait()
	store, err = OpenFactoryStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, item := range store.List() {
		status := item.Effects[len(item.Effects)-1].Status
		if status == "dispatched" {
			t.Fatalf("unsettled effect after shutdown: %s", item.ID)
		}
	}
}

func TestFactoryStartupQuiesceBeforeExecution(t *testing.T) {
	store, err := OpenFactoryStore(filepath.Join(t.TempDir(), "factory"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := blockedFactoryItem(t, store, "boundary")
	effects := &dispatchBoundaryEffects{executed: make(chan struct{}, 1)}
	controller := NewFactoryController(store, effects, nil)
	paused := make(chan struct{})
	release := make(chan struct{})
	controller.beforeExecute = func() { close(paused); <-release }
	done := make(chan error, 1)
	go func() { done <- controller.dispatch(context.Background(), item, item.Effects[len(item.Effects)-1]) }()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not reach admission boundary")
	}
	if err := controller.Quiesce(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-effects.executed:
		t.Fatal("effect executed after shutdown marked it uncertain")
	default:
	}
	got, err := store.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Effects[len(got.Effects)-1].Status != "uncertain" {
		t.Fatalf("effect status %q", got.Effects[len(got.Effects)-1].Status)
	}
}

type dispatchBoundaryEffects struct{ executed chan struct{} }

func (f *dispatchBoundaryEffects) Observe(context.Context, FactoryItem) (FactoryObservation, error) {
	return FactoryObservation{}, nil
}
func (f *dispatchBoundaryEffects) Execute(context.Context, FactoryItem, FactoryEffect) (FactoryOutcome, error) {
	f.executed <- struct{}{}
	return FactoryOutcome{}, nil
}
