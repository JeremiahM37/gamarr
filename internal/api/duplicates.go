package api

import (
	"net/http"
	"strings"
)

// handleCheckLibrary handles GET /api/library/check?title=X&platform=Y.
// Returns whether a game exists in the library.
func (s *Server) handleCheckLibrary(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	platform := strings.TrimSpace(r.URL.Query().Get("platform"))

	if title == "" {
		writeError(w, http.StatusBadRequest, "title parameter is required")
		return
	}

	item := s.mgr.Jobs().FindLibraryByTitle(title, platform)
	if item != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"exists": true,
			"item":   item,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"exists": false,
		"item":   nil,
	})
}
