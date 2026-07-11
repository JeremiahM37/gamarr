package api

import (
	"net/http"
	"strings"
)

// securityHeadersMiddleware adds standard security headers to all responses.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Strict CSP: no inline scripts anywhere (the UI uses external files +
		// event delegation, and the Tailwind runtime is vendored under /static/).
		//   style-src 'unsafe-inline' — the Tailwind Play runtime injects a
		//     <style> element at load; style injection, unlike script, is not
		//     an XSS vector on its own.
		//   img-src https: data: — game cover art may be hotlinked from
		//     external metadata sources.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: https:; font-src 'self'; connect-src 'self'; "+
				"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// sanitizeLog strips newlines from externally supplied values before they
// reach the log, preventing forged log entries.
func sanitizeLog(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// maxBodySize caps non-multipart request bodies at 1MB.
const maxBodySize = 1 << 20 // 1MB

// requestSizeLimitMiddleware caps non-multipart request bodies at 1MB.
// Requests whose declared Content-Length already exceeds the cap are
// rejected up front with 413; bodies without a declared length are wrapped
// in http.MaxBytesReader so oversized reads fail mid-decode (surfaced as
// 413 by decodeJSONBody).
func requestSizeLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType := r.Header.Get("Content-Type")
		if r.Body != nil && !strings.HasPrefix(contentType, "multipart/") {
			if r.ContentLength > maxBodySize {
				writeError(w, http.StatusRequestEntityTooLarge, "Request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		}
		next.ServeHTTP(w, r)
	})
}
