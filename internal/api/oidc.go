package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCHandler manages OpenID Connect authentication.
type OIDCHandler struct {
	cfg      *config.Config
	database *db.JobStore
	sessions *SessionStore
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier

	stateMu sync.Mutex
	states  map[string]time.Time // nonce → expiry
}

// NewOIDCHandler creates a new OIDC handler. Returns nil if OIDC is not configured.
func NewOIDCHandler(cfg *config.Config, database *db.JobStore, sessions *SessionStore) *OIDCHandler {
	if !cfg.HasOIDC() {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		slog.Error("failed to initialize OIDC provider", "issuer", cfg.OIDCIssuer, "error", err)
		return nil
	}

	h := &OIDCHandler{
		cfg:      cfg,
		database: database,
		sessions: sessions,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
		states:   make(map[string]time.Time),
	}

	// Cleanup expired states every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			h.stateMu.Lock()
			now := time.Now()
			for state, expiry := range h.states {
				if now.After(expiry) {
					delete(h.states, state)
				}
			}
			h.stateMu.Unlock()
		}
	}()

	slog.Info("OIDC provider initialized", "issuer", cfg.OIDCIssuer, "provider", cfg.OIDCProviderName)
	return h
}

func (h *OIDCHandler) oauth2Config(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     h.cfg.OIDCClientID,
		ClientSecret: h.cfg.OIDCClientSecret,
		Endpoint:     h.provider.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func (h *OIDCHandler) generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	state := hex.EncodeToString(b)
	h.stateMu.Lock()
	h.states[state] = time.Now().Add(10 * time.Minute)
	h.stateMu.Unlock()
	return state
}

func (h *OIDCHandler) validateState(state string) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	expiry, ok := h.states[state]
	if !ok {
		return false
	}
	delete(h.states, state)
	return time.Now().Before(expiry)
}

// HandleLogin redirects the user to the OIDC provider for authentication.
func (h *OIDCHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeError(w, http.StatusNotFound, "OIDC not configured")
		return
	}

	// Determine redirect URI from request if not explicitly configured.
	redirectURI := h.cfg.OIDCRedirectURI
	if redirectURI == "" {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		}
		host := r.Host
		redirectURI = scheme + "://" + host + "/api/oidc/callback"
	}

	state := h.generateState()
	url := h.oauth2Config(redirectURI).AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// HandleCallback processes the OIDC provider's callback after authentication.
func (h *OIDCHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeError(w, http.StatusNotFound, "OIDC not configured")
		return
	}

	state := r.URL.Query().Get("state")
	if !h.validateState(state) {
		writeError(w, http.StatusBadRequest, "Invalid or expired state")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "No authorization code")
		return
	}

	// Determine redirect URI (must match the one used in login).
	redirectURI := h.cfg.OIDCRedirectURI
	if redirectURI == "" {
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		}
		host := r.Host
		redirectURI = scheme + "://" + host + "/api/oidc/callback"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	token, err := h.oauth2Config(redirectURI).Exchange(ctx, code)
	if err != nil {
		slog.Error("OIDC token exchange failed", "error", err)
		writeError(w, http.StatusUnauthorized, "Token exchange failed")
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusUnauthorized, "No id_token in response")
		return
	}

	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		slog.Error("OIDC token verification failed", "error", err)
		writeError(w, http.StatusUnauthorized, "Token verification failed")
		return
	}

	// Extract claims.
	var claims struct {
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername  string `json:"preferred_username"`
		Subject           string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to parse claims")
		return
	}

	// Determine username: preferred_username > email > name > sub
	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = claims.Name
	}
	if username == "" {
		username = claims.Subject
	}
	username = strings.TrimSpace(username)

	// Find or create user.
	user, err := h.database.GetUserByUsername(username)
	if err != nil {
		if !h.cfg.OIDCAutoCreateUsers {
			writeError(w, http.StatusForbidden, "User not found and auto-creation is disabled")
			return
		}

		// Auto-create user. First user is admin.
		role := h.cfg.OIDCDefaultRole
		userCount, _ := h.database.CountUsers()
		if userCount == 0 {
			role = "admin"
		}

		// Generate a random password hash (user will only log in via OIDC).
		randPass := make([]byte, 32)
		rand.Read(randPass)
		hash, _ := hashPassword(hex.EncodeToString(randPass))

		id, err := h.database.CreateUser(username, hash, role)
		if err != nil {
			slog.Error("OIDC auto-create user failed", "username", username, "error", err)
			writeError(w, http.StatusInternalServerError, "Failed to create user")
			return
		}
		slog.Info("OIDC user auto-created", "username", username, "role", role, "id", id)

		user, err = h.database.GetUser(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to retrieve created user")
			return
		}
	}

	// Create session.
	h.database.UpdateLastLogin(user.ID)
	sessionToken := h.sessions.Create(user.ID, user.Username, user.Role)
	http.SetCookie(w, &http.Cookie{
		Name:     "gamarr_session",
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	h.database.LogAuthActivity(user.Username, "oidc_login", user.Username, "OIDC login via "+h.cfg.OIDCProviderName)

	// Redirect to app root.
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}
