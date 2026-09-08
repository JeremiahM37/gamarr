package api

import (
	"context"
	"errors"
	"net/http"

	"gamarr/internal/minerva"
)

func (s *Server) minervaStatus(ctx context.Context) minerva.Status {
	if s.minerva == nil {
		return minerva.Status{}
	}
	return s.minerva.Status(ctx)
}

func minervaSourceStatus(status minerva.Status) string {
	switch {
	case !status.Enabled:
		return "not_configured"
	case status.Syncing:
		return "syncing"
	case status.LastError != "" || !status.Ready:
		return "degraded"
	default:
		return "ok"
	}
}

func (s *Server) handleMinervaStatus(w http.ResponseWriter, r *http.Request) {
	if s.minerva == nil {
		// time.Time's zero value is not omitted by encoding/json's omitempty.
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false, "ready": false, "syncing": false, "collections": 0, "files": 0})
		return
	}
	writeJSON(w, http.StatusOK, s.minerva.Status(r.Context()))
}

func (s *Server) handleMinervaSync(w http.ResponseWriter, r *http.Request) {
	if s.minerva == nil {
		writeError(w, http.StatusBadRequest, "Minerva is not enabled")
		return
	}
	var req struct {
		Full bool `json:"full"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	// Claim the service guard before replying. The job outlives the HTTP request;
	// Service.Close cancels and joins it during application shutdown.
	err := s.minerva.StartSync(context.Background(), req.Full)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true})
	case errors.Is(err, minerva.ErrSyncInProgress):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
