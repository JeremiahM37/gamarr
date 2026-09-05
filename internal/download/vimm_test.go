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
	var solverCalls, downloadCalls int
	var solverRequest struct {
		Command        string `json:"cmd"`
		URL            string `json:"url"`
		MaxTimeout     int    `json:"maxTimeout"`
		WaitInSeconds  int    `json:"waitInSeconds"`
		TabsTillVerify int    `json:"tabs_till_verify"`
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vault/"):
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = io.WriteString(w, vimmChallengePageHTML)
		case r.URL.Path == "/v1":
			solverCalls++
			if err := json.NewDecoder(r.Body).Decode(&solverRequest); err != nil {
				t.Errorf("decode FlareSolverr request: %v", err)
			}
			page := fmt.Sprintf(`<html><body><form action="%s/download" id="dl_form"></form><script>let allMedia = [{"ID":3811,"Region":"USA"}];</script></body></html>`, srv.URL)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"solution": map[string]interface{}{
					"status": 200, "response": page, "userAgent": "solver-agent",
				},
			})
		case r.URL.Path == "/download":
			downloadCalls++
			if r.Method != http.MethodGet || r.URL.Query().Get("mediaId") != "3811" {
				t.Errorf("download request = %s %s, want GET ?mediaId=3811", r.Method, r.URL.String())
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
	timeout := 25_000
	m.cfg.FlareSolverrURL = srv.URL
	m.cfg.FlareSolverrMaxTimeout = timeout
	jobID := newJobID()
	jobs.Set(jobID, map[string]interface{}{"status": "downloading"})

	got := m.downloadVimmGame("4970", m.cfg.QBSavePath, jobID)
	if got == "" {
		job, _ := jobs.Get(jobID)
		t.Fatalf("download failed: %+v", job)
	}
	if solverCalls != 1 || downloadCalls != 1 {
		t.Fatalf("solver calls=%d download calls=%d, want one each", solverCalls, downloadCalls)
	}
	if solverRequest.Command != "request.get" || solverRequest.URL != srv.URL+"/vault/4970" {
		t.Errorf("FlareSolverr request = %+v", solverRequest)
	}
	if solverRequest.MaxTimeout != timeout || solverRequest.WaitInSeconds != 5 {
		t.Errorf("FlareSolverr timeouts = max %d wait %d", solverRequest.MaxTimeout, solverRequest.WaitInSeconds)
	}
	if solverRequest.TabsTillVerify != 74 {
		t.Errorf("FlareSolverr tabs_till_verify = %d, want 74", solverRequest.TabsTillVerify)
	}
	if filepath.Base(got) != "Solved Game.zip" {
		t.Errorf("downloaded file = %q", filepath.Base(got))
	}
}

func TestFetchWithFlareSolverrSerializesRequests(t *testing.T) {
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			close(firstEntered)
			<-releaseFirst
		case 2:
			secondEntered <- struct{}{}
		}
		_, _ = io.WriteString(w, `{"status":"ok","solution":{"response":"<html>ok</html>"}}`)
	}))
	t.Cleanup(srv.Close)

	m := New(newTestConfig(t), newTestJobs(t), nil)
	errs := make(chan error, 2)
	go func() {
		_, err := m.fetchWithFlareSolverr(context.Background(), srv.URL, "https://example.test/one", 25_000, 74)
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
		_, err := m.fetchWithFlareSolverr(context.Background(), srv.URL, "https://example.test/two", 25_000, 74)
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
}

func TestDownloadVimmGame_FlareSolverrStillSeesChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "solution": map[string]interface{}{"response": vimmChallengePageHTML},
			})
			return
		}
		_, _ = io.WriteString(w, vimmChallengePageHTML)
	}))
	t.Cleanup(srv.Close)
	m, jobs := newVimmManager(t, srv.URL+"/vault/")
	m.cfg.FlareSolverrURL = srv.URL
	m.cfg.FlareSolverrMaxTimeout = 25_000
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
