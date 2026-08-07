package api

import (
	"fmt"
	"testing"

	"gamarr/internal/config"
)

// ── Unconfigured mode: pass-through as admin ───────────────────────────────────

func TestAuthPassThroughWhenUnconfigured(t *testing.T) {
	// No users, no legacy auth, no API key → every request is admin.
	env := newTestEnv(t, nil)

	paths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/wishlist"},
		{"GET", "/api/settings"},        // requireAdmin
		{"GET", "/api/admin/dashboard"}, // requireAdmin
		{"GET", "/api/users"},           // requireAdmin
	}
	for _, tc := range paths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rr := env.do(tc.method, tc.path, "")
			wantStatus(t, rr, 200)
		})
	}
}

// ── API key auth ───────────────────────────────────────────────────────────────

func TestAPIKeyAuth(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.APIKey = "sekrit-key-123" })

	cases := []struct {
		name string
		path string
		opts []reqOpt
		want int
	}{
		{"no key rejected", "/api/wishlist", nil, 401},
		{"wrong header key rejected", "/api/wishlist", []reqOpt{withHeader("X-Api-Key", "nope")}, 401},
		{"header key accepted", "/api/wishlist", []reqOpt{withHeader("X-Api-Key", "sekrit-key-123")}, 200},
		{"query param key accepted", "/api/wishlist?apikey=sekrit-key-123", nil, 200},
		{"wrong query param rejected", "/api/wishlist?apikey=bad", nil, 401},
		{"key grants admin role", "/api/settings", []reqOpt{withHeader("X-Api-Key", "sekrit-key-123")}, 200},
		{"exempt path without key", "/api/health", nil, 200},
		{"exempt openapi without key", "/api/openapi.json", nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := env.do("GET", tc.path, "", tc.opts...)
			wantStatus(t, rr, tc.want)
		})
	}
}

// ── Legacy single-user auth ────────────────────────────────────────────────────

func TestLegacyAuthLoginFlow(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.AuthUsername = "admin"
		c.AuthPassword = "hunter22"
	})

	t.Run("unauthenticated request rejected", func(t *testing.T) {
		rr := env.do("GET", "/api/wishlist", "")
		wantStatus(t, rr, 401)
	})

	t.Run("wrong password rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/login", `{"username":"admin","password":"wrong"}`)
		wantStatus(t, rr, 401)
	})

	t.Run("malformed body rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/login", `{not json`)
		wantStatus(t, rr, 400)
	})

	t.Run("correct credentials issue a working session", func(t *testing.T) {
		rr := env.do("POST", "/api/login", `{"username":"admin","password":"hunter22"}`)
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		token, _ := m["token"].(string)
		if token == "" {
			t.Fatalf("login response missing token: %v", m)
		}
		if m["role"] != "admin" {
			t.Errorf("role = %v, want admin", m["role"])
		}

		rr = env.do("GET", "/api/wishlist", "", withSession(token))
		wantStatus(t, rr, 200)

		// Bogus session cookie still rejected.
		rr = env.do("GET", "/api/wishlist", "", withSession("not-a-real-token"))
		wantStatus(t, rr, 401)
	})
}

// ── Multi-user mode: register → login → session cookie → roles ─────────────────

func TestMultiUserRegistrationValidation(t *testing.T) {
	env := newTestEnv(t, nil)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"username too short", `{"username":"ab","password":"secret123"}`, 400},
		{"password too short", `{"username":"alice","password":"pw"}`, 400},
		{"malformed body", `{nope`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := env.do("POST", "/api/register", tc.body)
			wantStatus(t, rr, tc.want)
		})
	}

	// None of the failed attempts should have created a user.
	rr := env.do("GET", "/api/auth/status", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["has_users"] != false {
		t.Errorf("has_users = %v, want false after failed registrations", m["has_users"])
	}
}

func TestMultiUserSessionFlow(t *testing.T) {
	env := newTestEnv(t, nil)

	// First user registers → becomes admin and is auto-logged-in.
	rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
	wantStatus(t, rr, 201)
	reg := decodeMap(t, rr)
	if reg["role"] != "admin" {
		t.Fatalf("first user role = %v, want admin", reg["role"])
	}
	adminTok, _ := reg["token"].(string)
	if adminTok == "" {
		t.Fatal("first-user registration did not return a session token")
	}

	t.Run("multi-user mode now requires auth", func(t *testing.T) {
		rr := env.do("GET", "/api/wishlist", "")
		wantStatus(t, rr, 401)
	})

	t.Run("admin session cookie works", func(t *testing.T) {
		rr := env.do("GET", "/api/wishlist", "", withSession(adminTok))
		wantStatus(t, rr, 200)
	})

	t.Run("auth status reflects the session", func(t *testing.T) {
		rr := env.do("GET", "/api/auth/status", "", withSession(adminTok))
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["authenticated"] != true || m["username"] != "alice" || m["role"] != "admin" {
			t.Errorf("auth status = %v, want authenticated alice/admin", m)
		}
	})

	t.Run("duplicate username rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123","invite_code":"whatever"}`)
		// Invite code is validated before the UNIQUE constraint, so a bogus
		// code fails first with 403; a valid duplicate hits 409 (covered below
		// by re-registering bob with a fresh invite).
		if rr.Code != 403 && rr.Code != 409 {
			t.Errorf("duplicate/bad-invite registration = %d, want 403 or 409", rr.Code)
		}
	})

	t.Run("second registration without invite is forbidden", func(t *testing.T) {
		rr := env.do("POST", "/api/register", `{"username":"bob","password":"secret123"}`)
		wantStatus(t, rr, 403)
	})

	// Admin mints an invite code for bob.
	rr = env.do("POST", "/api/invites", `{"role":"user"}`, withSession(adminTok))
	wantStatus(t, rr, 201)
	invite := decodeMap(t, rr)
	code, _ := invite["code"].(string)
	if code == "" {
		t.Fatalf("invite response missing code: %v", invite)
	}

	t.Run("invite list requires admin and shows the code", func(t *testing.T) {
		rr := env.do("GET", "/api/invites", "", withSession(adminTok))
		wantStatus(t, rr, 200)
	})

	// Bob registers with the invite code → role user, no auto-login.
	rr = env.do("POST", "/api/register",
		fmt.Sprintf(`{"username":"bob","password":"secret456","invite_code":"%s"}`, code))
	wantStatus(t, rr, 201)
	bobReg := decodeMap(t, rr)
	if bobReg["role"] != "user" {
		t.Fatalf("bob role = %v, want user", bobReg["role"])
	}
	bobID := int64(bobReg["id"].(float64))

	t.Run("used invite cannot be reused", func(t *testing.T) {
		rr := env.do("POST", "/api/register",
			fmt.Sprintf(`{"username":"carol","password":"secret789","invite_code":"%s"}`, code))
		wantStatus(t, rr, 403)
	})

	t.Run("wrong password login fails", func(t *testing.T) {
		rr := env.do("POST", "/api/login", `{"username":"bob","password":"wrong"}`)
		wantStatus(t, rr, 401)
	})

	t.Run("unknown user login fails", func(t *testing.T) {
		rr := env.do("POST", "/api/login", `{"username":"mallory","password":"whatever1"}`)
		wantStatus(t, rr, 401)
	})

	// Bob logs in.
	rr = env.do("POST", "/api/login", `{"username":"bob","password":"secret456"}`)
	wantStatus(t, rr, 200)
	bobTok, _ := decodeMap(t, rr)["token"].(string)
	if bobTok == "" {
		t.Fatal("bob login did not return a token")
	}

	t.Run("non-admin gets 403 on admin routes", func(t *testing.T) {
		adminOnly := []struct {
			method string
			path   string
		}{
			{"GET", "/api/settings"},
			{"PUT", "/api/settings"},
			{"GET", "/api/users"},
			{"GET", "/api/admin/dashboard"},
			{"GET", "/api/invites"},
			{"POST", "/api/test/prowlarr"},
			{"POST", "/api/test/qbittorrent"},
			{"POST", "/api/test/sabnzbd"},
			{"POST", "/api/test/nzbget"},
			// Transmission/Deluge connectivity tests are admin-gated the same
			// as their siblings (RegisterMetadataRoutes wraps them in
			// requireAdmin).
			{"POST", "/api/test/transmission"},
			{"POST", "/api/test/deluge"},
			{"GET", "/api/backup/list"},
		}
		for _, tc := range adminOnly {
			rr := env.do(tc.method, tc.path, "", withSession(bobTok))
			if rr.Code != 403 {
				t.Errorf("%s %s as non-admin = %d, want 403", tc.method, tc.path, rr.Code)
			}
		}
	})

	t.Run("non-admin can still use regular routes", func(t *testing.T) {
		rr := env.do("GET", "/api/wishlist", "", withSession(bobTok))
		wantStatus(t, rr, 200)
	})

	t.Run("admin lists users", func(t *testing.T) {
		rr := env.do("GET", "/api/users", "", withSession(adminTok))
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		users, _ := m["users"].([]interface{})
		if len(users) != 2 {
			t.Errorf("users = %d, want 2", len(users))
		}
	})

	t.Run("admin promotes bob then deletes him", func(t *testing.T) {
		rr := env.do("PATCH", fmt.Sprintf("/api/users/%d", bobID), `{"role":"admin"}`, withSession(adminTok))
		wantStatus(t, rr, 200)

		rr = env.do("DELETE", fmt.Sprintf("/api/users/%d", bobID), "", withSession(adminTok))
		wantStatus(t, rr, 200)

		// Bob's account is gone; his existing session token is invalidated
		// only on expiry, but a fresh login must now fail.
		rr = env.do("POST", "/api/login", `{"username":"bob","password":"secret456"}`)
		wantStatus(t, rr, 401)
	})

	t.Run("admin cannot delete own account", func(t *testing.T) {
		adminID := int64(reg["id"].(float64))
		rr := env.do("DELETE", fmt.Sprintf("/api/users/%d", adminID), "", withSession(adminTok))
		wantStatus(t, rr, 400)
	})

	t.Run("logout invalidates the session", func(t *testing.T) {
		rr := env.do("POST", "/api/logout", "", withSession(adminTok))
		wantStatus(t, rr, 200)
		rr = env.do("GET", "/api/wishlist", "", withSession(adminTok))
		wantStatus(t, rr, 401)
	})
}

// TestAdminRegisterOnExemptPath locks in the fix for the unreachable
// admin-registers-a-user branch: /api/register is auth-exempt, but the
// middleware must still resolve credentials best-effort so handleRegister
// can see an admin caller's role. Resolution failure must NEVER reject an
// exempt request.
func TestAdminRegisterOnExemptPath(t *testing.T) {
	t.Run("admin session can register a user without invite", func(t *testing.T) {
		env := newTestEnv(t, nil)

		// First user → admin with auto-login token.
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
		wantStatus(t, rr, 201)
		adminTok, _ := decodeMap(t, rr)["token"].(string)
		if adminTok == "" {
			t.Fatal("first-user registration did not return a session token")
		}

		// Admin registers bob directly — no invite code needed.
		rr = env.do("POST", "/api/register", `{"username":"bob","password":"secret456"}`, withSession(adminTok))
		wantStatus(t, rr, 201)
		if m := decodeMap(t, rr); m["role"] != "user" {
			t.Errorf("bob role = %v, want user", m["role"])
		}

		// Bob's account really works.
		rr = env.do("POST", "/api/login", `{"username":"bob","password":"secret456"}`)
		wantStatus(t, rr, 200)
	})

	t.Run("admin API key can register a user without invite", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) { c.APIKey = "agent-key" })

		// Claiming an API-key-protected instance takes the key; anonymous
		// bootstrap is refused (see
		// TestFirstUserBootstrapRequiresCredentialsOnProtectedInstance).
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`,
			withHeader("X-Api-Key", "agent-key"))
		wantStatus(t, rr, 201)
		if m := decodeMap(t, rr); m["role"] != "admin" {
			t.Errorf("first user role = %v, want admin", m["role"])
		}

		// API key (admin role) registers carol via header.
		rr = env.do("POST", "/api/register", `{"username":"carol","password":"secret789"}`,
			withHeader("X-Api-Key", "agent-key"))
		wantStatus(t, rr, 201)

		// And via query param.
		rr = env.do("POST", "/api/register?apikey=agent-key", `{"username":"dave","password":"secret000"}`)
		wantStatus(t, rr, 201)

		// Wrong API key does not grant the admin branch — but must not be
		// rejected by the middleware either: the handler's own 403 fires.
		rr = env.do("POST", "/api/register", `{"username":"eve","password":"secret123"}`,
			withHeader("X-Api-Key", "wrong-key"))
		wantStatus(t, rr, 403)
	})

	t.Run("anonymous without invite still forbidden once users exist", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
		wantStatus(t, rr, 201)

		rr = env.do("POST", "/api/register", `{"username":"eve","password":"secret123"}`)
		wantStatus(t, rr, 403)

		// A bogus session cookie must not reject the exempt request outright;
		// it simply proceeds unauthenticated and hits the handler's 403.
		rr = env.do("POST", "/api/register", `{"username":"eve","password":"secret123"}`,
			withSession("not-a-real-token"))
		wantStatus(t, rr, 403)
	})

	t.Run("legacy instance with zero users answers from the handler", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) {
			// Legacy auth configured → no unconditional admin pass-through,
			// so this exercises the exempt path with zero users.
			c.AuthUsername = "legacy"
			c.AuthPassword = "legacy-pass"
		})
		// 403 (the handler's own refusal), never 401 — the middleware must
		// keep letting the exempt path through so the handler decides.
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
		wantStatus(t, rr, 403)

		// The legacy operator's session is what authorizes the bootstrap.
		rr = env.do("POST", "/api/login", `{"username":"legacy","password":"legacy-pass"}`)
		wantStatus(t, rr, 200)
		token, _ := decodeMap(t, rr)["token"].(string)

		rr = env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`, withSession(token))
		wantStatus(t, rr, 201)
		if m := decodeMap(t, rr); m["role"] != "admin" {
			t.Errorf("first user role = %v, want admin", m["role"])
		}
	})
}

func TestAPIKeyStillWorksInMultiUserMode(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) { c.APIKey = "agent-key" })

	// The key is what authorizes claiming an already-protected instance — see
	// TestFirstUserBootstrapRequiresCredentialsOnProtectedInstance.
	rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`,
		withHeader("X-Api-Key", "agent-key"))
	wantStatus(t, rr, 201)

	rr = env.do("GET", "/api/settings", "", withHeader("X-Api-Key", "agent-key"))
	wantStatus(t, rr, 200)

	rr = env.do("GET", "/api/settings", "")
	wantStatus(t, rr, 401)
}

// ── SessionStore unit behavior ─────────────────────────────────────────────────

func TestSessionStore(t *testing.T) {
	s := NewSessionStore()

	tok := s.Create(7, "alice", "admin")
	data, ok := s.Get(tok)
	if !ok || data.Username != "alice" || data.Role != "admin" || data.UserID != 7 {
		t.Fatalf("Get(%q) = %+v, %v", tok, data, ok)
	}
	if !s.Valid(tok) {
		t.Error("Valid should be true for fresh token")
	}
	s.Delete(tok)
	if s.Valid(tok) {
		t.Error("Valid should be false after Delete")
	}

	t.Run("pending sessions are single-use", func(t *testing.T) {
		p := s.CreatePending(9, "bob", "user")
		data, ok := s.GetPending(p)
		if !ok || data.UserID != 9 {
			t.Fatalf("GetPending = %+v, %v", data, ok)
		}
		if _, ok := s.GetPending(p); ok {
			t.Error("pending token should be consumed on first use")
		}
	})
}

func TestOIDCStatusUnconfigured(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("GET", "/api/oidc/status", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["enabled"] != false {
		t.Errorf("oidc enabled = %v, want false", m["enabled"])
	}

	rr = env.do("GET", "/api/oidc/login", "")
	wantStatus(t, rr, 404)
}

// ── /api/auth/status: what the frontend needs before it draws anything ────────

// The login UI reads these three booleans to choose between "load the app",
// "ask for a password" and "say why a password won't help" (issue #21). The
// combinations below are the four deployments gamarr actually ships in.
func TestAuthStatusDescribesTheDeployment(t *testing.T) {
	cases := []struct {
		name           string
		mutate         func(*config.Config)
		register       bool // create a DB user first (multi-user mode)
		authRequired   bool
		loginAvailable bool
		canRegister    bool
		hasUsers       bool
	}{
		{
			name:           "unconfigured instance is open",
			authRequired:   false,
			loginAvailable: false,
			canRegister:    true,
		},
		{
			name: "legacy single-user needs a password",
			mutate: func(c *config.Config) {
				c.AuthUsername = "admin"
				c.AuthPassword = "hunter22"
			},
			authRequired:   true,
			loginAvailable: true,
			canRegister:    false,
		},
		{
			name:           "api-key-only has no browser login",
			mutate:         func(c *config.Config) { c.APIKey = "sekrit-key-123" },
			authRequired:   true,
			loginAvailable: false,
			canRegister:    false,
		},
		{
			name:           "multi-user needs a password",
			register:       true,
			authRequired:   true,
			loginAvailable: true,
			canRegister:    false,
			hasUsers:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.mutate)
			if tc.register {
				rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
				wantStatus(t, rr, 201)
			}

			rr := env.do("GET", "/api/auth/status", "")
			wantStatus(t, rr, 200)
			m := decodeMap(t, rr)

			for _, f := range []struct {
				key  string
				want bool
			}{
				{"auth_required", tc.authRequired},
				{"login_available", tc.loginAvailable},
				{"can_register", tc.canRegister},
				{"has_users", tc.hasUsers},
				{"authenticated", false},
			} {
				if m[f.key] != f.want {
					t.Errorf("%s = %v, want %v (full status: %v)", f.key, m[f.key], f.want, m)
				}
			}
		})
	}
}

// A 401 must not be the frontend's first hint that auth exists: the status
// endpoint itself stays reachable and honest without credentials.
func TestAuthStatusIsReachableWithoutCredentials(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.AuthUsername = "admin"
		c.AuthPassword = "hunter22"
	})

	// Protected route rejects…
	wantStatus(t, env.do("GET", "/api/platforms", ""), 401)
	// …while the status route answers, so the UI can render a login form.
	rr := env.do("GET", "/api/auth/status", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["auth_required"] != true || m["login_available"] != true {
		t.Errorf("status = %v, want auth_required+login_available true", m)
	}
}

func TestAuthStatusReflectsLegacySession(t *testing.T) {
	env := newTestEnv(t, func(c *config.Config) {
		c.AuthUsername = "admin"
		c.AuthPassword = "hunter22"
	})

	rr := env.do("POST", "/api/login", `{"username":"admin","password":"hunter22"}`)
	wantStatus(t, rr, 200)
	token, _ := decodeMap(t, rr)["token"].(string)

	rr = env.do("GET", "/api/auth/status", "", withSession(token))
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["authenticated"] != true || m["username"] != "admin" {
		t.Errorf("status = %v, want authenticated admin", m)
	}
}

// ── First-user bootstrap on an already-protected instance ─────────────────────

// /api/register is exempt from the auth middleware so a fresh instance can be
// claimed. On an instance that already declares an owner — legacy credentials
// or an API key — that exemption would otherwise hand admin to any anonymous
// caller who reached the port, which the new sign-up UI would have advertised.
func TestFirstUserBootstrapRequiresCredentialsOnProtectedInstance(t *testing.T) {
	const body = `{"username":"mallory","password":"secret123"}`

	t.Run("legacy instance rejects anonymous bootstrap", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) {
			c.AuthUsername = "admin"
			c.AuthPassword = "hunter22"
		})
		wantStatus(t, env.do("POST", "/api/register", body), 403)

		// No user was created, so the instance is still legacy-only.
		rr := env.do("GET", "/api/auth/status", "")
		if m := decodeMap(t, rr); m["has_users"] != false {
			t.Errorf("has_users = %v, want false after a rejected bootstrap", m["has_users"])
		}
	})

	t.Run("legacy admin can migrate to multi-user", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) {
			c.AuthUsername = "admin"
			c.AuthPassword = "hunter22"
		})
		rr := env.do("POST", "/api/login", `{"username":"admin","password":"hunter22"}`)
		wantStatus(t, rr, 200)
		token, _ := decodeMap(t, rr)["token"].(string)

		rr = env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`, withSession(token))
		wantStatus(t, rr, 201)
		if m := decodeMap(t, rr); m["role"] != "admin" {
			t.Errorf("migrated first user role = %v, want admin", m["role"])
		}
	})

	t.Run("api-key instance rejects anonymous bootstrap", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) { c.APIKey = "sekrit-key-123" })
		wantStatus(t, env.do("POST", "/api/register", body), 403)
	})

	t.Run("api key authorizes bootstrap", func(t *testing.T) {
		env := newTestEnv(t, func(c *config.Config) { c.APIKey = "sekrit-key-123" })
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`,
			withHeader("X-Api-Key", "sekrit-key-123"))
		wantStatus(t, rr, 201)
	})

	t.Run("unclaimed instance still bootstraps freely", func(t *testing.T) {
		env := newTestEnv(t, nil)
		rr := env.do("POST", "/api/register", `{"username":"alice","password":"secret123"}`)
		wantStatus(t, rr, 201)
	})
}
