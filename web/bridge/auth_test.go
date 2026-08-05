package bridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	const token = "s3cret-token"
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := bearerAuth(token, stub)

	tests := []struct {
		name   string
		path   string
		header string
		want   int
	}{
		{"no header", "/api/sessions", "", http.StatusUnauthorized},
		{"wrong token", "/api/sessions", "Bearer wrong", http.StatusUnauthorized},
		{"wrong scheme", "/api/sessions", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"valid token", "/api/sessions", "Bearer " + token, http.StatusOK},
		{"non-api path passes through", "/index.html", "", http.StatusOK},
		{"events stream guarded", "/api/events", "", http.StatusUnauthorized},
		{"bare /api not guarded", "/api", "", http.StatusOK},
		{"/apix prefix not guarded", "/apix/sessions", "", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
					t.Errorf("missing WWW-Authenticate challenge: %q", got)
				}
				if strings.Contains(rec.Body.String(), token) {
					t.Error("401 body leaks the token")
				}
			}
		})
	}
}
