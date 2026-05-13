package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/webhook"
)

// handleGetWebhooks handles GET /api/webhooks.
func (s *Server) handleGetWebhooks(w http.ResponseWriter, r *http.Request) {
	configs := s.mgr.Jobs().GetWebhooks()
	if configs == nil {
		configs = []webhook.WebhookConfig{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"webhooks": configs,
	})
}

// handleAddWebhook handles POST /api/webhooks.
func (s *Server) handleAddWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Type   string `json:"type"`
		Events string `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeError(w, http.StatusBadRequest, "Name and URL are required")
		return
	}

	id, err := s.mgr.Jobs().AddWebhook(req.Name, req.URL, req.Type, req.Events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to add webhook")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"id":      id,
	})
}

// handleDeleteWebhook handles DELETE /api/webhooks/{id}.
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid webhook ID")
		return
	}
	if err := s.mgr.Jobs().DeleteWebhook(id); err != nil {
		writeError(w, http.StatusNotFound, "Webhook not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleTestWebhook handles POST /api/webhooks/test.
func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   *int64 `json:"id"`
		URL  string `json:"url"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	var cfg webhook.WebhookConfig
	if req.ID != nil {
		stored, err := s.mgr.Jobs().GetWebhook(*req.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, "Webhook not found")
			return
		}
		cfg = *stored
	} else if req.URL != "" {
		cfg = webhook.WebhookConfig{
			Name: "Test",
			URL:  req.URL,
			Type: req.Type,
		}
		if cfg.Type == "" {
			cfg.Type = "generic"
		}
	} else {
		writeError(w, http.StatusBadRequest, "Provide webhook ID or URL")
		return
	}

	if err := webhook.SendTest(cfg); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
