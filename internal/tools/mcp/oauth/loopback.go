package oauth

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// loopbackReadyTimeout bounds how long the listener takes to be ready
// after Listen returns. The OS hands back a socket synchronously, so this
// is effectively a sanity bound — but it caps pathological cases where the
// socket has been hijacked or the OS is starved.
const loopbackReadyTimeout = 5 * time.Second

// LoopbackResult is what the receiver hands back after the user (or
// attacker) completes (or attempts) the OAuth callback.
type LoopbackResult struct {
	Code  string
	State string
	Err   error
}

// Loopback binds a fresh 127.0.0.1 listener on a random port and waits
// for a single authorization-code callback. The returned RedirectURL is
// the URL the user agent should be redirected to.
//
// The function returns when:
//   - the callback arrives (returns nil),
//   - ctx is cancelled (returns ctx.Err wrapped),
//   - the supplied timeout elapses (returns an error),
//   - Listen fails (returns the error immediately).
//
// On success, the HTTP listener is closed before the function returns, so
// the bound port is released. The caller MUST then immediately call
// token-exchange and persist the result; the receiver itself does not
// perform that step so a cancellation can leave zero partial state.
//
// Done is read once by the engine. Callers MUST signal Close when they
// have finished with Done so the listener can be released promptly and
// the cleanup goroutine can terminate.
type Loopback struct {
	RedirectURI string
	Done        chan LoopbackResult

	doneOnce sync.Once
	closeCh  chan struct{}
	closeFn  func()
}

// Close releases the listener. It is idempotent and safe to call from
// any goroutine.
func (lb *Loopback) Close() {
	lb.doneOnce.Do(func() {
		close(lb.closeCh)
	})
}

// PreferredLoopbackPort is the fixed loopback port Authorize tries first.
// A stable redirect_uri is friendlier to authorization servers that
// pre-register it; when the port is already in use the caller falls back
// to an ephemeral one.
const PreferredLoopbackPort = 53682

// StartLoopback binds a 127.0.0.1 listener on an ephemeral port and waits
// for the callback. The returned Loopback.RedirectURI is safe to embed in
// the authorization request; it carries the actual bound port.
func StartLoopback(ctx context.Context, timeout time.Duration) (*Loopback, error) {
	return StartLoopbackOnPort(ctx, 0, timeout)
}

// StartLoopbackOnPort binds a 127.0.0.1 listener on the given port (0 asks
// the kernel for a free one) and waits for the callback.
func StartLoopbackOnPort(ctx context.Context, port int, timeout time.Duration) (*Loopback, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("oauth: bind loopback listener: %w", err)
	}

	boundPort := ln.Addr().(*net.TCPAddr).Port
	resultCh := make(chan LoopbackResult, 1)
	closeCh := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := LoopbackResult{}
		if errMsg := q.Get("error"); errMsg != "" {
			desc := q.Get("error_description")
			if desc != "" {
				res.Err = fmt.Errorf("oauth: authorization server returned %s: %s", errMsg, desc)
			} else {
				res.Err = fmt.Errorf("oauth: authorization server returned %s", errMsg)
			}
			writeLoopbackHTML(w, false, errMsg)
			select {
			case resultCh <- res:
			default:
			}
			return
		}
		res.Code = q.Get("code")
		res.State = q.Get("state")
		if res.Code == "" {
			res.Err = fmt.Errorf("oauth: callback missing %q parameter", "code")
			writeLoopbackHTML(w, false, "missing code")
		} else {
			writeLoopbackHTML(w, true, "")
		}
		select {
		case resultCh <- res:
		default:
		}
	})
	// Reject anything other than /callback on the loopback port — this is
	// the only legitimate redirect target.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: loopbackReadyTimeout,
		IdleTimeout:       100 * time.Millisecond,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(ln)
	}()

	go func() {
		defer cancel()
		defer func() {
			// Give Serve a moment to unwind so the port is actually
			// released by the time we return.
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			_ = srv.Shutdown(shutdownCtx)
			wg.Wait()
		}()
		// Tear down either when the supplied ctx fires (timeout /
		// caller cancel) OR when the caller signals Close. We do
		// not consume from resultCh here — the engine is the
		// unique reader so the callback result is not lost.
		select {
		case <-ctx.Done():
			select {
			case resultCh <- LoopbackResult{Err: ctx.Err()}:
			default:
			}
		case <-closeCh:
			// Engine already consumed resultCh.
		}
	}()

	return &Loopback{
		RedirectURI: fmt.Sprintf("http://127.0.0.1:%d/callback", boundPort),
		Done:        resultCh,
		closeCh:     closeCh,
	}, nil
}

// writeLoopbackHTML renders the page shown in the user's browser once the
// authorization callback arrives. We keep this self-contained (no
// external CSS/JS) so it works even when the browser is sandboxed or the
// user is offline.
func writeLoopbackHTML(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if ok {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Authorization complete</title></head><body style="font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;"><h1>Authorization complete</h1><p>You can close this tab and return to Marshal.</p></body></html>`))
		return
	}
	body := fmt.Sprintf(`<!doctype html><html><head><title>Authorization failed</title></head><body style="font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;"><h1>Authorization failed</h1><p>%s</p><p>Return to Marshal to retry.</p></body></html>`, html.EscapeString(msg))
	_, _ = w.Write([]byte(body))
}

// buildAuthorizeURL composes the authorization request URL from the
// authorization endpoint, redirect URI, PKCE challenge, and state. Any
// caller-provided scopes are joined with a single space per RFC 6749 §3.3.
func buildAuthorizeURL(authorizationEndpoint, redirectURI, clientID string, scopes []string, pkce PKCE, state string) (string, error) {
	u, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("oauth: parse authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", pkce.Method)
	q.Set("state", state)
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
