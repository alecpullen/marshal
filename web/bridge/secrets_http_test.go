package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func testSecretsServer(t *testing.T) (*Server, *Fleet) {
	t.Helper()
	f := testFleetWithAudit(t)
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err := GenerateKeyFile(key); err != nil {
		t.Fatal(err)
	}
	p, err := NewLocalProvider(filepath.Join(root, "state"), key)
	if err != nil {
		t.Fatal(err)
	}
	f.SetSecrets(p)
	return NewServer(f, ""), f
}

func TestSecretsHTTPRoundTripNeverReturnsValues(t *testing.T) {
	s, f := testSecretsServer(t)
	const value = "ghp_supersecret"
	rec := doReq(t, s, http.MethodPut, "/api/secrets/vault:git/github", map[string]string{"value": value}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	// The vault: prefix is optional in the path.
	if rec := doReq(t, s, http.MethodPut, "/api/secrets/providers/x", map[string]string{"value": "k"}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT bare = %d %s", rec.Code, rec.Body)
	}
	rec = doReq(t, s, http.MethodGet, "/api/secrets", nil, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), value) {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	var list struct{ Refs []string }
	decodeBody(t, rec, &list)
	if len(list.Refs) != 2 || list.Refs[0] != "vault:git/github" {
		t.Fatalf("refs = %v", list.Refs)
	}
	rec = doReq(t, s, http.MethodGet, "/api/secrets?prefix=vault:providers/", nil, nil)
	decodeBody(t, rec, &list)
	if len(list.Refs) != 1 || list.Refs[0] != "vault:providers/x" {
		t.Fatalf("prefixed refs = %v", list.Refs)
	}
	// Audit holds the ref and never the value.
	var set *AuditEvent
	for _, e := range auditTail(t, f) {
		e := e
		if e.Event == AuditSecretSet && e.Detail == "vault:git/github" {
			set = &e
		}
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), value) {
			t.Fatalf("audit leaked the value: %s", b)
		}
	}
	if set == nil {
		t.Fatal("no secret_set audit entry")
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/secrets/vault:git/github", nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if findEvent(auditTail(t, f), AuditSecretDeleted) == nil {
		t.Fatal("no secret_deleted audit entry")
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/secrets/vault:git/github", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d", rec.Code)
	}
}

func TestSecretsHTTPStatus(t *testing.T) {
	s, _ := testSecretsServer(t)
	rec := doReq(t, s, http.MethodGet, "/api/secrets/status", nil, nil)
	var st map[string]any
	decodeBody(t, rec, &st)
	if st["backend"] != "local" || st["healthy"] != true {
		t.Fatalf("status = %v", st)
	}
	// The default backend is env, read-only.
	f := testFleet(t)
	s2 := NewServer(f, "")
	rec = doReq(t, s2, http.MethodGet, "/api/secrets/status", nil, nil)
	decodeBody(t, rec, &st)
	if st["backend"] != "env" {
		t.Fatalf("default status = %v", st)
	}
	rec = doReq(t, s2, http.MethodPut, "/api/secrets/vault:x", map[string]string{"value": "v"}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT on env backend = %d, want 409", rec.Code)
	}
}

func TestSecretsHTTPRejectsBadRefsAndBodies(t *testing.T) {
	s, _ := testSecretsServer(t)
	for _, path := range []string{"/api/secrets/vault:a/../b", "/api/secrets/vault:", "/api/secrets/a//b"} {
		rec := doReq(t, s, http.MethodPut, path, map[string]string{"value": "v"}, nil)
		if rec.Code == http.StatusNoContent {
			t.Errorf("PUT %s accepted", path)
		}
	}
	if rec := doReq(t, s, http.MethodPut, "/api/secrets/vault:a", map[string]string{"value": ""}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty value = %d", rec.Code)
	}
}

func TestSecretsHTTPReservesCAAndScopesCredentials(t *testing.T) {
	s, f := testSecretsServer(t)
	f.secrets.Put(context.Background(), DefaultOwnerID, "ca/dev", []byte("pem"))
	f.secrets.Put(context.Background(), DefaultOwnerID, "git/ok", []byte("tok"))
	if c := doReq(t, s, http.MethodPut, "/api/secrets/vault:ca/dev", map[string]string{"value": "x"}, nil).Code; c != http.StatusBadRequest {
		t.Fatalf("PUT ca/ = %d", c)
	}
	if c := doReq(t, s, http.MethodDelete, "/api/secrets/vault:ca/dev", nil, nil).Code; c != http.StatusBadRequest {
		t.Fatalf("DELETE ca/ = %d", c)
	}
	rec := doReq(t, s, http.MethodGet, "/api/secrets", nil, nil)
	if strings.Contains(rec.Body.String(), "ca/") || !strings.Contains(rec.Body.String(), "git/ok") {
		t.Fatalf("list = %s", rec.Body)
	}
	if _, err := parseCredentialRef("vault:providers/openai"); err == nil {
		t.Fatal("credential ref outside git/ accepted")
	}
	if _, err := parseInjectionRef("vault:ca/dev"); err == nil {
		t.Fatal("injection of a CA key accepted")
	}
}
