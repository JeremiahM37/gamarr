package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleGetBlocklist handles GET /api/blocklist.
func (s *Server) handleGetBlocklist(w http.ResponseWriter, r *http.Request) {
	entries := s.mgr.Jobs().GetBlocklist()
	if entries == nil {
		entries = []db.BlocklistEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"items":   entries,
		"total":   len(entries),
	})
}

// handleAddBlocklistEntry handles POST /api/blocklist.
func (s *Server) handleAddBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	var entry db.BlocklistEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if entry.DownloadURL == "" && entry.InfoHash == "" {
		writeError(w, http.StatusBadRequest, "Either download_url or info_hash is required")
		return
	}
	id, err := s.mgr.Jobs().AddBlocklistEntry(&entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to add to blocklist")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

// handleDeleteBlocklistEntry handles DELETE /api/blocklist/{id}.
func (s *Server) handleDeleteBlocklistEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid blocklist ID")
		return
	}
	if err := s.mgr.Jobs().DeleteBlocklistEntry(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete entry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleClearBlocklist handles DELETE /api/blocklist/clear.
func (s *Server) handleClearBlocklist(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.mgr.Jobs().ClearBlocklist()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to clear blocklist")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "deleted": deleted})
}
