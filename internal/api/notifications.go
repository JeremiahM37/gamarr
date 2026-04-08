package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/models"
)

// handleGetNotifications handles GET /api/notifications.
func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "default"
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}

	notifications, err := s.mgr.Jobs().GetUserNotifications(userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to get notifications")
		return
	}
	if notifications == nil {
		notifications = []models.Notification{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":       true,
		"notifications": notifications,
	})
}

// handleUnreadCount handles GET /api/notifications/unread.
func (s *Server) handleUnreadCount(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "default"
	}

	count, err := s.mgr.Jobs().UnreadCount(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to count notifications")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"count":   count,
	})
}

// handleMarkRead handles POST /api/notifications/{id}/read.
func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid notification ID")
		return
	}

	if err := s.mgr.Jobs().MarkNotificationRead(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to mark notification as read")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleMarkAllRead handles POST /api/notifications/read-all.
func (s *Server) handleMarkAllRead(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "default"
	}

	if err := s.mgr.Jobs().MarkAllNotificationsRead(userID); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to mark notifications as read")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
