package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"gamarr/internal/db"

	"github.com/pquerna/otp/totp"
)

// generateBackupCodes creates n random 8-digit backup codes.
func generateBackupCodes(n int) []string {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, 4)
		rand.Read(b)
		codes[i] = fmt.Sprintf("%08d", int(b[0])<<24|int(b[1])<<16|int(b[2])<<8|int(b[3]))[:8]
	}
	return codes
}

// hashBackupCode creates a SHA-256 hash of a backup code.
func hashBackupCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// handleTOTPSetup handles POST /api/totp/setup — generates a TOTP secret and backup codes.
func handleTOTPSetup(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := getUserIDFromContext(r)
		if userID == 0 {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		user, err := database.GetUser(userID)
		if err != nil {
			writeError(w, http.StatusNotFound, "User not found")
			return
		}

		if user.TOTPEnabled {
			writeError(w, http.StatusBadRequest, "TOTP is already enabled")
			return
		}

		key, err := totp.Generate(totp.GenerateOpts{
			Issuer:      "Gamarr",
			AccountName: user.Username,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to generate TOTP secret")
			return
		}

		if err := database.SetTOTPSecret(userID, key.Secret()); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to save TOTP secret")
			return
		}

		// Generate backup codes
		backupCodes := generateBackupCodes(8)
		var hashes []string
		for _, code := range backupCodes {
			hashes = append(hashes, hashBackupCode(code))
		}
		if err := database.SaveBackupCodes(userID, hashes); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to save backup codes")
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":      true,
			"secret":       key.Secret(),
			"url":          key.URL(),
			"backup_codes": backupCodes,
		})
	}
}

// handleTOTPVerify handles POST /api/totp/verify — validates a TOTP code and enables 2FA.
func handleTOTPVerify(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := getUserIDFromContext(r)
		if userID == 0 {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		user, err := database.GetUser(userID)
		if err != nil {
			writeError(w, http.StatusNotFound, "User not found")
			return
		}

		if user.TOTPSecret == "" {
			writeError(w, http.StatusBadRequest, "TOTP not set up — call /api/totp/setup first")
			return
		}

		if !totp.Validate(req.Code, user.TOTPSecret) {
			writeError(w, http.StatusUnauthorized, "Invalid TOTP code")
			return
		}

		if err := database.EnableTOTP(userID); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to enable TOTP")
			return
		}

		database.LogAuthActivity(user.Username, "totp_enabled", user.Username, "TOTP enabled")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "TOTP enabled"})
	}
}

// handleTOTPDisable handles POST /api/totp/disable — requires a valid TOTP code.
func handleTOTPDisable(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := getUserIDFromContext(r)
		if userID == 0 {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		user, err := database.GetUser(userID)
		if err != nil {
			writeError(w, http.StatusNotFound, "User not found")
			return
		}

		if !user.TOTPEnabled {
			writeError(w, http.StatusBadRequest, "TOTP is not enabled")
			return
		}

		if !totp.Validate(req.Code, user.TOTPSecret) {
			writeError(w, http.StatusUnauthorized, "Invalid TOTP code")
			return
		}

		if err := database.DisableTOTP(userID); err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to disable TOTP")
			return
		}

		database.LogAuthActivity(user.Username, "totp_disabled", user.Username, "TOTP disabled")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "TOTP disabled"})
	}
}

// handleTOTPStatus handles GET /api/totp/status.
func handleTOTPStatus(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := getUserIDFromContext(r)
		if userID == 0 {
			writeError(w, http.StatusUnauthorized, "Authentication required")
			return
		}

		user, err := database.GetUser(userID)
		if err != nil {
			writeError(w, http.StatusNotFound, "User not found")
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": user.TOTPEnabled,
		})
	}
}
