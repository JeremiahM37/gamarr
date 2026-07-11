package api

import (
	"net/http"
	"testing"
)

// TestMalformedJSONBodies verifies that handlers reject malformed JSON
// request bodies with a 400 JSON error instead of silently ignoring them.
func TestMalformedJSONBodies(t *testing.T) {
	env := newTestEnv(t, nil)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"organize torrent", http.MethodPost, "/api/downloads/organize/abc123"},
		{"add ddl source", http.MethodPost, "/api/ddl-sources"},
		{"update settings", http.MethodPut, "/api/settings"},
		{"backup create", http.MethodPost, "/api/backup/create"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := env.do(tt.method, tt.path, `{"not json`)
			wantStatus(t, rr, http.StatusBadRequest)
			m := decodeMap(t, rr)
			if m["error"] == nil || m["error"] == "" {
				t.Errorf("expected error message in body, got %v", rr.Body.String())
			}
			if success, ok := m["success"].(bool); !ok || success {
				t.Errorf("expected success=false, got %v", m["success"])
			}
		})
	}
}

// TestUpdateSettings_EmptyBodyStillOK ensures the empty-body behavior of
// handlers using decodeJSONBody is preserved (defaults apply, no 400).
func TestUpdateSettings_EmptyBodyStillOK(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do(http.MethodPut, "/api/settings", "")
	wantStatus(t, rr, http.StatusOK)
}
