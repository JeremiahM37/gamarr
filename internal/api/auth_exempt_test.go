package api

import "testing"

// TestIsExempt locks in the auth-bypass paths so regressions on this list
// don't silently break Prowlarr/Sonarr discovery, AI-agent OpenAPI fetches,
// or e-reader OPDS browsing.
func TestIsExempt(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Health + readiness — Kubernetes / Docker / Tailscale probes.
		{"/health", true},
		{"/api/health", true},
		// Public discovery surfaces.
		{"/api/openapi.json", true},
		{"/metrics", true},
		// Login / register flow.
		{"/api/login", true},
		{"/api/login/totp", true},
		{"/api/register", true},
		{"/api/auth/status", true},
		{"/api/oidc/login", true},
		{"/api/oidc/callback", true},
		{"/api/oidc/status", true},
		// Torznab — Prowlarr sends its own ?apikey= which is checked inside
		// the Torznab handler, not by the gamarr auth middleware. r.URL.Path
		// doesn't include the query string at the HTTP layer.
		{"/torznab/api", true},
		{"/api", true}, // Prowlarr indexer-discovery probe alias
		// Static + UI root.
		{"/", true},
		{"/static/main.js", true},
		// Negative cases — these MUST require auth or it's a security bug.
		{"/api/search", false},
		{"/api/library", false},
		{"/api/wishlist", false},
		{"/api/users", false},
		{"/api/settings", false},
		{"/api/admin/dashboard", false},
		{"/api/admin/bulk/retry", false},
		{"/api/openapi.jsonx", false}, // typo / suffix attack
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := isExempt(tc.path)
			if got != tc.want {
				t.Errorf("isExempt(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
