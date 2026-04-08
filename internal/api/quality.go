package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleGetQualityProfiles handles GET /api/quality-profiles.
func (s *Server) handleGetQualityProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := s.mgr.Jobs().GetQualityProfiles()
	if profiles == nil {
		profiles = []db.QualityProfile{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"profiles": profiles,
	})
}

// handleCreateQualityProfile handles POST /api/quality-profiles.
func (s *Server) handleCreateQualityProfile(w http.ResponseWriter, r *http.Request) {
	var p db.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	if p.SourceRanking == nil {
		p.SourceRanking = []string{}
	}
	id, err := s.mgr.Jobs().AddQualityProfile(&p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create profile: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

// handleUpdateQualityProfile handles PUT /api/quality-profiles/{id}.
func (s *Server) handleUpdateQualityProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid profile ID")
		return
	}
	var p db.QualityProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	p.ID = id
	if p.SourceRanking == nil {
		p.SourceRanking = []string{}
	}
	if err := s.mgr.Jobs().UpdateQualityProfile(&p); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleDeleteQualityProfile handles DELETE /api/quality-profiles/{id}.
func (s *Server) handleDeleteQualityProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid profile ID")
		return
	}
	if err := s.mgr.Jobs().DeleteQualityProfile(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
