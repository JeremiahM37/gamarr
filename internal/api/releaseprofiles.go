package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleGetReleaseProfiles handles GET /api/release-profiles.
func (s *Server) handleGetReleaseProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := s.mgr.Jobs().GetReleaseProfiles()
	if profiles == nil {
		profiles = []db.ReleaseProfile{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"profiles": profiles,
	})
}

// handleCreateReleaseProfile handles POST /api/release-profiles.
func (s *Server) handleCreateReleaseProfile(w http.ResponseWriter, r *http.Request) {
	var p db.ReleaseProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if p.Name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	if p.MustContain == nil {
		p.MustContain = []string{}
	}
	if p.MustNotContain == nil {
		p.MustNotContain = []string{}
	}
	if p.Preferred == nil {
		p.Preferred = []db.PreferredWord{}
	}
	id, err := s.mgr.Jobs().AddReleaseProfile(&p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create profile: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

// handleUpdateReleaseProfile handles PUT /api/release-profiles/{id}.
func (s *Server) handleUpdateReleaseProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid profile ID")
		return
	}
	var p db.ReleaseProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	p.ID = id
	if err := s.mgr.Jobs().UpdateReleaseProfile(&p); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleDeleteReleaseProfile handles DELETE /api/release-profiles/{id}.
func (s *Server) handleDeleteReleaseProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid profile ID")
		return
	}
	if err := s.mgr.Jobs().DeleteReleaseProfile(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete profile")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
