package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleAddHistory handles POST /api/history.
func (s *Server) handleAddHistory(w http.ResponseWriter, r *http.Request) {
	var entry db.PlayHistoryEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if entry.GameTitle == "" {
		writeError(w, http.StatusBadRequest, "game_title is required")
		return
	}

	id, err := s.mgr.Jobs().AddPlayHistory(&entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to add play history")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"id":      id,
	})
}

// handleGetHistory handles GET /api/history.
func (s *Server) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "default"
	}
	status := r.URL.Query().Get("status") // "playing", "finished", or "" for all

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	entries, total, err := s.mgr.Jobs().GetPlayHistory(userID, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get play history")
		return
	}
	if entries == nil {
		entries = []db.PlayHistoryEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"entries": entries,
		"total":   total,
	})
}

// handleUpdateHistory handles PATCH /api/history/{id}.
func (s *Server) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid history ID")
		return
	}

	var fields map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := s.mgr.Jobs().UpdatePlayHistory(id, fields); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update play history")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleDeleteHistory handles DELETE /api/history/{id}.
func (s *Server) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid history ID")
		return
	}

	if err := s.mgr.Jobs().DeletePlayHistory(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete play history")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleHistoryStats handles GET /api/history/stats.
func (s *Server) handleHistoryStats(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "default"
	}

	stats := s.mgr.Jobs().GetPlayHistoryStats(userID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"stats":   stats,
	})
}
