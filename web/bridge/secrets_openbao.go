package bridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// baoProvider talks to an OpenBao (or Vault) KV v2 mount, authenticating
// with AppRole. Values are stored base64 under data.value.
type baoProvider struct {
	addr, mount      string
	roleID, secretID string
	client           *http.Client
	now              func() time.Time
	mu               sync.Mutex
	token            string
	renewAt          time.Time
}

// NewBaoProvider reads the AppRole ID files and builds the HTTP client.
// When caFile is set the client trusts it in addition to the system roots.
func NewBaoProvider(addr, mount, roleIDFile, secretIDFile, caFile string) (SecretProvider, error) {
	if addr == "" {
		return nil, errors.New("--secrets openbao needs --bao-addr")
	}
	if mount == "" {
		mount = "secret"
	}
	role, err := os.ReadFile(roleIDFile)
	if err != nil {
		return nil, fmt.Errorf("bao role id file: %w", err)
	}
	sec, err := os.ReadFile(secretIDFile)
	if err != nil {
		return nil, fmt.Errorf("bao secret id file: %w", err)
	}
	tr := &http.Transport{}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("bao ca file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("bao ca file holds no certificate")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &baoProvider{
		addr:     strings.TrimRight(addr, "/"),
		mount:    strings.Trim(mount, "/"),
		roleID:   strings.TrimSpace(string(role)),
		secretID: strings.TrimSpace(string(sec)),
		client:   &http.Client{Transport: tr, Timeout: 15 * time.Second},
		now:      time.Now,
	}, nil
}

func (p *baoProvider) Name() string { return "openbao" }

type baoAuth struct {
	Auth struct {
		ClientToken   string `json:"client_token"`
		LeaseDuration int    `json:"lease_duration"`
	} `json:"auth"`
}

func (p *baoProvider) setToken(a baoAuth) error {
	if a.Auth.ClientToken == "" {
		return errors.New("bao: login returned no token")
	}
	ttl := time.Duration(a.Auth.LeaseDuration) * time.Second
	p.token = a.Auth.ClientToken
	p.renewAt = p.now().Add(ttl / 2)
	return nil
}

// do sends one request with the given token and returns status and body.
func (p *baoProvider) do(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.addr+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, data, err
}

func (p *baoProvider) login(ctx context.Context) error {
	st, data, err := p.do(ctx, http.MethodPost, "/v1/auth/approle/login", "",
		map[string]string{"role_id": p.roleID, "secret_id": p.secretID})
	if err != nil {
		return err
	}
	if st != http.StatusOK {
		return fmt.Errorf("bao login: status %d", st)
	}
	var a baoAuth
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("bao login: %w", err)
	}
	return p.setToken(a)
}

// ensureToken logs in when there is no token, and renews (or logs in
// again) once half the TTL has passed. Caller holds p.mu.
func (p *baoProvider) ensureToken(ctx context.Context) error {
	if p.token == "" {
		return p.login(ctx)
	}
	if p.now().Before(p.renewAt) {
		return nil
	}
	st, data, err := p.do(ctx, http.MethodPost, "/v1/auth/token/renew-self", p.token, struct{}{})
	if err == nil && st == http.StatusOK {
		var a baoAuth
		if json.Unmarshal(data, &a) == nil {
			if a.Auth.ClientToken == "" {
				a.Auth.ClientToken = p.token
			}
			if p.setToken(a) == nil {
				return nil
			}
		}
	}
	return p.login(ctx)
}

// request runs an authenticated call; a 403 forces one re-login and retry.
func (p *baoProvider) request(ctx context.Context, method, path string, body any) (int, []byte, error) {
	p.mu.Lock()
	if err := p.ensureToken(ctx); err != nil {
		p.mu.Unlock()
		return 0, nil, err
	}
	tok := p.token
	p.mu.Unlock()
	st, data, err := p.do(ctx, method, path, tok, body)
	if err != nil || st != http.StatusForbidden {
		return st, data, err
	}
	p.mu.Lock()
	if p.token == tok { // nobody refreshed it meanwhile
		if err := p.login(ctx); err != nil {
			p.mu.Unlock()
			return 0, nil, err
		}
	}
	tok = p.token
	p.mu.Unlock()
	return p.do(ctx, method, path, tok, body)
}

func (p *baoProvider) kvPath(kind, owner, ref string) string {
	return "/v1/" + p.mount + "/" + kind + "/" + escapePath(storagePath(owner, ref))
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func baoStatusErr(op string, st int) error {
	if st == http.StatusNotFound {
		return ErrSecretNotFound
	}
	return fmt.Errorf("bao %s: status %d", op, st)
}

func (p *baoProvider) Get(ctx context.Context, owner, ref string) ([]byte, error) {
	if err := validateSecretPath(ref); err != nil {
		return nil, err
	}
	st, data, err := p.request(ctx, http.MethodGet, p.kvPath("data", owner, ref), nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, baoStatusErr("get", st)
	}
	var out struct {
		Data struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("bao get: %w", err)
	}
	v, err := base64.StdEncoding.DecodeString(out.Data.Data.Value)
	if err != nil {
		return nil, fmt.Errorf("bao get: %w", err)
	}
	return v, nil
}

func (p *baoProvider) Put(ctx context.Context, owner, ref string, value []byte) error {
	if err := validateSecretPath(ref); err != nil {
		return err
	}
	body := map[string]any{"data": map[string]string{"value": base64.StdEncoding.EncodeToString(value)}}
	st, _, err := p.request(ctx, http.MethodPost, p.kvPath("data", owner, ref), body)
	if err != nil {
		return err
	}
	if st != http.StatusOK && st != http.StatusNoContent {
		return fmt.Errorf("bao put: status %d", st)
	}
	return nil
}

func (p *baoProvider) Delete(ctx context.Context, owner, ref string) error {
	if err := validateSecretPath(ref); err != nil {
		return err
	}
	st, _, err := p.request(ctx, http.MethodDelete, p.kvPath("metadata", owner, ref), nil)
	if err != nil {
		return err
	}
	if st != http.StatusOK && st != http.StatusNoContent {
		return baoStatusErr("delete", st)
	}
	return nil
}

// List walks the KV tree under the owner root and returns every leaf path
// that starts with prefix.
func (p *baoProvider) List(ctx context.Context, owner, prefix string) ([]string, error) {
	var out []string
	var walk func(dir string) error
	walk = func(dir string) error {
		path := "/v1/" + p.mount + "/metadata/" + escapePath(storagePath(owner, dir))
		path = strings.TrimRight(path, "/")
		st, data, err := p.request(ctx, "LIST", path, nil)
		if err != nil {
			return err
		}
		if st == http.StatusNotFound {
			return nil
		}
		if st != http.StatusOK {
			return fmt.Errorf("bao list: status %d", st)
		}
		var resp struct {
			Data struct {
				Keys []string `json:"keys"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return fmt.Errorf("bao list: %w", err)
		}
		for _, k := range resp.Data.Keys {
			if strings.HasSuffix(k, "/") {
				if err := walk(dir + k); err != nil {
					return err
				}
				continue
			}
			if full := dir + k; strings.HasPrefix(full, prefix) {
				out = append(out, full)
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	return out, nil
}
