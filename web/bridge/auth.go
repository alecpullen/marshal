package bridge

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerAuth wraps next so that every /api route requires
// "Authorization: Bearer <token>". The comparison is constant-time and
// the token is never logged, never echoed in responses, and never
// placed in URLs or SSE payloads. Non-/api routes (the embedded SPA)
// pass through unauthenticated.
func bearerAuth(token string, next http.Handler) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) {
			unauthorized(w)
			return
		}
		got := []byte(strings.TrimPrefix(h, prefix))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unauthorized rejects the request without revealing why beyond the
// RFC 6750 challenge.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="webbridge"`)
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}
