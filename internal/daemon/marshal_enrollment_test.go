package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMarshalEnrollmentAmbiguousOrOffline proves discovery never guesses and
// never fails the session that asked: two configured instances are ambiguous
// until one is selected, an instance that answers ambiguous grants nothing, and
// an absent daemon is a status rather than an error anyone has to handle.
func TestMarshalEnrollmentAmbiguousOrOffline(t *testing.T) {
	local := MarshalInstance{ID: "/state/daemon/marshal.sock", Generation: "1:2"}
	remote := MarshalInstance{ID: "https://studio.example", Generation: "3:4"}

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
	if instance.ID != local.ID || instance.Generation != "99:1700000000000" {
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
