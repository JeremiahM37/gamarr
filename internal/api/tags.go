package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleGetTags handles GET /api/tags.
func (s *Server) handleGetTags(w http.ResponseWriter, r *http.Request) {
	tags := s.mgr.Jobs().GetTags()
	if tags == nil {
		tags = []db.Tag{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"tags":    tags,
	})
}

// handleCreateTag handles POST /api/tags.
func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	id, err := s.mgr.Jobs().AddTag(req.Name, req.Color)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create tag: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

// handleDeleteTag handles DELETE /api/tags/{id}.
func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid tag ID")
		return
	}
	if err := s.mgr.Jobs().DeleteTag(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete tag")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleAddItemTag handles POST /api/library/{id}/tags.
func (s *Server) handleAddItemTag(w http.ResponseWriter, r *http.Request) {
	itemID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid item ID")
		return
	}
	var req struct {
		TagID int64 `json:"tag_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := s.mgr.Jobs().AddItemTag(itemID, req.TagID); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to add tag")
		return
	}
	tags := s.mgr.Jobs().GetItemTags(itemID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "tags": tags})
}

// handleRemoveItemTag handles DELETE /api/library/{id}/tags/{tagID}.
func (s *Server) handleRemoveItemTag(w http.ResponseWriter, r *http.Request) {
	itemID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid item ID")
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tagID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid tag ID")
		return
	}
	if err := s.mgr.Jobs().RemoveItemTag(itemID, tagID); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to remove tag")
		return
	}
	tags := s.mgr.Jobs().GetItemTags(itemID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "tags": tags})
}

// handleGetItemTags handles GET /api/library/{id}/tags.
func (s *Server) handleGetItemTags(w http.ResponseWriter, r *http.Request) {
	itemID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid item ID")
		return
	}
	tags := s.mgr.Jobs().GetItemTags(itemID)
	if tags == nil {
		tags = []db.Tag{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "tags": tags})
}
