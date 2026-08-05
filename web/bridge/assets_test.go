package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Marshal Web UI") {
		t.Errorf("index.html body missing title; got %q", body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("index.html cache-control = %q, want no-cache", cc)
	}
}

func TestStaticHandlerApiNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/sessions on static handler: status = %d, want 404", rec.Code)
	}
}

func TestStaticHandlerFallbackToIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sessions/s-1", nil)
	staticHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sessions/s-1: status = %d, want 200 (index fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Marshal Web UI") {
		t.Errorf("fallback did not serve index.html")
	}
}
