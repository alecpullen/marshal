package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/credentials"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
	"marshal/internal/tools/mcp"
	"marshal/internal/trust"
)

// settingsSession is one session a settings agent serves: the service pair over
// its own state, the user config file its host writes, and a reloader that
// re-reads the layers from the same place.
type settingsSession struct {
	id      string
	state   *session.State
	sess    *service.Session
	host    *service.Host
	auth    *settingsAuth
	home    string
	work    string
	user    string
	reloads atomic.Int32
}

// newSettingsSession builds one session's service pair: the state over a fresh
// working directory carrying the default config, a user config under its own
// temporary home, and a ReloadLayers that re-reads the layers from it. extra
// adjusts the deps before the pair is built.
func newSettingsSession(t *testing.T, id string, extra func(*service.Deps)) *settingsSession {
	t.Helper()
	s := &settingsSession{
		id:   id,
		home: t.TempDir(),
		work: t.TempDir(),
		auth: newSettingsAuth(),
	}
	s.user = config.UserConfigPath(s.home)
	s.state = session.New(config.Default(), s.work, time.Unix(100, 0), session.Persistence{SessionID: id})
	deps := service.Deps{
		State:          s.state,
		Auth:           s.auth,
		HomeDir:        s.home,
		UserConfigPath: s.user,
		ReloadLayers: func() (config.Layers, bool) {
			layers, err := config.LoadLayers(config.LoadOptions{
				HomeDir:           s.home,
				WorkingDir:        s.work,
				SkipProjectConfig: true,
			})
			if err != nil {
				return config.Layers{}, false
			}
			s.reloads.Add(1)
			return layers, true
		},
	}
	if extra != nil {
		extra(&deps)
	}
	s.host, s.sess = service.New(deps)
	return s
}

// settingsAgent serves one connection's settings handlers over a pipe, to a raw
// client connection that declared the marshal capability or did not.
type settingsAgent struct {
	t        *testing.T
	conn     *rpc.Conn
	srv      *Server
	settings *SettingsManager
	sessions map[string]*settingsSession

	// lookup is what the managers' Lookup reports for every session.
	lookup atomic.Bool

	mu      sync.Mutex
	updates []recordedConfig
}

// recordedConfig is one session_config update the client received.
type recordedConfig struct {
	sessionID string
	config    acpwire.SessionConfig
}

// recordConfig keeps one received session_config update.
func (a *settingsAgent) recordConfig(sessionID string, cfg acpwire.SessionConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, recordedConfig{sessionID: sessionID, config: cfg})
}

// configs returns the session_config updates received so far, in arrival order.
func (a *settingsAgent) configs() []recordedConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]recordedConfig, len(a.updates))
	copy(out, a.updates)
	return out
}

// configCount is how many session_config updates the client has received.
func (a *settingsAgent) configCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.updates)
}

// notifyClient decodes a notification the agent sent, keeping each
// session_config update.
func (a *settingsAgent) notifyClient(method string, params json.RawMessage) {
	if method != acpwire.MethodSessionUpdate {
		return
	}
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil {
		return
	}
	var peek struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(env.Update, &peek); err != nil {
		return
	}
	if peek.Kind != acpwire.UpdateSessionConfig {
		return
	}
	var u acpwire.SessionConfigUpdate
	if err := json.Unmarshal(env.Update, &u); err != nil {
		return
	}
	a.recordConfig(env.SessionID, u.Config)
}

// startSettingsAgent serves a SettingsManager and a StatusManager on a pipe,
// with the real handshake, and returns the client's end. others are the handlers
// config/probe_provider and config/set_provider_key fall back to for a client
// without the capability.
func startSettingsAgent(t *testing.T, marshal bool, sessions []*settingsSession, others map[string]Handler) *settingsAgent {
	t.Helper()
	return startSettingsAgentDelay(t, 0, marshal, sessions, others)
}

// startSettingsAgentDelay is startSettingsAgent with every session/update
// frame slowed, so a call that returns before its update is written shows up
// as an ordering failure.
func startSettingsAgentDelay(t *testing.T, delay time.Duration, marshal bool, sessions []*settingsSession, others map[string]Handler) *settingsAgent {
	t.Helper()
	a := &settingsAgent{t: t, sessions: map[string]*settingsSession{}}
	a.lookup.Store(true)
	for _, s := range sessions {
		a.sessions[s.id] = s
	}
	lookup := func(id string) (*service.Session, bool) {
		s, ok := a.sessions[id]
		if !ok || !a.lookup.Load() {
			return nil, false
		}
		return s.sess, true
	}
	hostLookup := func(id string) (*service.Host, bool) {
		s, ok := a.sessions[id]
		if !ok || !a.lookup.Load() {
			return nil, false
		}
		return s.host, true
	}

	client := startTestAgent(t, func(srv *Server) {
		a.srv = srv
		registerInitialize(srv)
		notify := func(method string, params any) error {
			if delay > 0 && method == acpwire.MethodSessionUpdate {
				time.Sleep(delay)
			}
			return srv.Notify(method, params)
		}
		statuses := NewStatusManager(StatusManagerConfig{
			Lookup:  lookup,
			Notify:  notify,
			Marshal: srv.marshalClient,
		})
		registerStatus(srv, statuses)
		settings := NewSettingsManager(SettingsManagerConfig{
			Lookup:     lookup,
			HostLookup: hostLookup,
			Notify:     notify,
			Marshal:    srv.marshalClient,
		})
		registerSettings(srv, settings, others)
		srv.SetResponseBarrier(responseBarrier(statuses, settings))
		a.settings = settings
	})

	a.conn = rpc.NewConn(client, rpc.Handlers{Notify: a.notifyClient})
	t.Cleanup(func() {
		_ = a.conn.Close()
		if a.settings != nil {
			a.settings.Close()
		}
	})

	p := acpwire.InitializeParams{ProtocolVersion: acpwire.MarshalProtocolVersion}
	if marshal {
		p.ClientCapabilities = &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		}
	}
	var res acpwire.InitializeResult
	if err := a.conn.Call(context.Background(), "initialize", p, &res); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return a
}

// call sends one settings call and decodes its result into result when it is
// non-nil.
func (a *settingsAgent) call(method string, params, result any) error {
	a.t.Helper()
	return a.conn.Call(context.Background(), method, params, result)
}

// mustCall is call for a call that has to succeed.
func (a *settingsAgent) mustCall(method string, params, result any) {
	a.t.Helper()
	if err := a.call(method, params, result); err != nil {
		a.t.Fatalf("%s: %v", method, err)
	}
}

// rpcCode is the JSON-RPC code an error crossed the pipe with.
func rpcCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded, want an error")
	}
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want a JSON-RPC error", err)
	}
	return rpcErr.Code
}

// loadSettingsUserConfig reads the user config a write under home produced.
func loadSettingsUserConfig(t *testing.T, s *settingsSession) config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{
		HomeDir:           s.home,
		WorkingDir:        s.work,
		SkipProjectConfig: true,
	})
	if err != nil {
		t.Fatalf("load user config: %v", err)
	}
	return cfg
}

// TestHostCallsResolveTheNamedSessionsHost pins decision 1: a host call acts
// through the named session's runtime, so the write lands in that session's
// user config and refreshes that session's layers and no other's.
func TestHostCallsResolveTheNamedSessionsHost(t *testing.T) {
	s1 := newSettingsSession(t, "s1", nil)
	s2 := newSettingsSession(t, "s2", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s1, s2}, nil)

	a.mustCall(acpwire.MethodSetValue, acpwire.SetValueParams{
		SessionID: "s1",
		Path:      "project.name",
		Value:     json.RawMessage(`"named-session"`),
	}, nil)

	if got := loadSettingsUserConfig(t, s1).Project.Name; got != "named-session" {
		t.Fatalf("s1's user config project.name = %q, want %q", got, "named-session")
	}
	if got := s1.sess.Layers().Merged.Project.Name; got != "named-session" {
		t.Fatalf("s1's layers project.name = %q, want the write to refresh them", got)
	}
	if s1.reloads.Load() == 0 {
		t.Fatal("s1's layers were never reloaded")
	}
	if n := s2.reloads.Load(); n != 0 {
		t.Fatalf("s2's layers were reloaded %d times, want 0: only the named session served the call", n)
	}
	if _, err := os.Stat(s2.user); !os.IsNotExist(err) {
		t.Fatalf("s2's user config exists (stat err = %v): the write went to the named session only", err)
	}

	err := a.call(acpwire.MethodSetValue, acpwire.SetValueParams{
		Path:  "project.name",
		Value: json.RawMessage(`"anonymous"`),
	}, nil)
	if code := rpcCode(t, err); code != invalidParams {
		t.Fatalf("missing sessionId: code = %d, want %d", code, invalidParams)
	}
	err = a.call(acpwire.MethodSetValue, acpwire.SetValueParams{
		SessionID: "nope",
		Path:      "project.name",
		Value:     json.RawMessage(`"nobody"`),
	}, nil)
	if code := rpcCode(t, err); code != serverError {
		t.Fatalf("unknown session: code = %d, want %d", code, serverError)
	}
}

// TestSetValueKeepsTheFieldsType pins decision 5: the agent decodes the value
// into the config field's own type, so the file receives that type rather than
// whatever a JSON decode into `any` produced.
func TestSetValueKeepsTheFieldsType(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	cases := []struct {
		name  string
		path  string
		value string
		// raw is the file text the write must produce, which is what tells an
		// integer from a float and a named string from a string.
		raw   string
		check func(t *testing.T, cfg config.Config)
	}{
		{
			name: "bool", path: "agent.auto", value: `true`, raw: "auto = true",
			check: func(t *testing.T, cfg config.Config) {
				if !cfg.Agent.Auto {
					t.Fatalf("agent.auto = false, want true")
				}
			},
		},
		{
			name: "string", path: "project.name", value: `"typed"`, raw: `name = 'typed'`,
			check: func(t *testing.T, cfg config.Config) {
				if cfg.Project.Name != "typed" {
					t.Fatalf("project.name = %q, want %q", cfg.Project.Name, "typed")
				}
			},
		},
		{
			name: "integer", path: "agent.max_tool_iterations", value: `7`, raw: "max_tool_iterations = 7",
			check: func(t *testing.T, cfg config.Config) {
				if cfg.Agent.MaxToolIterations != 7 {
					t.Fatalf("agent.max_tool_iterations = %d, want 7", cfg.Agent.MaxToolIterations)
				}
			},
		},
		{
			name: "float", path: "budgets.daily_usd", value: `5`, raw: "daily_usd = 5.0",
			check: func(t *testing.T, cfg config.Config) {
				if cfg.Budgets.DailyUSD != 5 {
					t.Fatalf("budgets.daily_usd = %v, want 5", cfg.Budgets.DailyUSD)
				}
			},
		},
		{
			name: "named type", path: "agent.mode", value: `"act"`, raw: "mode = 'act'",
			check: func(t *testing.T, cfg config.Config) {
				if string(cfg.Agent.Mode) != "act" {
					t.Fatalf("agent.mode = %q, want %q", string(cfg.Agent.Mode), "act")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a.mustCall(acpwire.MethodSetValue, acpwire.SetValueParams{
				SessionID: "s1",
				Path:      tc.path,
				Value:     json.RawMessage(tc.value),
			}, nil)
			tc.check(t, loadSettingsUserConfig(t, s))
			data, err := os.ReadFile(s.user)
			if err != nil {
				t.Fatalf("read user config: %v", err)
			}
			if !strings.Contains(string(data), tc.raw) {
				t.Fatalf("user config does not hold %q:\n%s", tc.raw, data)
			}
		})
	}

	err := a.call(acpwire.MethodSetValue, acpwire.SetValueParams{
		SessionID: "s1",
		Path:      "no.such.path",
		Value:     json.RawMessage(`true`),
	}, nil)
	if code := rpcCode(t, err); code != invalidParams {
		t.Fatalf("unknown path: code = %d, want %d", code, invalidParams)
	}
}

// probeServer serves a model list the way an OpenAI-compatible endpoint does,
// and counts the requests it received.
func probeServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"service-model","owned_by":"test"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// otherHandlers are the handlers a client without the capability reaches: each
// answers with a result no service call could produce.
func otherHandlers() map[string]Handler {
	return map[string]Handler{
		acpwire.MethodProbeProvider: func(ctx context.Context, params json.RawMessage) (any, error) {
			return ConfigProbeResult{Models: []ModelEntry{{ID: "other-model"}}, Error: "the other handler ran"}, nil
		},
		acpwire.MethodSetProviderKey: func(ctx context.Context, params json.RawMessage) (any, error) {
			return map[string]any{"otherHandler": true}, nil
		},
	}
}

// TestProbeProviderForAMarshalClientUsesTheService pins marshalOr's first arm:
// the probe runs on the named session's service, not on the connection's old
// ConfigManager handler.
func TestProbeProviderForAMarshalClientUsesTheService(t *testing.T) {
	srv, _ := probeServer(t)
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, otherHandlers())

	var res acpwire.ModelsResult
	a.mustCall(acpwire.MethodProbeProvider, acpwire.ProviderParams{
		SessionID: "s1",
		Name:      "testprov",
		Provider:  json.RawMessage(fmt.Sprintf(`{"type":"openai_compatible","baseUrl":%q}`, srv.URL+"/v1")),
	}, &res)

	var models []schema.ModelInfo
	if err := json.Unmarshal(res.Models, &models); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "service-model" {
		t.Fatalf("models = %+v, want the service's probe result", models)
	}
}

// TestProbeProviderWithoutTheCapabilityReachesItsOtherHandler pins marshalOr's
// second arm: a client that did not declare the capability keeps the handler it
// had, and the service is never reached.
func TestProbeProviderWithoutTheCapabilityReachesItsOtherHandler(t *testing.T) {
	srv, hits := probeServer(t)
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, false, []*settingsSession{s}, otherHandlers())

	var res ConfigProbeResult
	a.mustCall(acpwire.MethodProbeProvider, acpwire.ProviderParams{
		SessionID: "s1",
		Name:      "testprov",
		Provider:  json.RawMessage(fmt.Sprintf(`{"type":"openai_compatible","baseUrl":%q}`, srv.URL+"/v1")),
	}, &res)

	if res.Error != "the other handler ran" || len(res.Models) != 1 || res.Models[0].ID != "other-model" {
		t.Fatalf("result = %+v, want the other handler's", res)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the endpoint received %d probes, want 0: the service must not be reached", n)
	}
}

// TestSetProviderKeyForAMarshalClientRejectsAMask pins the service's mask guard:
// a masked key would become the stored key it stands for, so it is refused and
// the file keeps the real one. Without the capability the old handler answers.
func TestSetProviderKeyForAMarshalClientRejectsAMask(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, otherHandlers())

	a.mustCall(acpwire.MethodSetProviderKey, acpwire.ProviderKeyParams{
		SessionID: "s1", Name: "local", Key: "sk-the-real-key",
	}, nil)
	data, err := os.ReadFile(s.user)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(data), "sk-the-real-key") {
		t.Fatalf("the real key was not written:\n%s", data)
	}

	err = a.call(acpwire.MethodSetProviderKey, acpwire.ProviderKeyParams{
		SessionID: "s1", Name: "local", Key: "••••-key",
	}, nil)
	// A marshal client's unclassified failure keeps its sentence (decision 1),
	// so the wire carries the service's refusal rather than a fixed text.
	var refused *acpwire.Error
	if !errors.As(err, &refused) || refused.Code != "" {
		t.Fatalf("err = %v, want the service's uncoded refusal", err)
	}
	if !strings.Contains(err.Error(), "no stored secret matches its mask") {
		t.Fatalf("err = %q, want the service's mask sentence", err.Error())
	}
	after, err := os.ReadFile(s.user)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if string(after) != string(data) {
		t.Fatalf("the refused write changed the file:\n%s\nwant\n%s", after, data)
	}

	// The manager answers with the same sentence in process, so the two arms
	// agree on the refusal (decision 1).
	raw := json.RawMessage(`{"sessionId":"s1","name":"local","key":"••••-key"}`)
	_, derErr := a.settings.SetProviderKey(context.Background(), raw)
	if derErr == nil || !strings.Contains(derErr.Error(), "no stored secret matches its mask") {
		t.Fatalf("SetProviderKey = %v, want the service's mask error", derErr)
	}

	// A client without the capability reaches the old handler instead.
	plain := startSettingsAgent(t, false, []*settingsSession{s}, otherHandlers())
	var res map[string]any
	plain.mustCall(acpwire.MethodSetProviderKey, acpwire.ProviderKeyParams{
		SessionID: "s1", Name: "local", Key: "sk-ignored",
	}, &res)
	if res["otherHandler"] != true {
		t.Fatalf("result = %+v, want the other handler's", res)
	}
}

// settingsAuth is a test AuthResolver: a private in-memory token store and the
// engines a test pinned, by provider name. No real keychain is ever opened.
type settingsAuth struct {
	mu        sync.Mutex
	store     *credentials.MemStore
	cache     map[string]*oauth.Engine
	storeErr  error
	engineErr error
}

func newSettingsAuth() *settingsAuth {
	return &settingsAuth{store: credentials.NewMemStore(), cache: map[string]*oauth.Engine{}}
}

func (a *settingsAuth) OpenStore(string) (credentials.Store, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.storeErr != nil {
		return nil, a.storeErr
	}
	return a.store, nil
}

func (a *settingsAuth) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engineErr != nil {
		return nil, a.engineErr
	}
	if eng, ok := a.cache[name]; ok {
		return eng, nil
	}
	eng, err := provider.NewCodexOAuthEngineWithStore(name, a.store)
	if err != nil {
		return nil, err
	}
	a.cache[name] = eng
	return eng, nil
}

// pinSignInEngine pins a sign-in engine for name: a test-owned token endpoint
// and an ephemeral loopback port, so no browser and no network are touched.
func pinSignInEngine(t *testing.T, auth *settingsAuth, name string, loopback time.Duration) {
	t.Helper()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"test-at","refresh_token":"test-rt","expires_in":3600,"token_type":"Bearer"}`)
	}))
	t.Cleanup(tokenSrv.Close)
	engine := &oauth.Engine{
		ServerURL:  "https://auth.test.invalid",
		Store:      auth.store,
		StorageKey: "marshal:provider:" + name,
		Flow: oauth.FlowConfig{
			Issuer:        "https://auth.test.invalid",
			AuthorizeURL:  "https://auth.test.invalid/oauth/authorize",
			TokenURL:      tokenSrv.URL,
			ClientID:      "app_test",
			RedirectPorts: []int{0}, // ephemeral port: no fixed-port collision
			RedirectPath:  "/auth/callback",
		},
		CacheDir:        t.TempDir(), // never the repo's .marshal/oauth-metadata
		LoopbackTimeout: loopback,
	}
	auth.mu.Lock()
	auth.cache[name] = engine
	auth.mu.Unlock()
}

// completeSignIn drives the loopback callback the way a browser would, so the
// sign-in in flight finishes.
func completeSignIn(t *testing.T, authorizeURL string) {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL %q: %v", authorizeURL, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		return
	}
	resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=test-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("loopback callback: %v", err)
	}
	resp.Body.Close()
}

// TestLoginProviderReportsTheURLAsProgress pins decision 7's report: the
// authorization URL travels as this call's $/progress value, and it precedes the
// response.
func TestLoginProviderReportsTheURLAsProgress(t *testing.T) {
	auth := newSettingsAuth()
	pinSignInEngine(t, auth, "codex-test", 10*time.Second)
	s := newSettingsSession(t, "s1", func(d *service.Deps) { d.Auth = auth })
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	var (
		mu  sync.Mutex
		got []acpwire.LoginProgress
	)
	done := make(chan error, 1)
	go func() {
		done <- a.conn.CallWithProgress(context.Background(), acpwire.MethodLoginProvider,
			acpwire.LoginParams{SessionID: "s1", Name: "codex-test"}, nil,
			func(value json.RawMessage) {
				var p acpwire.LoginProgress
				if err := json.Unmarshal(value, &p); err != nil {
					return
				}
				mu.Lock()
				got = append(got, p)
				mu.Unlock()
				completeSignIn(t, p.URL)
			})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("config/login_provider: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("config/login_provider did not return")
	}

	mu.Lock()
	values := append([]acpwire.LoginProgress(nil), got...)
	mu.Unlock()
	if len(values) != 1 {
		t.Fatalf("progress values = %+v, want exactly one", values)
	}
	u, err := url.Parse(values[0].URL)
	if err != nil {
		t.Fatalf("progress URL %q: %v", values[0].URL, err)
	}
	if u.Query().Get("redirect_uri") == "" {
		t.Fatalf("progress URL %q carries no loopback redirect", values[0].URL)
	}
}

// settingsInFlight reports whether the server is still handling a request: its
// in-flight registry empties once every handler has returned.
func settingsInFlight(srv *Server) bool {
	srv.stateMu.Lock()
	defer srv.stateMu.Unlock()
	return len(srv.inFlight) > 0
}

// TestCancellingASignInEndsTheFlow pins decision 7's cancellation: cancelling
// the client's context ends the sign-in the handler is waiting on, so the
// handler returns within the bound rather than waiting for its callback.
func TestCancellingASignInEndsTheFlow(t *testing.T) {
	auth := newSettingsAuth()
	pinSignInEngine(t, auth, "codex-test", 30*time.Second)
	s := newSettingsSession(t, "s1", func(d *service.Deps) { d.Auth = auth })
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	urlSeen := make(chan struct{}, 1)
	callDone := make(chan error, 1)
	go func() {
		callDone <- a.conn.CallWithProgress(ctx, acpwire.MethodLoginProvider,
			acpwire.LoginParams{SessionID: "s1", Name: "codex-test"}, nil,
			func(json.RawMessage) {
				select {
				case urlSeen <- struct{}{}:
				default:
				}
			})
	}()

	// The URL means the sign-in is running and waiting for its callback.
	select {
	case <-urlSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the sign-in never reported its URL")
	}

	cancel()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled call did not return")
	}

	// The client's return says nothing about the agent: the handler must have
	// ended too, which means the engine's context ended with it.
	deadline := time.Now().Add(2 * time.Second)
	for settingsInFlight(a.srv) {
		if time.Now().After(deadline) {
			t.Fatal("the sign-in's handler outlived the cancelled request by more than 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTrustCallsReachTheHost pins the two trust calls: trust/prompt answers the
// pending directory and trust/decide passes the decision to the runtime.
func TestTrustCallsReachTheHost(t *testing.T) {
	pending := t.TempDir()
	var (
		mu      sync.Mutex
		decided []trust.Decision
	)
	s := newSettingsSession(t, "s1", func(d *service.Deps) {
		d.TrustPrompt = pending
		d.DecideTrust = func(dec trust.Decision) {
			mu.Lock()
			defer mu.Unlock()
			decided = append(decided, dec)
		}
	})
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	var res acpwire.TrustPromptResult
	a.mustCall(acpwire.MethodTrustPrompt, acpwire.HostCallParams{SessionID: "s1"}, &res)
	if res.Dir != pending {
		t.Fatalf("trust/prompt dir = %q, want %q", res.Dir, pending)
	}
	if res.InPlace {
		t.Fatalf("trust/prompt = %+v, want no in-place trust without ApplyTrust", res)
	}

	a.mustCall(acpwire.MethodTrustDecide, acpwire.TrustDecideParams{
		SessionID: "s1", Dir: pending, Decision: acpwire.TrustPermanent,
	}, nil)
	mu.Lock()
	got := append([]trust.Decision(nil), decided...)
	mu.Unlock()
	if len(got) != 1 || got[0] != trust.Decision(acpwire.TrustPermanent) {
		t.Fatalf("decisions = %v, want one %q", got, acpwire.TrustPermanent)
	}

	// A directory that is not the pending one is the service's error.
	err := a.call(acpwire.MethodTrustDecide, acpwire.TrustDecideParams{
		SessionID: "s1", Dir: t.TempDir(), Decision: acpwire.TrustSession,
	}, nil)
	if err == nil {
		t.Fatal("trust/decide accepted a directory that is not pending")
	}
}

// TestTheTrustCallsServeMarshalClientsOnly pins decision 3: a client without
// the marshal capability reads no prompt, and its decision is refused. The
// bridge's own connection is such a client, so it can neither read the prompt
// nor answer it (P4 W11).
func TestTheTrustCallsServeMarshalClientsOnly(t *testing.T) {
	pending := t.TempDir()
	var (
		mu      sync.Mutex
		applied []trust.Decision
	)
	s := newSettingsSession(t, "s1", func(d *service.Deps) {
		d.TrustPrompt = pending
		d.ApplyTrust = func(_ context.Context, dec trust.Decision) error {
			mu.Lock()
			defer mu.Unlock()
			applied = append(applied, dec)
			return nil
		}
	})
	a := startSettingsAgent(t, false, []*settingsSession{s}, nil)

	var res acpwire.TrustPromptResult
	a.mustCall(acpwire.MethodTrustPrompt, acpwire.HostCallParams{SessionID: "s1"}, &res)
	if res.Dir != "" || res.InPlace {
		t.Fatalf("trust/prompt = %+v for a client without the capability, want no prompt", res)
	}

	err := a.call(acpwire.MethodTrustDecide, acpwire.TrustDecideParams{
		SessionID: "s1", Dir: pending, Decision: acpwire.TrustPermanent,
	}, nil)
	if err == nil {
		t.Fatal("trust/decide accepted a client without the capability")
	}
	if code := rpcCode(t, err); code != int(invalidRequest) {
		t.Fatalf("trust/decide code = %d, want invalid request (%d)", code, invalidRequest)
	}
	if !strings.Contains(err.Error(), "trust/decide serves marshal clients only") {
		t.Fatalf("trust/decide error = %q, want the marshal-client refusal", err)
	}
	mu.Lock()
	got := append([]trust.Decision(nil), applied...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("applied decisions = %v, want none", got)
	}
}

// TestTheHostCallsRefuseBadParams pins the shared guard: every host call takes
// the client's current session, and a call without one is invalid params.
func TestTheHostCallsRefuseBadParams(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, otherHandlers())

	calls := map[string]string{
		acpwire.MethodProviderTemplates: `{}`,
		acpwire.MethodTrustPrompt:       `{}`,
		acpwire.MethodTrustDecide:       `{"dir":"/tmp/x","decision":"trust_session"}`,
		acpwire.MethodProbeProvider:     `{"name":"p","provider":{}}`,
		acpwire.MethodProbeModel:        `{"name":"p","provider":{},"model":"m"}`,
		acpwire.MethodModelsList:        `{"name":"p"}`,
		acpwire.MethodModelsSave:        `{"name":"p"}`,
		acpwire.MethodModelsForget:      `{"name":"p"}`,
		acpwire.MethodModelsRename:      `{"from":"a","to":"b"}`,
		acpwire.MethodLoginProvider:     `{"name":"p"}`,
		acpwire.MethodSetValue:          `{"path":"project.name","value":"x"}`,
		acpwire.MethodSaveProviders:     `{"providers":{}}`,
		acpwire.MethodSetProviderKey:    `{"name":"p","key":"k"}`,
		acpwire.MethodSavePresets:       `{"presets":{}}`,
		acpwire.MethodPreparePresets:    `{"config":{}}`,
		acpwire.MethodSaveProvenance:    `{}`,
	}
	for method, params := range calls {
		t.Run(method, func(t *testing.T) {
			err := a.call(method, json.RawMessage(params), nil)
			if code := rpcCode(t, err); code != invalidParams {
				t.Fatalf("%s without sessionId: code = %d, want %d", method, code, invalidParams)
			}
		})
	}
}

// waitForSettings polls cond until it reports true or timeout expires.
func waitForSettings(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// readSessionConfig calls session/config and returns its result.
func readSessionConfig(t *testing.T, a *settingsAgent, id string) acpwire.SessionConfig {
	t.Helper()
	var got acpwire.SessionConfig
	a.mustCall(acpwire.MethodSessionConfig, acpwire.ConfigParams{SessionID: id}, &got)
	return got
}

// forwarderRunning reports whether the manager holds a config forwarder for the
// session, and how many fences are registered for it.
func (a *settingsAgent) forwarderRunning(id string) (running bool, fences int) {
	m := a.settings
	m.mu.Lock()
	_, running = m.forwarders[id]
	m.mu.Unlock()
	m.fences.mu.Lock()
	fences = len(m.fences.fences[id])
	m.fences.mu.Unlock()
	return running, fences
}

// TestTheFirstSessionConfigArrivesBeforeTheResponse pins R2's subscription: the
// first marshal session/config returns after exactly one session_config update
// for the session was recorded, and that update carries the call's own result.
func TestTheFirstSessionConfigArrivesBeforeTheResponse(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	got := readSessionConfig(t, a, "s1")

	updates := a.configs()
	if len(updates) != 1 {
		t.Fatalf("recorded %d session_config updates before the response, want exactly 1", len(updates))
	}
	if updates[0].sessionID != "s1" {
		t.Errorf("update sessionId = %q, want %q", updates[0].sessionID, "s1")
	}
	if !reflect.DeepEqual(updates[0].config, got) {
		t.Errorf("the update = %+v, want the call's result %+v", updates[0].config, got)
	}
}

// TestAConfigChangeIsSentWithinTheInterval pins the forwarder's cadence: a
// config change reaches the client by the next tick, and a quiet session sends
// nothing.
func TestAConfigChangeIsSentWithinTheInterval(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)
	readSessionConfig(t, a, "s1")

	// The state's layers change the way a reload changes them, without a call.
	layers := s.state.Layers()
	layers.Merged.Project.Name = "layers-changed"
	s.state.SetLayers(layers)

	waitForSettings(t, 500*time.Millisecond, "the layer change to arrive", func() bool {
		for _, u := range a.configs() {
			var got config.Layers
			if err := config.UnmarshalLayersJSON(u.config.Layers, &got); err != nil {
				continue
			}
			if got.Merged.Project.Name == "layers-changed" {
				return true
			}
		}
		return false
	})

	// Three quiet intervals send nothing: the encoding is unchanged.
	quiet := a.configCount()
	time.Sleep(3*configInterval + 50*time.Millisecond)
	if n := a.configCount(); n != quiet {
		t.Fatalf("a quiet session sent %d more updates, want none", n-quiet)
	}
}

// TestAHostWriteReturnsAfterItsConfigUpdate pins R2's barrier for a host write:
// with every session/update frame slowed, config/set_value returns only after a
// session_config update carrying the new value was recorded.
func TestAHostWriteReturnsAfterItsConfigUpdate(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgentDelay(t, 50*time.Millisecond, true, []*settingsSession{s}, nil)
	// The first read subscribes the mirror, so the barrier has a forwarder to
	// fence.
	readSessionConfig(t, a, "s1")

	a.mustCall(acpwire.MethodSetValue, acpwire.SetValueParams{
		SessionID: "s1",
		Path:      "project.name",
		Value:     json.RawMessage(`"written-by-host"`),
	}, nil)

	// The call has returned: the update it caused must already be recorded.
	for _, u := range a.configs() {
		var layers config.Layers
		if err := config.UnmarshalLayersJSON(u.config.Layers, &layers); err != nil {
			continue
		}
		if layers.Merged.Project.Name == "written-by-host" {
			return
		}
	}
	t.Fatalf("config/set_value returned before a session_config update carrying the new value was recorded: %+v", a.configs())
}

// TestApplyConfigInstallsTheDecodedConfig pins the decode path: a config with a
// priced preset, applied over the wire, is the state's config and its Pricing
// is the typed pricing rather than the map a JSON decode into `any` produces.
func TestApplyConfigInstallsTheDecodedConfig(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"priced": {
			Provider: "testprov",
			Model:    "priced-model",
			Pricing:  &pricing.ModelPricing{InputPerMTokCents: 250, OutputPerMTokCents: 1000},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}

	a.mustCall(acpwire.MethodApplyConfig, acpwire.ConfigParams{SessionID: "s1", Config: raw}, nil)

	installed := s.state.ConfigSnapshot()
	preset, ok := installed.Models.Presets["priced"]
	if !ok {
		t.Fatal("the applied config is not the state's config: the priced preset is absent")
	}
	priced, ok := preset.Pricing.(*pricing.ModelPricing)
	if !ok {
		t.Fatalf("Pricing = %T, want a *pricing.ModelPricing", preset.Pricing)
	}
	if priced.InputPerMTokCents != 250 || priced.OutputPerMTokCents != 1000 {
		t.Fatalf("Pricing = %+v, want the applied rates", priced)
	}
}

// TestTheReloadFlagTravelsWithItsError pins decision 6: the reload's flag and
// its error travel together in the result, not as a JSON-RPC error.
func TestTheReloadFlagTravelsWithItsError(t *testing.T) {
	s := newSettingsSession(t, "s1", func(d *service.Deps) {
		d.ReloadConfig = func(config.Config) error { return errors.New("reload boom") }
	})
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	raw, err := json.Marshal(config.Default())
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	var res acpwire.ReloadResult
	a.mustCall(acpwire.MethodReloadConfig, acpwire.ConfigParams{SessionID: "s1", Config: raw}, &res)
	if !res.Reloaded {
		t.Error("reloaded = false, want true: the session has a reload path")
	}
	if res.Error == nil || res.Error.Message != "reload boom" {
		t.Errorf("error = %+v, want the reload's own message", res.Error)
	}

	// A session with no reload path answers the flag without an error.
	plain := newSettingsSession(t, "s1", nil)
	b := startSettingsAgent(t, true, []*settingsSession{plain}, nil)
	var none acpwire.ReloadResult
	b.mustCall(acpwire.MethodReloadConfig, acpwire.ConfigParams{SessionID: "s1", Config: raw}, &none)
	if none.Reloaded {
		t.Error("reloaded = true, want false: the session has no reload path")
	}
	if none.Error != nil {
		t.Errorf("error = %+v, want none", none.Error)
	}
}

// TestAnMCPLoginReportsTheURLAsProgress pins decision 7 for the MCP sign-in: the
// authorization URL travels as this call's $/progress value, against a
// test-owned authorization server, and it precedes the response.
func TestAnMCPLoginReportsTheURLAsProgress(t *testing.T) {
	// The engine writes its on-disk metadata cache relative to the working
	// directory; chdir into a temp dir so the test is hermetic.
	t.Chdir(t.TempDir())
	mcp.AllowInsecureHTTP(true)
	t.Cleanup(func() { mcp.AllowInsecureHTTP(false) })

	metadataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		base := "http://" + r.Host
		switch {
		// The engine derives the RFC 8414 path from the server URL, so an
		// endpoint with a path (/mcp) is probed at /.well-known/…/mcp.
		case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-authorization-server"):
			io.WriteString(w, `{"issuer":"`+base+`","authorization_endpoint":"`+base+`/authorize","token_endpoint":"`+base+`/token","registration_endpoint":"`+base+`/register"}`)
		case r.URL.Path == "/register":
			io.WriteString(w, `{"client_id":"test-client"}`)
		case r.URL.Path == "/token":
			io.WriteString(w, `{"access_token":"test-at","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(metadataSrv.Close)

	s := newSettingsSession(t, "s1", func(d *service.Deps) {
		d.State.Config.MCP.Servers = map[string]config.MCPServerConfig{
			"linear": {URL: metadataSrv.URL + "/mcp", Type: "http", Auth: "oauth", Trust: "unrestricted"},
		}
	})
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	var (
		mu  sync.Mutex
		got []acpwire.LoginProgress
	)
	done := make(chan error, 1)
	go func() {
		done <- a.conn.CallWithProgress(context.Background(), acpwire.MethodMCPLogin,
			acpwire.LoginParams{SessionID: "s1", Name: "linear"}, nil,
			func(value json.RawMessage) {
				var p acpwire.LoginProgress
				if err := json.Unmarshal(value, &p); err != nil {
					return
				}
				mu.Lock()
				got = append(got, p)
				mu.Unlock()
				completeSignIn(t, p.URL)
			})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("session/mcp_login: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("session/mcp_login did not return")
	}

	mu.Lock()
	values := append([]acpwire.LoginProgress(nil), got...)
	mu.Unlock()
	if len(values) != 1 {
		t.Fatalf("progress values = %+v, want exactly one", values)
	}
	if !strings.Contains(values[0].URL, "/authorize?") {
		t.Fatalf("progress URL %q, want an authorization URL", values[0].URL)
	}
}

// TestAClientWithoutTheCapabilityGetsNoConfigUpdates pins V6's second half: a
// client that did not declare the capability reads the config, and nothing is
// pushed to it.
func TestAClientWithoutTheCapabilityGetsNoConfigUpdates(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, false, []*settingsSession{s}, nil)

	got := readSessionConfig(t, a, "s1")
	if len(got.Config) == 0 || len(got.Layers) == 0 {
		t.Fatalf("session/config = %+v, want the session's config and layers", got)
	}

	time.Sleep(200 * time.Millisecond)
	if n := a.configCount(); n != 0 {
		t.Fatalf("recorded %d session_config updates, want none for a client without the capability", n)
	}
}

// TestTheSessionCallsRefuseBadParams pins the shared guard: every session call
// names its session, and a call without one is invalid params.
func TestTheSessionCallsRefuseBadParams(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)

	calls := map[string]string{
		acpwire.MethodSessionConfig:      `{}`,
		acpwire.MethodApplyConfig:        `{"config":{}}`,
		acpwire.MethodReloadConfig:       `{"config":{}}`,
		acpwire.MethodMCPReconnect:       `{}`,
		acpwire.MethodSaveProjectConfig:  `{"config":{}}`,
		acpwire.MethodSaveVerifyCommands: `{"build":"b","test":"t"}`,
		acpwire.MethodMCPLogin:           `{"name":"linear"}`,
	}
	for method, params := range calls {
		t.Run(method, func(t *testing.T) {
			err := a.call(method, json.RawMessage(params), nil)
			if code := rpcCode(t, err); code != invalidParams {
				t.Fatalf("%s without sessionId: code = %d, want %d", method, code, invalidParams)
			}
		})
	}
}

// TestTheConfigForwarderStopsWhenTheSessionGoes pins the forwarder's own end: a
// session the lookup no longer reports stops its forwarder and drops its fence.
func TestTheConfigForwarderStopsWhenTheSessionGoes(t *testing.T) {
	s := newSettingsSession(t, "s1", nil)
	a := startSettingsAgent(t, true, []*settingsSession{s}, nil)
	readSessionConfig(t, a, "s1")

	waitForSettings(t, 500*time.Millisecond, "the config forwarder to start", func() bool {
		running, fences := a.forwarderRunning("s1")
		return running && fences == 1
	})

	a.lookup.Store(false)

	waitForSettings(t, 500*time.Millisecond, "the config forwarder to stop", func() bool {
		running, fences := a.forwarderRunning("s1")
		return !running && fences == 0
	})
}
