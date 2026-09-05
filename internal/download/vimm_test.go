package download

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/db"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

const vimmGamePageHTML = `<html><body>
<form action="/download" method="POST" id="dl_form" onsubmit="return submitDL(this, 'dialog3')"><input type="hidden" name="mediaId" value="1624"><input type="hidden" name="alt" value="0" disabled><button type="submit">Download</button></form>
<script>function submitDL(theForm,dialogId){theForm.method='GET';return true;}</script>
</body></html>`

func TestParseVimmDownloadForm(t *testing.T) {
	action, media := parseVimmDownloadForm(vimmGamePageHTML)
	if media != "1624" {
		t.Errorf("mediaId = %q, want 1624", media)
	}
	if action != "/download" {
		t.Errorf("action = %q, want /download", action)
	}

	live := `<form action="//dl3.vimm.net/" method="POST" id="dl_form" onsubmit="return submitDL(this, 'dialog3')"><input type="hidden" name="mediaId" value="1624"></form>`
	action, media = parseVimmDownloadForm(live)
	if media != "1624" || action != "//dl3.vimm.net/" {
		t.Errorf("live form action=%q media=%q", action, media)
	}

	// Current solved pages can carry the selected media in JavaScript instead
	// of a hidden input. Be liberal about whitespace and quoted numbers.
	js := `<form action='//dl3.vimm.net/' id='dl_form'></form><script>let allMedia = [{"ID" : "3811", "Region":"USA"}];</script>`
	action, media = parseVimmDownloadForm(js)
	if media != "3811" || action != "//dl3.vimm.net/" {
		t.Errorf("JavaScript form action=%q media=%q", action, media)
	}

	// An unrelated page model must not win over Vimm's scoped media array.
	js = `<script>const analytics = {"ID":9999};</script>
		<form action="//dl3.vimm.net/" id="dl_form"></form>
		<script>const allMedia = [{"ID":3811,"SortOrder":2},{"ID":"3812","SortOrder":1,"GoodTitle":"A [bracket] in a string"}];</script>`
	action, media = parseVimmDownloadForm(js)
	if media != "3812" || action != "//dl3.vimm.net/" {
		t.Errorf("default JavaScript media action=%q media=%q, want 3812", action, media)
	}

	// Multiple entries with no rendered form value or explicit default are
	// ambiguous; guessing from a select index can pick the wrong disc.
	js = `<form action="//dl3.vimm.net/" id="dl_form"></form><script>let media=[{"ID":3811},{"ID":3812}]</script>`
	_, media = parseVimmDownloadForm(js)
	if media != "" {
		t.Errorf("ambiguous media array chose %q, want no guess", media)
	}

	// Current captures have also used the shorter variable name.
	js = `<form action="//dl3.vimm.net/" id="download_form"></form><script>const media=[{"ID":3811}]</script>`
	action, media = parseVimmDownloadForm(js)
	if media != "3811" || action != "//dl3.vimm.net/" {
		t.Errorf("const media action=%q media=%q", action, media)
	}

	// The form on the live vault page as FlareSolverr 3.5.0 rendered it on
	// 2026-09-05: the id is dl-form, with a hyphen, and the hidden field sits
	// inside it. Reading that field is what makes a multi-release page
	// unambiguous, so the hyphen must not push parsing onto the script fallback.
	live = `<form method="GET" action="/vault/"><input name="q"></form>
		<form action="//dl3.vimm.net/" method="POST" id="dl-form" onsubmit="return submitDL(this, 'dialog3')">
		<input type="hidden" name="mediaId" value="3811"><button type="submit">Download</button></form>
		<script>allMedia=[{"ID":3811},{"ID":3812}];</script>`
	action, media = parseVimmDownloadForm(live)
	if media != "3811" || action != "//dl3.vimm.net/" {
		t.Errorf("live dl-form action=%q media=%q", action, media)
	}

	// A similarly named input outside the download form is not authoritative.
	js = `<form id="tracking"><input name="mediaId" value="9999"></form>
		<form action="//dl3.vimm.net/" id="dl_form"><input value="3811" name="mediaId"></form>`
	action, media = parseVimmDownloadForm(js)
	if media != "3811" || action != "//dl3.vimm.net/" {
		t.Errorf("scoped form action=%q media=%q", action, media)
	}
}

func TestVimmDownloadGETURL(t *testing.T) {
	got := vimmGETURL("https://dl3.vimm.net/", "1624")
	if got != "https://dl3.vimm.net/?mediaId=1624" {
		t.Errorf("GET URL = %q, want https://dl3.vimm.net/?mediaId=1624", got)
	}
}

func TestVimmDownloadURLs_UsesFormHostOnly(t *testing.T) {
	got := vimmDownloadURLs("https://dl3.vimm.net/", "1624")
	if len(got) != 1 || got[0] != "https://dl3.vimm.net/?mediaId=1624" {
		t.Fatalf("got %v, want only the form-action GET", got)
	}
	for _, u := range got {
		if strings.Contains(u, "download3.vimm.net") || strings.Contains(u, "download") {
			t.Errorf("invented host in %q — downloadN.vimm.net is not a real Vimm hostname", u)
		}
	}
}

func TestVimmLooksLikeFile(t *testing.T) {
	html := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html; charset=UTF-8"}}}
	if vimmLooksLikeFile(html) {
		t.Error("HTML 200 should not look like a file")
	}
	zip := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/zip"}}}
	if !vimmLooksLikeFile(zip) {
		t.Error("application/zip 200 should look like a file")
	}
	bad := &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/zip"}}}
	if vimmLooksLikeFile(bad) {
		t.Error("HTTP 400 should not look like a file")
	}
	partial := &http.Response{StatusCode: 206, Header: http.Header{"Content-Type": []string{"application/zip"}}}
	if vimmLooksLikeFile(partial) {
		t.Error("HTTP 206 Content-Length is the slice size; treating it as a full file would save a truncated ROM")
	}
}

func TestDownloadVimmGame_UsesGETNotPOST(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			_, _ = io.WriteString(w, vimmGamePageHTML)
		case r.URL.Path == "/download":
			methods = append(methods, r.Method+" "+r.URL.RawQuery)
			if r.Method != http.MethodGet {
				http.Error(w, "POST is 400 on current Vimm", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("mediaId") != "1624" {
				http.Error(w, "missing mediaId", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="Super Metroid (Japan, USA).zip"`)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK\x03\x04fake-zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := newTestConfig(t)
	reg, err := sources.Default()
	if err != nil {
		t.Fatal(err)
	}
	reg.Vimm.BaseURL = srv.URL + "/vault/"
	cfg.Sources = reg
	// A configured solver is a challenge fallback, not a proxy for normal
	// pages. This deliberately points at a dead endpoint; the direct path must
	// still complete without touching it.
	cfg.FlareSolverrURL = "http://127.0.0.1:1"
	cfg.FlareSolverrMaxTimeout = 25_000

	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("1654", cfg.QBSavePath, jobID)
	if got == "" {
		job, _ := jobs.Get(jobID)
		t.Fatalf("download failed: %+v methods=%v", job, methods)
	}
	if filepath.Base(got) != "Super Metroid (Japan, USA).zip" {
		t.Errorf("filename = %q", filepath.Base(got))
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PK\x03\x04fake-zip" {
		t.Errorf("file contents = %q", body)
	}
	if len(methods) == 0 {
		t.Fatal("download endpoint was never hit — vault URL is still hardcoded")
	}
	if methods[0] != "GET mediaId=1624" {
		t.Errorf("download requests = %v, want GET with mediaId", methods)
	}
	for _, m := range methods {
		if strings.HasPrefix(m, "POST") {
			t.Errorf("Vimm rejects POST; should not have sent %q", m)
		}
	}
}

func TestDownloadVimmGame_ProtocolRelativeAction(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	var gotMethod, gotQuery string
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Disposition", `attachment; filename="game.zip"`)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK"))
	}))
	t.Cleanup(dl.Close)
	dlURL, err := url.Parse(dl.URL)
	if err != nil {
		t.Fatal(err)
	}
	page := fmt.Sprintf(`<form action="//%s/" method="POST" id="dl_form"><input type="hidden" name="mediaId" value="1624"></form>`, dlURL.Host)

	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, page)
	}))
	t.Cleanup(vault.Close)

	cfg := newTestConfig(t)
	reg, _ := sources.Default()
	reg.Vimm.BaseURL = vault.URL + "/vault/"
	cfg.Sources = reg
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("1654", cfg.QBSavePath, jobID)
	if got == "" {
		job, _ := jobs.Get(jobID)
		t.Fatalf("download failed: %+v", job)
	}
	if gotMethod != http.MethodGet || gotQuery != "mediaId=1624" {
		t.Errorf("dl host got %s %q, want GET mediaId=1624", gotMethod, gotQuery)
	}
}

func TestDownloadVimmGame_HTMLResponseIsError(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vault/") {
			_, _ = io.WriteString(w, vimmGamePageHTML)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "<html>rate limited</html>")
	}))
	t.Cleanup(srv.Close)

	cfg := newTestConfig(t)
	reg, _ := sources.Default()
	reg.Vimm.BaseURL = srv.URL + "/vault/"
	cfg.Sources = reg
	jobs := newTestJobs(t)
	m := New(cfg, jobs, nil)
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("1654", cfg.QBSavePath, jobID); got != "" {
		t.Errorf("HTML body should not be saved as a ROM, got %q", got)
	}
	job, _ := jobs.Get(jobID)
	errMsg, _ := job["error"].(string)
	if !strings.Contains(strings.ToLower(errMsg), "html") && !strings.Contains(strings.ToLower(errMsg), "web page") {
		t.Errorf("error = %q, want mention of HTML/web page", errMsg)
	}
}

// A trimmed copy of what vimm.net serves a plain HTTP client for any vault
// page since it moved behind Cloudflare Turnstile (issue #37). The download
// form is absent; only the challenge widget is present.
const vimmChallengePageHTML = `<!DOCTYPE html><html><head><title>Vimm's Lair</title></head><body>
<div id="challenge"><p>Checking if you are human.</p>
<div class="cf-turnstile" data-sitekey="0x4AAAAAAAcFgS2_wvnSBZF1" data-callback="onTurnstileSuccess" style="margin-top:8px"></div>
<form method="post"><input type="hidden" name="cf-turnstile-response" value=""></form></div>
</body></html>`

func TestVimmIsChallenge(t *testing.T) {
	if !vimmIsChallenge(vimmChallengePageHTML) {
		t.Error("Turnstile page not recognised as a challenge")
	}
	if vimmIsChallenge(vimmGamePageHTML) {
		t.Error("a normal vault page must not read as a challenge")
	}
	if vimmIsChallenge("<html>rate limited</html>") {
		t.Error("unrelated HTML must not read as a challenge")
	}
}

func newVimmChallengeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = io.WriteString(w, vimmChallengePageHTML)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newVimmManager(t *testing.T, vaultURL string) (*Manager, *db.JobStore) {
	t.Helper()
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })
	cfg := newTestConfig(t)
	reg, err := sources.Default()
	if err != nil {
		t.Fatal(err)
	}
	reg.Vimm.BaseURL = vaultURL
	cfg.Sources = reg
	jobs := newTestJobs(t)
	return New(cfg, jobs, nil), jobs
}

func TestDownloadVimmGame_TurnstileGateIsNamed(t *testing.T) {
	srv := newVimmChallengeServer(t)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("1654", m.cfg.QBSavePath, jobID); got != "" {
		t.Fatalf("challenge page must not produce a file, got %q", got)
	}
	job, _ := jobs.Get(jobID)
	if job["status"] != "error" {
		t.Errorf("status = %v, want error", job["status"])
	}
	errMsg, _ := job["error"].(string)
	if errMsg != vimmChallengeError {
		t.Errorf("error = %q, want the Turnstile explanation", errMsg)
	}
	if strings.Contains(errMsg, "Could not find download form") {
		t.Error("gate reported as a parsing miss")
	}
}

func TestDownloadVimmGame_UsesFlareSolverrMediaID(t *testing.T) {
	orig := vimmDownloadPause
	vimmDownloadPause = 0
	t.Cleanup(func() { vimmDownloadPause = orig })

	var srv *httptest.Server
	var solverRequests []struct {
		Command        string `json:"cmd"`
		URL            string `json:"url"`
		Session        string `json:"session"`
		MaxTimeout     int    `json:"maxTimeout"`
		WaitInSeconds  int    `json:"waitInSeconds"`
		TabsTillVerify *int   `json:"tabs_till_verify"`
	}
	var downloadCalls int
	var activeSession string
	sessionDestroyed := false
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = io.WriteString(w, vimmChallengePageHTML)
		case r.URL.Path == "/v1":
			var solverRequest struct {
				Command        string `json:"cmd"`
				URL            string `json:"url"`
				Session        string `json:"session"`
				MaxTimeout     int    `json:"maxTimeout"`
				WaitInSeconds  int    `json:"waitInSeconds"`
				TabsTillVerify *int   `json:"tabs_till_verify"`
			}
			if err := json.NewDecoder(r.Body).Decode(&solverRequest); err != nil {
				t.Errorf("decode FlareSolverr request: %v", err)
			}
			solverRequests = append(solverRequests, solverRequest)
			w.Header().Set("Content-Type", "application/json")
			switch solverRequest.Command {
			case "sessions.create":
				activeSession = solverRequest.Session
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "ok", "message": "Session created successfully.", "session": activeSession,
				})
			case "request.get":
				if solverRequest.Session != activeSession {
					http.Error(w, `{"status":"error","message":"wrong session"}`, http.StatusBadRequest)
					return
				}
				if solverRequest.TabsTillVerify != nil {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"status":"error","message":"Error: Error solving the challenge. Message: stale element reference: stale element not found"}`)
					return
				}
				page := fmt.Sprintf(`<html><body><form action="%s/download" id="dl_form"></form><script>let allMedia = [{"ID":3328,"Region":"USA"}];</script></body></html>`, srv.URL)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "ok", "solution": map[string]interface{}{
						"status": 200, "response": page, "userAgent": "solver-agent",
					},
				})
			case "sessions.destroy":
				if solverRequest.Session != activeSession {
					t.Errorf("destroy session = %q, want %q", solverRequest.Session, activeSession)
				}
				sessionDestroyed = true
				_, _ = io.WriteString(w, `{"status":"ok","message":"The session has been removed."}`)
			}
		case r.URL.Path == "/download":
			downloadCalls++
			if !sessionDestroyed {
				t.Error("download started before the temporary FlareSolverr session was destroyed")
			}
			if r.Method != http.MethodGet || r.URL.Query().Get("mediaId") != "3328" {
				t.Errorf("download request = %s %s, want GET ?mediaId=3328", r.Method, r.URL.String())
			}
			w.Header().Set("Content-Disposition", `attachment; filename="Solved Game.zip"`)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK\x03\x04solved"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	// These Config values represent startup environment configuration. Leave
	// tabs at the Config zero value to exercise the Vimm default of 74.
	timeout := 55_000
	m.cfg.FlareSolverrURL = srv.URL
	m.cfg.FlareSolverrMaxTimeout = timeout
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("4970", m.cfg.QBSavePath, jobID)
	if got == "" {
		job, _ := jobs.Get(jobID)
		t.Fatalf("download failed: %+v", job)
	}
	if len(solverRequests) != 4 || downloadCalls != 1 {
		t.Fatalf("solver requests=%+v download calls=%d, want four lifecycle calls and one download", solverRequests, downloadCalls)
	}
	wantCommands := []string{"sessions.create", "request.get", "request.get", "sessions.destroy"}
	for i, want := range wantCommands {
		if solverRequests[i].Command != want {
			t.Errorf("solver call %d command = %q, want %q", i, solverRequests[i].Command, want)
		}
		if solverRequests[i].Session != activeSession {
			t.Errorf("solver call %d session = %q, want %q", i, solverRequests[i].Session, activeSession)
		}
	}
	first, followup := solverRequests[1], solverRequests[2]
	if first.URL != srv.URL+"/vault/4970" || followup.URL != first.URL {
		t.Errorf("FlareSolverr URLs = %q then %q", first.URL, followup.URL)
	}
	if first.MaxTimeout != timeout || first.WaitInSeconds != 5 || followup.MaxTimeout != timeout || followup.WaitInSeconds != 2 {
		t.Errorf("FlareSolverr request timing = first (%d, %d), follow-up (%d, %d)", first.MaxTimeout, first.WaitInSeconds, followup.MaxTimeout, followup.WaitInSeconds)
	}
	if first.TabsTillVerify == nil || *first.TabsTillVerify != 74 || followup.TabsTillVerify != nil {
		t.Errorf("FlareSolverr tabs = first %v, follow-up %v", first.TabsTillVerify, followup.TabsTillVerify)
	}
	if filepath.Base(got) != "Solved Game.zip" {
		t.Errorf("downloaded file = %q", filepath.Base(got))
	}
}

func TestFetchWithFlareSolverrSerializesRequests(t *testing.T) {
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var getCalls atomic.Int32
	var commandMu sync.Mutex
	var commands []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			Command string `json:"cmd"`
			Session string `json:"session"`
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		commandMu.Lock()
		commands = append(commands, got.Command)
		commandMu.Unlock()
		switch got.Command {
		case "sessions.create":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "message": "Session created successfully.", "session": got.Session,
			})
		case "request.get":
			switch getCalls.Add(1) {
			case 1:
				close(firstEntered)
				<-releaseFirst
			case 2:
				secondEntered <- struct{}{}
			}
			_, _ = io.WriteString(w, `{"status":"ok","solution":{"response":"<html>ok</html>"}}`)
		case "sessions.destroy":
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		}
	}))
	t.Cleanup(srv.Close)

	m := New(newTestConfig(t), newTestJobs(t), nil)
	errs := make(chan error, 2)
	go func() {
		_, err := m.fetchWithFlareSolverr(context.Background(), srv.URL, "https://example.test/one", 55_000, 74)
		errs <- err
	}()
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first solver request did not arrive")
	}

	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := m.fetchWithFlareSolverr(context.Background(), srv.URL, "https://example.test/two", 55_000, 74)
		errs <- err
	}()
	<-secondStarted

	concurrent := false
	select {
	case <-secondEntered:
		concurrent = true
	case <-time.After(150 * time.Millisecond):
		// Expected: the second call is waiting for the one solver slot.
	}
	close(releaseFirst)
	if !concurrent {
		select {
		case <-secondEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("second solver request did not run after the first completed")
		}
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if concurrent {
		t.Error("FlareSolverr requests ran concurrently")
	}
	commandMu.Lock()
	defer commandMu.Unlock()
	if strings.Join(commands, ",") != "sessions.create,request.get,sessions.destroy,sessions.create,request.get,sessions.destroy" {
		t.Errorf("solver command order = %v, want two serialized session lifecycles", commands)
	}
}

func TestDownloadVimmGame_FlareSolverrStillSeesChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" {
			var got struct {
				Command string `json:"cmd"`
				Session string `json:"session"`
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			switch got.Command {
			case "sessions.create":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "ok", "message": "Session created successfully.", "session": got.Session,
				})
			case "request.get":
				solverChallenge := vimmChallengePageHTML + `<form id="dl_form" action="/download"><input name="mediaId" value="3328"></form>`
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "ok", "solution": map[string]interface{}{"response": solverChallenge},
				})
			case "sessions.destroy":
				_, _ = io.WriteString(w, `{"status":"ok"}`)
			}
			return
		}
		_, _ = io.WriteString(w, vimmChallengePageHTML)
	}))
	t.Cleanup(srv.Close)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	m.cfg.FlareSolverrURL = srv.URL
	m.cfg.FlareSolverrMaxTimeout = 55_000
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("4970", m.cfg.QBSavePath, jobID); got != "" {
		t.Fatalf("challenge page must not produce a file, got %q", got)
	}
	job, _ := jobs.Get(jobID)
	if errMsg, _ := job["error"].(string); errMsg != vimmFlareSolverrChallengeError {
		t.Errorf("error = %q, want solver challenge explanation", errMsg)
	}
}

func TestDownloadVimmGame_ChallengeOnDownloadHost(t *testing.T) {
	// The vault page still renders a form, but the download host answers with
	// the challenge instead of the file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		if strings.HasPrefix(r.URL.Path, "/vault/") {
			_, _ = io.WriteString(w, vimmGamePageHTML)
			return
		}
		_, _ = io.WriteString(w, vimmChallengePageHTML)
	}))
	t.Cleanup(srv.Close)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	if got := m.downloadVimmGame("1654", m.cfg.QBSavePath, jobID); got != "" {
		t.Fatalf("challenge page must not be saved as a ROM, got %q", got)
	}
	job, _ := jobs.Get(jobID)
	if errMsg, _ := job["error"].(string); errMsg != vimmChallengeError {
		t.Errorf("error = %q, want the Turnstile explanation", errMsg)
	}
}

func TestDDLSourceName(t *testing.T) {
	cfg := newTestConfig(t)
	reg, _ := sources.Default()
	reg.Myrient.BaseURL = "http://127.0.0.1:9/files/"
	cfg.Sources = reg
	m := New(cfg, newTestJobs(t), nil)

	cases := []struct{ url, vimmID, want string }{
		{"", "1654", "vimm"},
		{"http://127.0.0.1:9/files/gb/Tetris.zip", "", "myrient"},
		{"https://myrient.erista.me/files/No-Intro/x.zip", "", "myrient"},
		{"http://127.0.0.1:9/elsewhere/x.zip", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := m.ddlSourceName(c.url, c.vimmID); got != c.want {
			t.Errorf("ddlSourceName(%q, %q) = %q, want %q", c.url, c.vimmID, got, c.want)
		}
	}
}

// The reporter's health panel read score 100 / 0 failed downloads after every
// Vimm download had failed: nothing on the DDL path ever recorded a download
// outcome. The worker must feed the same health store searches do.
func TestDDLWorker_RecordsVimmDownloadFailures(t *testing.T) {
	srv := newVimmChallengeServer(t)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	search.ResetCircuit("vimm")
	t.Cleanup(func() { search.ResetCircuit("vimm") })

	before := 0
	if h := search.GetSourceHealth("vimm"); h != nil {
		before = h.DownloadFail
	}

	for i := 0; i < 3; i++ {
		jobID := newJobID()
		jobs.Set(jobID, map[string]interface{}{"status": "downloading"})
		m.ddlDownloadWorker(jobID, "", "1654", "Super Metroid", "SNES", "snes", false)
		job, _ := jobs.Get(jobID)
		if errMsg, _ := job["error"].(string); errMsg != vimmChallengeError {
			t.Fatalf("run %d: job error = %q, want the Turnstile explanation", i, errMsg)
		}
		if i < 2 && search.IsDownloadDegraded("vimm") {
			t.Fatalf("run %d: degraded before the threshold", i)
		}
	}

	h := search.GetSourceHealth("vimm")
	if h == nil {
		t.Fatal("no health recorded for vimm")
	}
	if h.DownloadFail != before+3 {
		t.Errorf("download_fail = %d, want %d", h.DownloadFail, before+3)
	}
	if h.LastErrorKind != "download" || h.LastError != vimmChallengeError {
		t.Errorf("last error = (%s, %q), want the download-side Turnstile message", h.LastErrorKind, h.LastError)
	}
	if h.Score == 100 {
		t.Error("score still 100 after three failed downloads")
	}
	if !h.DownloadDegraded || !search.IsDownloadDegraded("vimm") {
		t.Error("three consecutive failed downloads should mark the source degraded")
	}
	if !h.CircuitOpen {
		t.Error("three consecutive failed downloads should open the circuit")
	}
}

func TestDDLWorker_RecordsVimmDownloadSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vault/") {
			_, _ = io.WriteString(w, vimmGamePageHTML)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="Super Metroid (Japan, USA).zip"`)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK\x03\x04fake-zip"))
	}))
	t.Cleanup(srv.Close)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	search.ResetCircuit("vimm")
	t.Cleanup(func() { search.ResetCircuit("vimm") })

	// Start degraded: a delivered file is what clears it.
	for i := 0; i < 3; i++ {
		search.RecordDownloadFail("vimm", "earlier gate")
	}
	before := search.GetSourceHealth("vimm").DownloadOK

	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})
	m.ddlDownloadWorker(jobID, "", "1654", "Super Metroid", "SNES", "snes", false)

	h := search.GetSourceHealth("vimm")
	if h.DownloadOK != before+1 {
		t.Errorf("download_ok = %d, want %d", h.DownloadOK, before+1)
	}
	if search.IsDownloadDegraded("vimm") {
		t.Error("a delivered file should clear the degraded flag")
	}
}

func TestRetryJobRestartsVimmDownloadOnTheSameRow(t *testing.T) {
	var available atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/") && !available.Load():
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = io.WriteString(w, vimmChallengePageHTML)
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = io.WriteString(w, vimmGamePageHTML)
		case r.URL.Path == "/download":
			w.Header().Set("Content-Disposition", `attachment; filename="retry-fixture.zip"`)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write([]byte("PK\x03\x04retry-fixture"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	search.ResetCircuit("vimm")
	t.Cleanup(func() { search.ResetCircuit("vimm") })

	// Let a failed retry cross the threshold. RetryJob deliberately bypasses
	// discovery's open circuit; a later successful delivery then recovers both
	// the degraded flag and the circuit itself.
	search.RecordDownloadFail("vimm", "earlier gate")
	jobID := m.DownloadDDL("", "4970", "Vimm Retry Fixture", "SNES", "snes", false)
	failed := waitJobStatus(t, jobs, jobID, "error", minPollTimeout)
	if failed["vimm_id"] != "4970" || failed["source_type"] != "ddl" {
		t.Fatalf("job did not retain its replay inputs: %v", failed)
	}
	waitFor(t, minPollTimeout, "the initial Vimm worker to finish", func() bool {
		_, busy := m.activeDDL.Load(jobID)
		return !busy
	})

	ok, msg := m.RetryJob(jobID)
	if !ok {
		t.Fatalf("RetryJob refused a replayable Vimm job: %s", msg)
	}
	waitJobStatus(t, jobs, jobID, "error", minPollTimeout)
	waitFor(t, minPollTimeout, "the failed Vimm retry to finish", func() bool {
		_, busy := m.activeDDL.Load(jobID)
		return !busy
	})
	if !search.IsCircuitOpen("vimm") || !search.IsDownloadDegraded("vimm") {
		t.Fatal("fixture did not begin with a degraded, open Vimm source")
	}

	available.Store(true)
	ok, msg = m.RetryJob(jobID)
	if !ok {
		t.Fatalf("RetryJob refused a replayable Vimm job: %s", msg)
	}
	if msg != "Retrying (#2)" {
		t.Errorf("second retry message = %q, want Retrying (#2)", msg)
	}
	completed := waitJobStatus(t, jobs, jobID, "completed", minPollTimeout)
	if completed["error"] != nil && completed["error"] != "" {
		t.Errorf("completed retry retained error %v", completed["error"])
	}
	if got := jobRetryCount(completed); got != 2 {
		t.Errorf("retry_count = %d, want 2", got)
	}
	if got := len(jobs.Items()); got != 1 {
		t.Errorf("job rows = %d, want the retry to reuse its existing row", got)
	}
	if search.IsCircuitOpen("vimm") || search.IsDownloadDegraded("vimm") {
		t.Error("successful Vimm retry did not restore source health")
	}
}

func TestRetryJobRefusesWhileVimmWorkerIsStillActive(t *testing.T) {
	m, jobs := newVimmManager(t, "http://127.0.0.1:1/vault/")
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{
		"status": "error", "title": "Vimm Retry Fixture", "source_type": "ddl", "vimm_id": "4970",
	})
	m.activeDDL.Store(jobID, struct{}{})
	t.Cleanup(func() { m.activeDDL.Delete(jobID) })

	if ok, msg := m.RetryJob(jobID); ok || !strings.Contains(strings.ToLower(msg), "already running") {
		t.Fatalf("retry while active = (%v, %q), want an already-running refusal", ok, msg)
	}
	job, _ := jobs.Get(jobID)
	if job["status"] != "error" || job["error"] != nil {
		t.Errorf("refused retry changed the job: %v", job)
	}
}
