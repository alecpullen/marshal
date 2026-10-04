package bridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// EgressSidecar is the client side of the control link: it runs inside
// the marshal-egress container, applies each policy the bridge streams,
// ships connection records back, and fetches leaf certificates. It holds
// no CA key.
type EgressSidecar struct {
	proxy  *EgressProxy
	client *http.Client
	base   string // http://egress-control, always over the dialer below

	records chan EgressRecord

	mu    sync.Mutex
	leafs map[string]*tls.Certificate
}

// controlClient returns an HTTP client that reaches the bridge through
// the control address: "unix:///path" or "tcp://host:port".
func controlClient(addr string) (*http.Client, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	var network, target string
	switch u.Scheme {
	case "unix":
		network, target = "unix", u.Path
	case "tcp":
		network, target = "tcp", u.Host
	default:
		return nil, fmt.Errorf("control address %q: expected unix:///path or tcp://host:port", addr)
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, target)
		},
	}}, nil
}

// NewEgressSidecar builds the sidecar for the given control address.
func NewEgressSidecar(control string) (*EgressSidecar, error) {
	c, err := controlClient(control)
	if err != nil {
		return nil, err
	}
	s := &EgressSidecar{client: c, base: "http://egress-control", records: make(chan EgressRecord, 1024), leafs: map[string]*tls.Certificate{}}
	s.proxy = NewEgressProxy(s.leaf, s.records, s.reportBlocked, nil)
	return s, nil
}

// Proxy returns the sidecar's proxy, for serving.
func (s *EgressSidecar) Proxy() *EgressProxy { return s.proxy }

// leaf fetches (and caches) a leaf from the bridge.
func (s *EgressSidecar) leaf(workspace, host string) (*tls.Certificate, error) {
	key := workspace + "|" + host
	s.mu.Lock()
	if c, ok := s.leafs[key]; ok && c.Leaf != nil && time.Now().Before(c.Leaf.NotAfter.Add(-leafRefreshMargin)) {
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		s.base+"/leaf?workspace="+url.QueryEscape(workspace)+"&host="+url.QueryEscape(host), nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("leaf for %s: status %d", host, resp.StatusCode)
	}
	var lr leafResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&lr); err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(lr.Cert), []byte(lr.Key))
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		blk, _ := pem.Decode([]byte(lr.Cert))
		if blk == nil {
			return nil, fmt.Errorf("leaf for %s: no certificate", host)
		}
		if cert.Leaf, err = x509.ParseCertificate(blk.Bytes); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.leafs[key] = &cert
	s.mu.Unlock()
	return &cert, nil
}

func (s *EgressSidecar) post(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("POST %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// reportBlocked tells the bridge about a refused request without
// holding up the proxy.
func (s *EgressSidecar) reportBlocked(agentID, host string) {
	go func() {
		if err := s.post("/blocked", map[string]string{"agentId": agentID, "host": host}); err != nil {
			slog.Default().Warn("egress: report blocked failed", "err", err)
		}
	}()
}

// Run applies policy updates and ships records until ctx ends. It
// reconnects to the policy stream whenever it drops. Until a policy has
// arrived the proxy refuses everything.
func (s *EgressSidecar) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.shipRecords(ctx) }()
	backoff := time.Second
	for ctx.Err() == nil {
		err := s.streamPolicy(ctx)
		if ctx.Err() != nil {
			break
		}
		slog.Default().Warn("egress: policy stream ended", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
	wg.Wait()
}

func (s *EgressSidecar) streamPolicy(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/policy", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("policy stream: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var pol EgressPolicy
		if err := json.Unmarshal(sc.Bytes(), &pol); err != nil {
			return err
		}
		s.proxy.SetPolicy(pol)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// shipRecords posts batches once a second (or at 200 records), and
// drains on shutdown.
func (s *EgressSidecar) shipRecords(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var batch []EgressRecord
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.post("/log", batch); err != nil {
			slog.Default().Warn("egress: ship records failed", "err", err, "records", len(batch))
			if len(batch) > 5000 {
				batch = batch[len(batch)-5000:] // bound memory while the bridge is away
			}
			return
		}
		batch = nil
	}
	for {
		select {
		case r := <-s.records:
			batch = append(batch, r)
			if len(batch) >= 200 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for len(s.records) > 0 {
				batch = append(batch, <-s.records)
			}
			flush()
			return
		}
	}
}

// ServeEgress runs the sidecar: listen on addr and proxy until ctx ends.
// It backs the `webbridge egress` subcommand.
func ServeEgress(ctx context.Context, listen, control string) error {
	sc, err := NewEgressSidecar(control)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: sc.Proxy(), ReadHeaderTimeout: 10 * time.Second}
	go sc.Run(ctx)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
