//go:build probe_c

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// spikeTimeout bounds every probe HTTP call. The probe is interactive, so a
// hung request should fail loudly rather than block a terminal.
const spikeTimeout = 30 * time.Second

// newSpikeClient returns the shared unauthenticated HTTP client. Auth is
// attached per-request by authedRequest so a token is never baked into a
// client that outlives the call.
func newSpikeClient() *http.Client {
	return &http.Client{Timeout: spikeTimeout}
}

// authedRequest builds a request against url with the codex header set:
// Authorization: Bearer <tok>, originator, and (when accountID is non-empty)
// ChatGPT-Account-ID. body may be nil.
//
// Header names come from constants.go; see the source notes for provenance.
func authedRequest(ctx context.Context, tok, accountID, method, url string, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(codexParamOriginator, codexOriginator)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if accountID != "" {
		req.Header.Set(codexAccountIDHeader, accountID)
	}
	return req, nil
}

// dumpResponse prints the status line, every response header (sorted for
// stable diffing), and the body. The probe's whole job is observation, so
// nothing is filtered here.
func dumpResponse(w io.Writer, resp *http.Response, body []byte) {
	fmt.Fprintf(w, "HTTP %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	names := make([]string, 0, len(resp.Header))
	for name := range resp.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range resp.Header.Values(name) {
			fmt.Fprintf(w, "%s: %s\n", name, value)
		}
	}
	fmt.Fprintln(w, "--- body ---")
	fmt.Fprintln(w, string(body))
}

// redactToken renders a token for identification only: first 12 characters
// plus an ellipsis. The probe must never print full token material.
func redactToken(tok string) string {
	if tok == "" {
		return "(none)"
	}
	if len(tok) <= 12 {
		return tok[:1] + "..."
	}
	return tok[:12] + "..."
}

// headerValue returns the first value of a response header, case-insensitively.
func headerValue(resp *http.Response, name string) string {
	return resp.Header.Get(name)
}

// matchingHeaders returns "Name: value" lines for every header whose name
// matches any of the supplied lowercase substrings.
func matchingHeaders(h http.Header, needles []string) []string {
	out := []string{}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lower := strings.ToLower(name)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				for _, value := range h.Values(name) {
					out = append(out, fmt.Sprintf("%s: %s", name, value))
				}
				break
			}
		}
	}
	return out
}
