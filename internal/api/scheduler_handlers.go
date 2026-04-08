package api

import (
	"net/http"
)

// handleSchedulerStatus handles GET /api/scheduler/status.
func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
			"error":   "Scheduler not initialized",
		})
		return
	}
	writeJSON(w, http.StatusOK, s.scheduler.Status())
}

// handleSchedulerRun handles POST /api/scheduler/run — triggers a manual run.
func (s *Server) handleSchedulerRun(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeError(w, http.StatusInternalServerError, "Scheduler not initialized")
		return
	}
	s.scheduler.RunNow()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Scheduler run triggered",
	})
}
