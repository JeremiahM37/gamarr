package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/config"
	"gamarr/internal/db"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const (
	ctxUserID   contextKey = "userID"
	ctxUserRole contextKey = "userRole"
	ctxUsername contextKey = "username"
)

// SessionData holds session metadata.
type SessionData struct {
	UserID   int64
	Username string
	Role     string
	Expiry   time.Time
}

// SessionStore manages session-based authentication.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionData
	pending  map[string]*SessionData // TOTP-pending sessions (5-minute expiry)
}

// NewSessionStore creates a new session store with periodic cleanup.
func NewSessionStore() *SessionStore {
	s := &SessionStore{
		sessions: make(map[string]*SessionData),
		pending:  make(map[string]*SessionData),
	}
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			now := time.Now()
			s.mu.Lock()
			for token, data := range s.sessions {
				if now.After(data.Expiry) {
					delete(s.sessions, token)
				}
			}
			for token, data := range s.pending {
				if now.After(data.Expiry) {
					delete(s.pending, token)
				}
			}
			s.mu.Unlock()
		}
	}()
	return s
}

// Create generates a new session token for a user, valid for 24 hours.
func (s *SessionStore) Create(userID int64, username, role string) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.sessions[token] = &SessionData{
		UserID:   userID,
		Username: username,
		Role:     role,
		Expiry:   time.Now().Add(24 * time.Hour),
	}
	s.mu.Unlock()
	return token
}

// Get retrieves session data if the token is valid.
func (s *SessionStore) Get(token string) (*SessionData, bool) {
	s.mu.RLock()
	data, ok := s.sessions[token]
	s.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if time.Now().After(data.Expiry) {
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return nil, false
	}
	return data, true
}

// Valid checks if a session token is valid and not expired.
func (s *SessionStore) Valid(token string) bool {
	_, ok := s.Get(token)
	return ok
}

// Delete removes a session.
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// CreatePending creates a TOTP-pending session token (5-minute expiry).
func (s *SessionStore) CreatePending(userID int64, username, role string) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.pending[token] = &SessionData{
		UserID:   userID,
		Username: username,
		Role:     role,
		Expiry:   time.Now().Add(5 * time.Minute),
	}
	s.mu.Unlock()
	return token
}

// GetPending retrieves and consumes a pending TOTP session.
func (s *SessionStore) GetPending(token string) (*SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.pending[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(data.Expiry) {
		delete(s.pending, token)
		return nil, false
	}
	delete(s.pending, token)
	return data, true
}

// exemptPaths are paths that do not require authentication.
var exemptPaths = map[string]bool{
	"/":                  true,
	"/health":            true,
	"/api/health":        true,
	"/api/login":         true,
	"/api/login/totp":    true,
	"/api/register":      true,
	"/api/auth/status":   true,
	"/api/oidc/login":    true,
	"/api/oidc/callback": true,
	"/api/oidc/status":   true,
	"/api/openapi.json":  true, // public API schema for AI/tooling discovery
}

// isExempt returns true if the path does not require auth.
func isExempt(path string) bool {
	if exemptPaths[path] {
		return true
	}
	// Static assets.
	if strings.HasPrefix(path, "/static/") {
		return true
	}
	// Prometheus metrics.
	if path == "/metrics" {
		return true
	}
	// Torznab indexer API has its own apikey auth inside the handler (gated
	// by TORZNAB_API_KEY). Without exempting these paths here, Prowlarr —
	// which sends ?apikey=<torznab-key> — would be rejected by the gamarr
	// auth middleware (which checks ?apikey=<gamarr-api-key>) before ever
	// reaching the Torznab handler. The /api alias is mounted on exact path
	// /api only, so this does NOT match /api/search, /api/library, etc.
	if path == "/api" || strings.HasPrefix(path, "/torznab/") {
		return true
	}
	return false
}

// resolveIdentity attempts to authenticate the request via API key (header
// or query param), multi-user session cookie, or legacy single-user session,
// in that order. On success it returns a context enriched with the caller's
// role/username (plus user ID for multi-user sessions) and true. On failure
// it returns the request's original context and false — it never writes a
// response and never consumes the request body.
func resolveIdentity(r *http.Request, cfg *config.Config, sessions *SessionStore, multiUser bool) (context.Context, bool) {
	// Check API key (header or query param).
	if cfg.HasAPIKey() {
		apiKey := r.Header.Get("X-Api-Key")
		if apiKey == "" {
			apiKey = r.URL.Query().Get("apikey")
		}
		if subtle.ConstantTimeCompare([]byte(apiKey), []byte(cfg.APIKey)) == 1 {
			ctx := context.WithValue(r.Context(), ctxUserRole, "admin")
			ctx = context.WithValue(ctx, ctxUsername, "api")
			return ctx, true
		}
	}

	// Check session cookie for multi-user mode.
	if multiUser {
		if cookie, err := r.Cookie("gamarr_session"); err == nil {
			if data, ok := sessions.Get(cookie.Value); ok {
				ctx := context.WithValue(r.Context(), ctxUserID, data.UserID)
				ctx = context.WithValue(ctx, ctxUserRole, data.Role)
				ctx = context.WithValue(ctx, ctxUsername, data.Username)
				return ctx, true
			}
		}
	}

	// Legacy single-user session auth (when no multi-user DB users exist).
	if !multiUser && cfg.HasAuth() {
		if cookie, err := r.Cookie("gamarr_session"); err == nil && sessions.Valid(cookie.Value) {
			ctx := context.WithValue(r.Context(), ctxUserRole, "admin")
			ctx = context.WithValue(ctx, ctxUsername, cfg.AuthUsername)
			return ctx, true
		}
	}

	return r.Context(), false
}

// authMiddleware returns an HTTP middleware that enforces authentication.
func authMiddleware(cfg *config.Config, database *db.JobStore, sessions *SessionStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if multi-user is active (any users in DB).
		userCount, _ := database.CountUsers()
		multiUser := userCount > 0

		// If no multi-user and no legacy auth and no API key, pass through as admin.
		if !multiUser && !cfg.HasAuth() && !cfg.HasAPIKey() {
			ctx := context.WithValue(r.Context(), ctxUserRole, "admin")
			ctx = context.WithValue(ctx, ctxUsername, "anonymous")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Exempt paths always pass through, but credentials are still
		// resolved best-effort so exempt handlers that care about the
		// caller's role can see it (e.g. /api/register's admin-registers-
		// a-user branch). Resolution failure NEVER rejects an exempt
		// request — the request simply proceeds unauthenticated. Torznab
		// paths are unaffected: resolveIdentity only reads headers/cookies/
		// query params, and the Torznab handler validates its own apikey.
		if isExempt(r.URL.Path) {
			if ctx, ok := resolveIdentity(r, cfg, sessions, multiUser); ok {
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
			return
		}

		if ctx, ok := resolveIdentity(r, cfg, sessions, multiUser); ok {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// No valid auth found.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Authentication required",
		})
	})
}

// requireAdmin is middleware that checks if the current user has admin role.
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ctxUserRole).(string)
		if role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{
				"success": false,
				"error":   "Admin access required",
			})
			return
		}
		next(w, r)
	}
}

// getUserIDFromContext extracts the user ID from the request context.
func getUserIDFromContext(r *http.Request) int64 {
	id, _ := r.Context().Value(ctxUserID).(int64)
	return id
}

// hashPassword hashes a password using bcrypt.
func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

// checkPassword verifies a password against a bcrypt hash.
func checkPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// handleLogin handles POST /api/login.
func handleLogin(cfg *config.Config, database *db.JobStore, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeJSONBody(w, r, &req) {
			return
		}

		// Check if multi-user mode is active.
		userCount, _ := database.CountUsers()
		multiUser := userCount > 0

		if multiUser {
			user, err := database.GetUserByUsername(req.Username)
			if err != nil || !checkPassword(req.Password, user.PasswordHash) {
				writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
					"success": false,
					"error":   "Invalid credentials",
				})
				return
			}

			// If TOTP is enabled, return a pending token instead of a full session.
			if user.TOTPEnabled {
				pendingToken := sessions.CreatePending(user.ID, user.Username, user.Role)
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"success":         true,
					"needs_totp":      true,
					"session_pending": pendingToken,
				})
				return
			}

			database.UpdateLastLogin(user.ID)
			token := sessions.Create(user.ID, user.Username, user.Role)
			http.SetCookie(w, &http.Cookie{
				Name:     "gamarr_session",
				Value:    token,
				Path:     "/",
				MaxAge:   86400,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})

			database.LogAuthActivity(user.Username, "login", user.Username, "User logged in")

			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success":  true,
				"token":    token,
				"username": user.Username,
				"role":     user.Role,
			})
			return
		}

		// Legacy single-user mode.
		if !cfg.HasAuth() {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": true,
				"message": "Auth not configured",
			})
			return
		}

		if req.Username != cfg.AuthUsername || req.Password != cfg.AuthPassword {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"success": false,
				"error":   "Invalid credentials",
			})
			return
		}

		token := sessions.Create(0, cfg.AuthUsername, "admin")
		http.SetCookie(w, &http.Cookie{
			Name:     "gamarr_session",
			Value:    token,
			Path:     "/",
			MaxAge:   86400,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		database.LogAuthActivity(cfg.AuthUsername, "login", cfg.AuthUsername, "User logged in (legacy)")

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":  true,
			"token":    token,
			"username": cfg.AuthUsername,
			"role":     "admin",
		})
	}
}

// handleRegister handles POST /api/register. First user becomes admin.
// Supports invite codes: non-admin users can register with a valid invite code.
func handleRegister(database *db.JobStore, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username   string `json:"username"`
			Password   string `json:"password"`
			InviteCode string `json:"invite_code"`
		}
		if !decodeJSONBody(w, r, &req) {
			return
		}

		if len(req.Username) < 3 || len(req.Username) > 64 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Username must be 3-64 characters",
			})
			return
		}
		if len(req.Password) < 6 || len(req.Password) > 72 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Password must be 6-72 characters",
			})
			return
		}

		userCount, _ := database.CountUsers()
		isFirstUser := userCount == 0

		var inviteRole string
		if !isFirstUser {
			// Check if user is admin OR has a valid invite code.
			callerRole, _ := r.Context().Value(ctxUserRole).(string)
			if callerRole != "admin" {
				if req.InviteCode == "" {
					writeJSON(w, http.StatusForbidden, map[string]interface{}{
						"success": false,
						"error":   "Admin access or invite code required",
					})
					return
				}
				var err error
				inviteRole, err = database.ValidateInviteCode(req.InviteCode)
				if err != nil {
					writeJSON(w, http.StatusForbidden, map[string]interface{}{
						"success": false,
						"error":   err.Error(),
					})
					return
				}
			}
		}

		role := "user"
		if isFirstUser {
			role = "admin"
		}
		if inviteRole != "" {
			role = inviteRole
		}

		hash, err := hashPassword(req.Password)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to hash password",
			})
			return
		}

		id, err := database.CreateUser(req.Username, hash, role)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeJSON(w, http.StatusConflict, map[string]interface{}{
					"success": false,
					"error":   "Username already exists",
				})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to create user",
			})
			return
		}

		// Mark invite code as used.
		if req.InviteCode != "" {
			database.UseInviteCode(req.InviteCode)
		}

		slog.Info("user registered", "id", id, "username", req.Username, "role", role)

		// If first user, auto-login.
		if isFirstUser {
			database.UpdateLastLogin(id)
			token := sessions.Create(id, req.Username, role)
			http.SetCookie(w, &http.Cookie{
				Name:     "gamarr_session",
				Value:    token,
				Path:     "/",
				MaxAge:   86400,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
			writeJSON(w, http.StatusCreated, map[string]interface{}{
				"success":  true,
				"id":       id,
				"username": req.Username,
				"role":     role,
				"token":    token,
				"message":  "First user created as admin",
			})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"success":  true,
			"id":       id,
			"username": req.Username,
			"role":     role,
		})
	}
}

// handleAuthStatus returns the current auth state.
func handleAuthStatus(database *db.JobStore, sessions *SessionStore, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userCount, _ := database.CountUsers()

		resp := map[string]interface{}{
			"has_users":     userCount > 0,
			"authenticated": false,
			"oidc_enabled":  cfg.HasOIDC(),
			"oidc_provider": cfg.OIDCProviderName,
		}

		cookie, err := r.Cookie("gamarr_session")
		if err == nil {
			if data, ok := sessions.Get(cookie.Value); ok {
				resp["authenticated"] = true
				resp["username"] = data.Username
				resp["role"] = data.Role
				resp["user_id"] = data.UserID
				if user, err := database.GetUser(data.UserID); err == nil {
					resp["totp_enabled"] = user.TOTPEnabled
				}
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// handleLogout handles POST /api/logout.
func handleLogout(sessions *SessionStore, database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username, _ := r.Context().Value(ctxUsername).(string)
		cookie, err := r.Cookie("gamarr_session")
		if err == nil {
			sessions.Delete(cookie.Value)
		}
		database.LogAuthActivity(username, "logout", username, "User logged out")
		http.SetCookie(w, &http.Cookie{
			Name:     "gamarr_session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
	}
}

// handleLoginTOTP handles POST /api/login/totp — validates TOTP code or backup code for pending sessions.
func handleLoginTOTP(database *db.JobStore, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PendingToken string `json:"session_pending"`
			Code         string `json:"code"`
		}
		if !decodeJSONBody(w, r, &req) {
			return
		}

		pending, ok := sessions.GetPending(req.PendingToken)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"success": false,
				"error":   "Invalid or expired pending session",
			})
			return
		}

		user, err := database.GetUser(pending.UserID)
		if err != nil {
			writeError(w, http.StatusNotFound, "User not found")
			return
		}

		// Try TOTP code first, then backup code.
		valid := false
		usedBackup := false

		if totp.Validate(req.Code, user.TOTPSecret) {
			valid = true
		} else {
			// Try as backup code.
			codeHash := hashBackupCode(req.Code)
			if database.UseBackupCode(user.ID, codeHash) {
				valid = true
				usedBackup = true
			}
		}

		if !valid {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"success": false,
				"error":   "Invalid TOTP or backup code",
			})
			return
		}

		database.UpdateLastLogin(user.ID)
		token := sessions.Create(user.ID, user.Username, user.Role)
		http.SetCookie(w, &http.Cookie{
			Name:     "gamarr_session",
			Value:    token,
			Path:     "/",
			MaxAge:   86400,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		detail := "TOTP login"
		if usedBackup {
			detail = "TOTP login (backup code)"
		}
		database.LogAuthActivity(user.Username, "login", user.Username, detail)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":     true,
			"token":       token,
			"username":    user.Username,
			"role":        user.Role,
			"used_backup": usedBackup,
		})
	}
}

// handleListUsers handles GET /api/users — admin only.
func handleListUsers(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := database.ListUsers()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to list users",
			})
			return
		}

		type safeUser struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			Role      string `json:"role"`
			CreatedAt string `json:"created_at"`
			LastLogin string `json:"last_login,omitempty"`
		}

		var result []safeUser
		for _, u := range users {
			su := safeUser{
				ID:        u.ID,
				Username:  u.Username,
				Role:      u.Role,
				CreatedAt: u.CreatedAt.Format(time.RFC3339),
			}
			if !u.LastLogin.IsZero() {
				su.LastLogin = u.LastLogin.Format(time.RFC3339)
			}
			result = append(result, su)
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"users":   result,
		})
	}
}

// handleUpdateUser handles PATCH /api/users/{id} — admin only.
func handleUpdateUser(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Invalid user ID",
			})
			return
		}

		var req struct {
			Role     string `json:"role"`
			Password string `json:"password,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Invalid request body",
			})
			return
		}

		user, err := database.GetUser(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{
				"success": false,
				"error":   "User not found",
			})
			return
		}

		if req.Role != "" && (req.Role == "admin" || req.Role == "user") {
			if err := database.UpdateUser(id, user.Username, req.Role); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
					"success": false,
					"error":   "Failed to update user",
				})
				return
			}
		}

		if req.Password != "" {
			hash, err := hashPassword(req.Password)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
					"success": false,
					"error":   "Failed to hash password",
				})
				return
			}
			if err := database.UpdateUserPassword(id, hash); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
					"success": false,
					"error":   "Failed to update password",
				})
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
	}
}

// handleDeleteUser handles DELETE /api/users/{id} — admin only.
func handleDeleteUser(database *db.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Invalid user ID",
			})
			return
		}

		// Prevent deleting yourself.
		currentID := getUserIDFromContext(r)
		if currentID == id {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Cannot delete your own account",
			})
			return
		}

		if err := database.DeleteUser(id); err != nil {
			slog.Error("failed to delete user", "id", id, "error", err)
			writeJSON(w, http.StatusNotFound, map[string]interface{}{
				"success": false,
				"error":   "Failed to delete user",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
	}
}
