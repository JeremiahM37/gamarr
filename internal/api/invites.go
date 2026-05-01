package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
)

// handleListInvites handles GET /api/invites — admin only.
func handleListInvites(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		codes, err := database.ListInviteCodes()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to list invite codes")
			return
		}
		if codes == nil {
			codes = []map[string]interface{}{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"invites": codes,
		})
	}
}

// handleCreateInvite handles POST /api/invites — admin only.
func handleCreateInvite(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := getUserIDFromContext(r)

		var req struct {
			Role     string `json:"role"`
			MaxUses  int    `json:"max_uses"`
			ExpirySec int   `json:"expiry_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		if req.Role == "" {
			req.Role = "user"
		}
		if req.Role != "user" && req.Role != "admin" {
			writeError(w, http.StatusBadRequest, "Role must be 'user' or 'admin'")
			return
		}
		if req.MaxUses <= 0 {
			req.MaxUses = 1
		}

		// Generate random code.
		b := make([]byte, 16)
		rand.Read(b)
		code := hex.EncodeToString(b)

		var expiresAt *float64
		if req.ExpirySec > 0 {
			exp := float64(time.Now().Unix()) + float64(req.ExpirySec)
			expiresAt = &exp
		}

		id, err := database.CreateInviteCode(code, userID, req.Role, req.MaxUses, expiresAt)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to create invite code")
			return
		}

		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"success": true,
			"id":      id,
			"code":    code,
			"role":    req.Role,
			"max_uses": req.MaxUses,
		})
	}
}

// handleDeleteInvite handles DELETE /api/invites/{id} — admin only.
func handleDeleteInvite(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid invite ID")
			return
		}
		if err := database.DeleteInviteCode(id); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to delete invite code")
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
	}
}
