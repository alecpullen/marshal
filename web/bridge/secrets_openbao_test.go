package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBao emulates AppRole login, renew-self and KV v2.
type fakeBao struct {
	mu        sync.Mutex
	logins    int
	renews    int
	validTok  string
	kv        map[string]string // storage path -> base64 value
	leaseSecs int
	failRenew bool
}

func newFakeBao() *fakeBao { return &fakeBao{kv: map[string]string{}, leaseSecs: 100} }

func (f *fakeBao) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeAuth := func(tok string) {
		json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": f.leaseSecs}})
	}
	switch {
	case r.URL.Path == "/v1/auth/approle/login":
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["role_id"] != "role" || b["secret_id"] != "sec" {
			http.Error(w, "denied", http.StatusBadRequest)
			return
		}
		f.logins++
		f.validTok = "tok-" + string(rune('0'+f.logins))
		writeAuth(f.validTok)
		return
	case r.URL.Path == "/v1/auth/token/renew-self":
		if f.failRenew || r.Header.Get("X-Vault-Token") != f.validTok {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		f.renews++
		writeAuth(f.validTok)
		return
	}
	if r.Header.Get("X-Vault-Token") != f.validTok {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/secret/data/"):
		sp := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodGet:
			v, ok := f.kv[sp]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"value": v}}})
		case http.MethodPost:
			var b struct {
				Data map[string]string `json:"data"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			f.kv[sp] = b.Data["value"]
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		}
	case strings.HasPrefix(r.URL.Path, "/v1/secret/metadata/"):
		sp := strings.TrimPrefix(r.URL.Path, "/v1/secret/metadata/")
		switch r.Method {
		case http.MethodDelete:
			delete(f.kv, sp)
			w.WriteHeader(http.StatusNoContent)
		case "LIST":
			dir := strings.TrimSuffix(sp, "/") + "/"
			seen := map[string]bool{}
			for k := range f.kv {
				if rest, ok := strings.CutPrefix(k, dir); ok {
					if i := strings.Index(rest, "/"); i >= 0 {
						rest = rest[:i+1]
					}
					seen[rest] = true
				}
			}
			if len(seen) == 0 {
				http.NotFound(w, r)
				return
			}
			var keys []string
			for k := range seen {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": keys}})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func newTestBao(t *testing.T) (*baoProvider, *fakeBao, *httptest.Server) {
	t.Helper()
	fb := newFakeBao()
	srv := httptest.NewTLSServer(fb)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o600)
		return p
	}
	ca := write("ca.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
	p, err := NewBaoProvider(srv.URL, "secret", write("role", "role\n"), write("sec", " sec \n"), ca)
	if err != nil {
		t.Fatal(err)
	}
	return p.(*baoProvider), fb, srv
}

func TestBaoOperations(t *testing.T) {
	ctx := context.Background()
	p, fb, _ := newTestBao(t)
	if err := p.Put(ctx, "local", "git/github", []byte("tok")); err != nil {
		t.Fatal(err)
	}
	p.Put(ctx, "local", "providers/x", []byte("k"))
	if want := base64.StdEncoding.EncodeToString([]byte("tok")); fb.kv["marshal/local/git/github"] != want {
		t.Fatalf("stored %q", fb.kv["marshal/local/git/github"])
	}
	got, err := p.Get(ctx, "local", "git/github")
	if err != nil || string(got) != "tok" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := p.Get(ctx, "local", "git/none"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing: %v", err)
	}
	all, err := p.List(ctx, "local", "")
	if err != nil || len(all) != 2 || all[0] != "git/github" || all[1] != "providers/x" {
		t.Fatalf("List = %v, %v", all, err)
	}
	sub, _ := p.List(ctx, "local", "git/")
	if len(sub) != 1 || sub[0] != "git/github" {
		t.Fatalf("prefixed List = %v", sub)
	}
	if err := p.Delete(ctx, "local", "git/github"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, "local", "git/github"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if empty, err := p.List(ctx, "other", ""); err != nil || len(empty) != 0 {
		t.Fatalf("empty List = %v, %v", empty, err)
	}
}

func TestBaoRenewsAtHalfTTL(t *testing.T) {
	ctx := context.Background()
	p, fb, _ := newTestBao(t)
	clock := time.Now()
	p.now = func() time.Time { return clock }
	p.Put(ctx, "local", "a", []byte("v"))
	if fb.logins != 1 || fb.renews != 0 {
		t.Fatalf("logins=%d renews=%d", fb.logins, fb.renews)
	}
	clock = clock.Add(49 * time.Second)
	p.Get(ctx, "local", "a")
	if fb.renews != 0 {
		t.Fatalf("renewed before half the TTL")
	}
	clock = clock.Add(2 * time.Second)
	if _, err := p.Get(ctx, "local", "a"); err != nil {
		t.Fatal(err)
	}
	if fb.renews != 1 || fb.logins != 1 {
		t.Fatalf("logins=%d renews=%d", fb.logins, fb.renews)
	}
	// A failing renew falls back to a fresh login.
	fb.failRenew = true
	clock = clock.Add(60 * time.Second)
	if _, err := p.Get(ctx, "local", "a"); err != nil {
		t.Fatal(err)
	}
	if fb.logins != 2 {
		t.Fatalf("logins=%d", fb.logins)
	}
}

func TestBao403RetriesOnce(t *testing.T) {
	ctx := context.Background()
	p, fb, _ := newTestBao(t)
	p.Put(ctx, "local", "a", []byte("v"))
	fb.mu.Lock()
	fb.validTok = "rotated-server-side"
	fb.mu.Unlock()
	if _, err := p.Get(ctx, "local", "a"); err != nil {
		t.Fatalf("Get after token revoked: %v", err)
	}
	if fb.logins != 2 {
		t.Fatalf("logins=%d, want one re-login", fb.logins)
	}
}

func TestBao403TwiceFails(t *testing.T) {
	ctx := context.Background()
	fb := newFakeBao()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/auth/") {
			fb.ServeHTTP(w, r)
			return
		}
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	p := &baoProvider{addr: srv.URL, mount: "secret", roleID: "role", secretID: "sec", client: srv.Client(), now: time.Now}
	if _, err := p.Get(ctx, "local", "a"); err == nil {
		t.Fatal("expected an error")
	}
	if fb.logins != 2 {
		t.Fatalf("logins=%d, want 2 (initial + one retry)", fb.logins)
	}
}

func TestBaoCAFileTrustsServer(t *testing.T) {
	ctx := context.Background()
	fb := newFakeBao()
	srv := httptest.NewTLSServer(fb)
	defer srv.Close()
	dir := t.TempDir()
	role := filepath.Join(dir, "r")
	sec := filepath.Join(dir, "s")
	os.WriteFile(role, []byte("role"), 0o600)
	os.WriteFile(sec, []byte("sec"), 0o600)
	// Without the CA the self-signed server is rejected.
	p, err := NewBaoProvider(srv.URL, "secret", role, sec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, "local", "a"); err == nil {
		t.Fatal("untrusted server accepted")
	}
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	p, err = NewBaoProvider(srv.URL, "secret", role, sec, ca)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, "local", "a"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("with CA: %v", err)
	}
	bad := filepath.Join(dir, "bad.pem")
	os.WriteFile(bad, []byte("nope"), 0o600)
	if _, err := NewBaoProvider(srv.URL, "secret", role, sec, bad); err == nil {
		t.Fatal("garbage CA accepted")
	}
}
