package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// enrollmentStub is a bridge's enrollment route as a server profile serves it:
// behind the shared bearer token, answering the connected body this client
// reads. It records the Authorization header of every request it answers.
func enrollmentStub(t *testing.T, wantToken string) (http.Handler, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []string
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != marshalEnrollPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("enrollment method = %s, want POST", r.Method)
		}
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		if wantToken != "" && auth != "Bearer "+wantToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"state":"connected","instanceId":%q,"generation":"7:8",`+
			`"projectId":"project-1","sessionId":"s1","token":"scoped-credential",`+
			`"expiresAt":"2100-01-01T00:00:00Z"}`, "https://studio.example")
	})
	return handler, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// TestMarshalEnrollmentRemoteRequiresAuth proves an explicitly configured remote
// enrolls over its authenticated TLS path or not at all: with no token source
// there is nothing to authenticate with, and the client reports unsupported
// rather than sending an unauthenticated request — or falling back to the local
// socket's credential.
func TestMarshalEnrollmentRemoteRequiresAuth(t *testing.T) {
	serverTLS, trustedTLS, _ := attachCertificates(t)
	handler, seen := enrollmentStub(t, "remote-token")
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = serverTLS
	srv.StartTLS()
	t.Cleanup(srv.Close)
	base := srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No token source at all: unsupported, and nothing is dialled.
	client, err := Remote(base, trustedTLS, nil)
	if err != nil {
		t.Fatalf("Remote(): %v", err)
	}
	enrollment, err := client.EnrollMarshal(ctx, "/work/one", "s1")
	if !errors.Is(err, ErrMarshalUnsupported) {
		t.Fatalf("a remote without a token source: err = %v, want unsupported", err)
	}
	if enrollment.Status.State != MarshalStateUnsupported {
		t.Fatalf("state = %q, want %q", enrollment.Status.State, MarshalStateUnsupported)
	}
	if enrollment.Token != "" {
		t.Fatal("an unauthenticated remote minted a credential")
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("the remote was sent %d requests with no credential, want none", len(got))
	}

	// A token source that yields nothing is the same answer.
	empty, err := Remote(base, trustedTLS, func(context.Context) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("Remote(): %v", err)
	}
	if _, err := empty.EnrollMarshal(ctx, "/work/one", "s1"); !errors.Is(err, ErrMarshalUnsupported) {
		t.Fatalf("an empty token: err = %v, want unsupported", err)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("the remote was sent %d requests with an empty token, want none", len(got))
	}

	// A token source that fails is unsupported too, not an unauthenticated try.
	failing, err := Remote(base, trustedTLS, func(context.Context) (string, error) {
		return "", errors.New("keychain locked")
	})
	if err != nil {
		t.Fatalf("Remote(): %v", err)
	}
	if _, err := failing.EnrollMarshal(ctx, "/work/one", "s1"); !errors.Is(err, ErrMarshalUnsupported) {
		t.Fatalf("a failing token source: err = %v, want unsupported", err)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("the remote was sent %d requests with a failing token, want none", len(got))
	}

	// With a token the same remote enrolls, and the credential is the bridge's.
	authed, err := Remote(base, trustedTLS, func(context.Context) (string, error) { return "remote-token", nil })
	if err != nil {
		t.Fatalf("Remote(): %v", err)
	}
	enrollment, err = authed.EnrollMarshal(ctx, "/work/one", "s1")
	if err != nil {
		t.Fatalf("EnrollMarshal(): %v", err)
	}
	if enrollment.Status.State != MarshalStateConnected || enrollment.Token != "scoped-credential" {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if enrollment.Status.ProjectID != "project-1" || enrollment.Status.InstanceID != "https://studio.example" {
		t.Fatalf("status = %+v", enrollment.Status)
	}
	if !enrollment.ExpiresAt.Equal(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %v", enrollment.ExpiresAt)
	}
	seenNow := seen()
	if len(seenNow) != 1 || seenNow[0] != "Bearer remote-token" {
		t.Fatalf("the bridge read %v, want one authenticated request", seenNow)
	}

	// A server profile that does not serve enrollment is unsupported: the
	// client does not retry over a broader path.
	bare := httptest.NewUnstartedServer(http.NotFoundHandler())
	bare.TLS = serverTLS
	bare.StartTLS()
	t.Cleanup(bare.Close)
	unsupported, err := Remote(bare.URL, trustedTLS, func(context.Context) (string, error) { return "remote-token", nil })
	if err != nil {
		t.Fatalf("Remote(): %v", err)
	}
	if _, err := unsupported.EnrollMarshal(ctx, "/work/one", "s1"); !errors.Is(err, ErrMarshalUnsupported) {
		t.Fatalf("a bridge without the route: err = %v, want unsupported", err)
	}
}

// TestMarshalEnrollmentAmbiguousOrOffline proves discovery never guesses and
// never fails the session that asked: two configured instances are ambiguous
// until one is selected, an instance that answers ambiguous grants nothing, and
// an absent daemon is a status rather than an error anyone has to handle.
func TestMarshalEnrollmentAmbiguousOrOffline(t *testing.T) {
	local := MarshalInstance{ID: "/state/daemon/marshal.sock", Generation: "1:2"}
	remote := MarshalInstance{ID: "https://studio.example", Generation: "3:4", Remote: true}

	// Two instances and no selection: nothing is enrolled, and the candidates
	// are not guessed between.
	if _, err := ResolveMarshalInstance([]MarshalInstance{local, remote}, ""); !errors.Is(err, ErrMarshalAmbiguous) {
		t.Fatalf("two instances: err = %v, want ambiguous", err)
	}
	// A selection is what settles it, and an unknown selection is unsupported
	// rather than a silent fallback.
	if got, err := ResolveMarshalInstance([]MarshalInstance{local, remote}, remote.ID); err != nil || got.ID != remote.ID {
		t.Fatalf("the selected instance = %+v, %v", got, err)
	}
	if _, err := ResolveMarshalInstance([]MarshalInstance{local, remote}, "elsewhere"); !errors.Is(err, ErrMarshalUnsupported) {
		t.Fatalf("an unknown selection: err = %v, want unsupported", err)
	}
	if got, err := ResolveMarshalInstance([]MarshalInstance{local}, ""); err != nil || got.ID != local.ID {
		t.Fatalf("the only instance = %+v, %v", got, err)
	}
	if _, err := ResolveMarshalInstance(nil, ""); !errors.Is(err, ErrMarshalUnavailable) {
		t.Fatalf("no instance: err = %v, want unavailable", err)
	}

	// An instance that answers ambiguous is reported as such, with the
	// candidates and no credential.
	ambiguousSocket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != marshalEnrollPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"ambiguous","sessionId":"s1","candidates":["project-1","project-2"]}`))
	}))
	enrollment, err := NewClient(ambiguousSocket).EnrollMarshal(context.Background(), "/work/one", "s1")
	if !errors.Is(err, ErrMarshalAmbiguous) {
		t.Fatalf("an ambiguous answer: err = %v, want ambiguous", err)
	}
	if enrollment.Token != "" || len(enrollment.Status.Candidates) != 2 {
		t.Fatalf("ambiguous enrollment = %+v", enrollment)
	}
	if enrollment.Status.State != MarshalStateAmbiguous {
		t.Fatalf("state = %q", enrollment.Status.State)
	}

	// An absent daemon is a status: no error escapes as a failure of the
	// session, and the answer arrives promptly rather than on a timeout.
	offline := shortSocket(t)
	started := time.Now()
	enrollment, err = NewClient(offline).EnrollMarshal(context.Background(), "/work/one", "s1")
	if !errors.Is(err, ErrMarshalUnavailable) {
		t.Fatalf("an absent daemon: err = %v, want unavailable", err)
	}
	if enrollment.Status.State != MarshalStateUnavailable {
		t.Fatalf("state = %q, want %q", enrollment.Status.State, MarshalStateUnavailable)
	}
	if enrollment.Token != "" {
		t.Fatal("an absent daemon minted a credential")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("an absent daemon took %v to answer", elapsed)
	}
	if _, err := NewClient(offline).DiscoverMarshal(context.Background()); !errors.Is(err, ErrMarshalUnavailable) {
		t.Fatalf("discovery of an absent daemon: err = %v, want unavailable", err)
	}

	// Discovery names an instance by its canonical socket path and its
	// generation, both from the daemon's own answer.
	liveSocket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.0","profile":"local","pid":99,` +
			`"startedAt":1700000000000,"socket":"` + local.ID + `","projects":["/work/one"]}`))
	}))
	instance, err := NewClient(liveSocket).DiscoverMarshal(context.Background())
	if err != nil {
		t.Fatalf("DiscoverMarshal(): %v", err)
	}
	if instance.ID != local.ID || instance.Generation != "99:1700000000000" || instance.Remote {
		t.Fatalf("instance = %+v", instance)
	}
	if resolved, err := ResolveMarshalInstance([]MarshalInstance{instance}, ""); err != nil || resolved.Generation != instance.Generation {
		t.Fatalf("the discovered instance = %+v, %v", resolved, err)
	}
}

// TestMarshalEnrollmentLocalSendsNoBearer proves the socket client enrolls with
// the socket itself as the credential: no bearer token is ever attached to a
// request that is not a configured remote's.
func TestMarshalEnrollmentLocalSendsNoBearer(t *testing.T) {
	var sawAuth bool
	socket := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != marshalEnrollPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"connected","instanceId":"/s.sock","generation":"1:2",` +
			`"projectId":"project-1","sessionId":"s1","token":"local-credential"}`))
	}))

	enrollment, err := NewClient(socket).EnrollMarshal(context.Background(), "/work/one", "s1")
	if err != nil {
		t.Fatalf("EnrollMarshal(): %v", err)
	}
	if enrollment.Status.State != MarshalStateConnected || enrollment.Token != "local-credential" {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if sawAuth {
		t.Fatal("the local socket's enrollment carried a bearer token")
	}
	if strings.Contains(enrollment.Status.InstanceID, "http") {
		t.Fatalf("the local instance was named as a URL: %q", enrollment.Status.InstanceID)
	}
}
